package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

// IssueBootstrap creates a host-authorized short-lived first-Administrator credential.
func (service *Service) IssueBootstrap(ctx context.Context) (string, error) {
	if service.storageDegraded() {
		return "", ErrStorageDegraded
	}
	token, err := service.newToken()
	if err != nil {
		return "", err
	}
	now := service.now().UTC()
	if err := service.storage.IssueBootstrap(
		ctx,
		tokenDigest(token),
		now,
		now.Add(service.bootstrapTTL),
	); err != nil {
		return "", err
	}
	return token, nil
}

// SetupRequired reports whether browser setup still needs a first Account.
func (service *Service) SetupRequired(ctx context.Context) (bool, error) {
	if service.storageDegraded() {
		return false, ErrStorageDegraded
	}
	return service.storage.SetupRequired(ctx)
}

// BootstrapAdministrator consumes a bootstrap credential and creates the first
// Administrator together with an authenticated session.
func (service *Service) BootstrapAdministrator(
	ctx context.Context,
	bootstrapToken string,
	name string,
	password string,
) (Session, error) {
	return service.BootstrapFirstAccount(ctx, bootstrapToken, name, name, password)
}

// BootstrapFirstAccount consumes a bootstrap credential and creates the first
// Administrator Account with separate sign-in and display names.
func (service *Service) BootstrapFirstAccount(
	ctx context.Context,
	bootstrapToken string,
	handle string,
	displayName string,
	password string,
) (Session, error) {
	if service.storageDegraded() {
		return Session{}, ErrStorageDegraded
	}
	normalizedName, _, handleErr := normalizeAccountName(handle)
	displayName, displayErr := normalizeDisplayName(displayName)
	if handleErr != nil || displayErr != nil ||
		!service.validPassword(password) || !validToken(bootstrapToken) {
		return Session{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	sessionToken, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	userHandle, err := service.newWebAuthnUserHandle()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	created, err := service.storage.BootstrapAdministrator(
		ctx,
		store.BootstrapAdministratorParams{
			BootstrapHash:      tokenDigest(bootstrapToken),
			Name:               displayName,
			NormalizedName:     normalizedName,
			WebAuthnUserHandle: userHandle,
			PasswordHash:       passwordHash,
			SessionHash:        tokenDigest(sessionToken),
			Now:                now,
			SessionExpiry:      expiresAt,
		},
	)
	if errors.Is(err, store.ErrInvalidBootstrap) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	session := newSession(sessionToken, expiresAt, created)
	service.rememberSession(sessionToken, session.Account, expiresAt)
	return session, nil
}

// RegistrationOpen reports whether visitors may create Accounts.
func (service *Service) RegistrationOpen(ctx context.Context) (bool, error) {
	if service.storageDegraded() {
		return false, ErrStorageDegraded
	}
	return service.storage.RegistrationOpen(ctx)
}

// SetRegistrationOpen updates the installation Registration Policy.
func (service *Service) SetRegistrationOpen(
	ctx context.Context,
	actor Account,
	open bool,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if !actor.Administrator {
		return ErrAdministratorRequired
	}
	return service.storage.SetRegistrationOpen(actor.Context(ctx), open)
}

// Register creates one visitor Account without requiring email.
func (service *Service) Register(
	ctx context.Context,
	handle string,
	displayName string,
	password string,
) (Account, error) {
	if service.storageDegraded() {
		return Account{}, ErrStorageDegraded
	}
	normalizedHandle, _, handleErr := normalizeAccountName(handle)
	displayName, displayErr := normalizeDisplayName(displayName)
	if handleErr != nil || displayErr != nil || !service.validPassword(password) {
		return Account{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Account{}, err
	}
	userHandle, err := service.newWebAuthnUserHandle()
	if err != nil {
		return Account{}, err
	}
	created, err := service.storage.RegisterAccount(ctx, store.CreateAccountParams{
		Name:               displayName,
		NormalizedName:     normalizedHandle,
		WebAuthnUserHandle: userHandle,
		PasswordHash:       passwordHash,
		Now:                service.now().UTC(),
	})
	if err != nil {
		return Account{}, err
	}
	return account(created), nil
}

// Profile returns the authenticated Account's private Profile settings.
func (service *Service) Profile(ctx context.Context, actor Account) (Profile, error) {
	found, exists, err := service.storage.AccountProfile(actor.Context(ctx), actor.ID)
	if err != nil {
		return Profile{}, err
	}
	result := Profile{Handle: actor.Handle, DisplayName: actor.Name}
	if exists {
		result = profile(found)
	}
	available, err := service.storage.ReleasedProfileEntries(actor.Context(ctx))
	if err != nil {
		return Profile{}, err
	}
	selected := make(map[int]struct{}, len(result.Entries))
	for _, entry := range result.Entries {
		selected[entry.ID] = struct{}{}
	}
	result.AvailableEntries = make([]ProfileEntry, 0, len(available))
	for _, entry := range available {
		_, ok := selected[entry.ID]
		result.AvailableEntries = append(
			result.AvailableEntries,
			ProfileEntry{ID: entry.ID, Name: entry.Name, Selected: ok},
		)
	}
	return result, nil
}

// PublicProfile returns the published Profile matching a Handle.
func (service *Service) PublicProfile(
	ctx context.Context,
	handle string,
) (Profile, bool, error) {
	normalizedHandle, valid := publicProfileHandle(handle)
	if !valid {
		return Profile{}, false, nil
	}
	found, ok, err := service.storage.PublicProfile(ctx, normalizedHandle)
	if err != nil || !ok {
		return Profile{}, ok, err
	}
	return profile(found), true, nil
}

func publicProfileHandle(handle string) (string, bool) {
	normalized, _, err := normalizeAccountName(handle)
	return normalized, err == nil
}

// UpdateProfile changes one Account's Display Name and public selection.
func (service *Service) UpdateProfile(
	ctx context.Context,
	actor Account,
	displayName string,
	published bool,
	entryIDs []int,
	commandID string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	displayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return ErrInvalidAccountDetails
	}
	for _, entryID := range entryIDs {
		if entryID <= 0 {
			return ErrProfileEntryUnavailable
		}
	}
	if validateErr := command.ValidateID(commandID); validateErr != nil {
		return ErrInvalidAccountDetails
	}
	// Canonicalize EntryIDs before hashing so equivalent submissions (same
	// Entries in a different order, or with duplicates) share one command
	// identity instead of colliding as a payload mismatch. This mirrors the
	// canonicalization the store applies before persisting.
	entryIDs = slices.Compact(slices.Sorted(slices.Values(entryIDs)))
	payload, err := json.Marshal(struct {
		DisplayName string `json:"display_name"`
		Published   bool   `json:"published"`
		EntryIDs    []int  `json:"entry_ids"`
	}{DisplayName: displayName, Published: published, EntryIDs: entryIDs})
	if err != nil {
		return errors.New("encode Update Profile command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "UpdateAccountProfile",
		TargetType: "Account", TargetID: strconv.Itoa(actor.ID), Now: service.now().UTC(),
	}
	_, err = command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
		Replay: func(outcome string) (struct{}, error) {
			var original store.AccountProfile
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return struct{}{}, restoreRejected(decodeErr)
			}
			return struct{}{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			updated, updateErr := transaction.UpdateAccountProfile(actor.Context(ctx), store.UpdateAccountProfileParams{
				AccountID: actor.ID, AccountHandle: actor.Handle,
				DisplayName: displayName, Published: published, EntryIDs: entryIDs,
			})
			if errors.Is(updateErr, ErrProfileEntryUnavailable) {
				return accountRejectionExecution[struct{}](ErrProfileEntryUnavailable), nil
			}
			if updateErr != nil {
				return command.Execution[struct{}]{}, updateErr
			}
			encoded, encodeErr := json.Marshal(updated)
			if encodeErr != nil {
				return command.Execution[struct{}]{}, errors.New("encode Update Profile outcome")
			}
			return command.Success(struct{}{}, string(encoded)), nil
		},
		// Applied fires only after a fresh Apply commits, never after a
		// Replay of an earlier command. Adopting displayName into the
		// session cache here (rather than from command.Execute's return
		// value) guards against a replayed older command clobbering the
		// cache with its own stale, historical Display Name.
		Applied: func() {
			service.sessionMu.Lock()
			for token, session := range service.sessions {
				if session.account.ID == actor.ID {
					session.account.Name = displayName
					service.sessions[token] = session
				}
			}
			service.sessionMu.Unlock()
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func profile(found store.AccountProfile) Profile {
	entries := make([]ProfileEntry, 0, len(found.Entries))
	for _, entry := range found.Entries {
		entries = append(entries, ProfileEntry{ID: entry.ID, Name: entry.Name})
	}
	return Profile{
		Handle: found.Handle, DisplayName: found.DisplayName,
		Published: found.Published, Entries: entries,
	}
}

// CreateAccount creates an individual non-Administrator Account.
func (service *Service) CreateAccount(
	ctx context.Context,
	actor Account,
	name string,
	password string,
	commandID string,
) (Account, error) {
	return service.createAccount(ctx, actor, name, "", false, password, commandID)
}

// CreateAccountWithDisplayName creates an Account with a distinct Handle and Display Name.
func (service *Service) CreateAccountWithDisplayName(
	ctx context.Context,
	actor Account,
	handle string,
	displayName string,
	password string,
	commandID string,
) (Account, error) {
	return service.createAccount(ctx, actor, handle, displayName, true, password, commandID)
}

func (service *Service) createAccount(
	ctx context.Context,
	actor Account,
	handle string,
	displayName string,
	requireDisplayName bool,
	password string,
	commandID string,
) (Account, error) {
	if service.storageDegraded() {
		return Account{}, ErrStorageDegraded
	}
	identityName := handle
	defaultDisplayName := handle
	if normalizedName, normalizedDisplayName, normalizeErr := normalizeAccountName(handle); normalizeErr == nil {
		identityName = normalizedName
		defaultDisplayName = normalizedDisplayName
	}
	identityDisplayName := displayName
	switch {
	case requireDisplayName:
		if normalizedDisplayName, normalizeErr := normalizeDisplayName(displayName); normalizeErr == nil {
			identityDisplayName = normalizedDisplayName
		}
	case displayName == "":
		identityDisplayName = defaultDisplayName
	default:
		if normalizedDisplayName, normalizeErr := normalizeDisplayName(displayName); normalizeErr == nil {
			identityDisplayName = normalizedDisplayName
		}
	}
	payloadHash := command.PayloadHash(identityName)
	if requireDisplayName {
		payloadHash = command.PayloadHash(identityName, identityDisplayName)
	}
	if err := command.ValidateID(commandID); err != nil {
		return Account{}, ErrInvalidAccountDetails
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID, PayloadHash: payloadHash,
		Action: "CreateAccount", TargetType: "Account", TargetID: "unidentified", Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Account]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
		Replay: func(outcome string) (Account, error) {
			var original store.AccountCredential
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return Account{}, restoreRejected(decodeErr)
			}
			return account(original), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Account], error) {
			if !actor.Administrator {
				return accountRejectionExecution[Account](ErrAdministratorRequired), nil
			}
			normalizedName, normalizedDisplayName, normalizeErr := normalizeAccountName(handle)
			if requireDisplayName && normalizeErr == nil {
				normalizedDisplayName, normalizeErr = normalizeDisplayName(displayName)
			}
			if normalizeErr != nil || !service.validPassword(password) {
				// The executor returns this rejection only after its Receipt and Audit Entry commit.
				//nolint:nilerr // A callback error would roll back the durable rejection.
				return accountRejectionExecution[Account](ErrInvalidAccountDetails), nil
			}
			passwordHash, hashErr := service.hashPassword(password)
			if hashErr != nil {
				return command.Execution[Account]{}, hashErr
			}
			userHandle, handleErr := service.newWebAuthnUserHandle()
			if handleErr != nil {
				return command.Execution[Account]{}, handleErr
			}
			created, createErr := transaction.CreateAccount(actor.Context(ctx), store.CreateAccountParams{
				ActorAccountID:     actor.ID,
				Name:               normalizedDisplayName,
				NormalizedName:     normalizedName,
				WebAuthnUserHandle: userHandle,
				PasswordHash:       passwordHash,
				Now:                identity.Now,
				CommandID:          commandID,
				PayloadHash:        payloadHash,
			})
			if errors.Is(createErr, ErrAccountExists) {
				return accountRejectionExecution[Account](createErr), nil
			}
			if createErr != nil {
				return command.Execution[Account]{}, createErr
			}
			encoded, encodeErr := json.Marshal(created)
			if encodeErr != nil {
				return command.Execution[Account]{}, errors.New("encode Account creation outcome")
			}
			return command.Success(account(created), string(encoded)).WithTargetID(strconv.Itoa(created.ID)), nil
		},
	})
}

// DisableAccount retires one enabled Account while preserving a final Administrator.
func (service *Service) DisableAccount(
	ctx context.Context,
	actor Account,
	accountID int,
	commandID string,
	reason string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil {
		return ErrInvalidAccountDetails
	}
	reason = strings.TrimSpace(reason)
	payload, err := json.Marshal(struct {
		AccountID int    `json:"account_id"`
		Reason    string `json:"reason"`
	}{AccountID: accountID, Reason: reason})
	if err != nil {
		return errors.New("encode Disable Account command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "DisableAccount",
		TargetType: "Account", TargetID: strconv.Itoa(accountID), Now: service.now().UTC(),
	}
	_, err = command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
		Replay: func(outcome string) (struct{}, error) {
			var original store.DisabledAccount
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return struct{}{}, restoreRejected(decodeErr)
			}
			return struct{}{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			if !actor.Administrator {
				return accountRejectionExecution[struct{}](ErrAdministratorRequired), nil
			}
			if !validDisableReason(reason) {
				return accountRejectionExecution[struct{}](ErrDisableReasonRequired), nil
			}
			disabled, disableErr := transaction.DisableAccount(actor.Context(ctx), accountID, identity.Now)
			if errors.Is(disableErr, ErrDisableAccountNotFound) || errors.Is(disableErr, ErrLastAdministrator) {
				return accountRejectionExecution[struct{}](disableErr), nil
			}
			if disableErr != nil {
				return command.Execution[struct{}]{}, disableErr
			}
			encoded, encodeErr := json.Marshal(disabled)
			if encodeErr != nil {
				return command.Execution[struct{}]{}, errors.New("encode Disable Account outcome")
			}
			return command.Success(struct{}{}, string(encoded)).WithAudit(store.AuditDetails{Reason: reason}), nil
		},
	})
	return err
}

func accountRejectionExecution[T any](reason error) command.Execution[T] {
	rejection := accountRejection(reason)
	var zero T
	// A fresh application must return the same error a replay restores, so a
	// code that classifies from a storage sentinel but restores its own
	// error (credential_not_found, below) reports consistently either way.
	return command.Reject(zero, rejection, accountRejections.Return(reason))
}

// accountRejections is the single source for Account command rejection codes
// in both directions. An unclassified failure records "unavailable", which no
// sentinel claims, so a replay of it reports the command as unavailable rather
// than inventing a specific cause.
var accountRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
		{Err: ErrInvalidAccountDetails, Code: "invalid_account_details"},
		{Err: ErrAccountExists, Code: "account_exists"},
		{Err: ErrDisableAccountNotFound, Code: "account_not_found"},
		{Err: ErrDisableReasonRequired, Code: "disable_reason_required"},
		{Err: ErrRecoveryReasonRequired, Code: "recovery_reason_required"},
		{Err: ErrRecoveryAccountNotFound, Code: "recovery_account_not_found"},
		{Err: ErrLastAdministrator, Code: "last_administrator"},
		{Err: ErrFinalCredential, Code: "final_credential"},
		{Err: ErrProfileEntryUnavailable, Code: "profile_entry_unavailable"},
		{Err: store.ErrInvalidSession, Code: "credential_not_found", Restored: ErrInvalidSession},
	},
}

func accountRejection(reason error) store.CommandRejection {
	rejection, known := accountRejections.Rejection(reason)
	if !known {
		return store.CommandRejection{Code: "unavailable"}
	}
	return rejection
}

func validDisableReason(value string) bool {
	switch value {
	case "crew_departed", "access_revoked", "account_compromised", "duplicate_account":
		return true
	default:
		return false
	}
}

// ListAccounts returns selectable enabled Accounts for an Administrator.
func (service *Service) ListAccounts(
	ctx context.Context,
	actor Account,
) ([]Account, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListAccounts(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(found))
	for _, item := range found {
		accounts = append(accounts, account(item))
	}
	return accounts, nil
}

// ListAuditEntries returns installation history to an Administrator.
func (service *Service) ListAuditEntries(
	ctx context.Context,
	actor Account,
) ([]AuditEntry, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListAuditEntries(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	entries := make([]AuditEntry, 0, len(found))
	for _, item := range found {
		actorKind := ""
		if item.ActorKind != "Account" {
			actorKind = item.ActorKind
		}
		entries = append(entries, AuditEntry{
			ID: item.ID, ActorKind: actorKind,
			ActorAccountID: item.ActorAccountID, ActorName: item.ActorName,
			ServerTime: item.ServerTime, Action: item.Action,
			TargetType: item.TargetType, TargetID: item.TargetID,
			Outcome: item.Outcome, Reason: item.Reason, Note: item.Note,
		})
	}
	return entries, nil
}

// SetReducedEffects persists the preference for the authenticated Account.
func (service *Service) SetReducedEffects(
	ctx context.Context,
	token string,
	enabled bool,
) error {
	authenticated, err := service.authenticate(ctx, token)
	if err != nil {
		return err
	}
	return service.storage.SetAccountReducedEffects(ctx, authenticated.ID, enabled)
}

// ReducedEffects returns the preference for the authenticated Account.
func (service *Service) ReducedEffects(
	ctx context.Context,
	token string,
) (bool, error) {
	authenticated, err := service.authenticate(ctx, token)
	if err != nil {
		return false, err
	}
	return service.storage.AccountReducedEffects(ctx, authenticated.ID)
}

func (service *Service) newWebAuthnUserHandle() ([]byte, error) {
	handle := make([]byte, webAuthnUserHandleBytes)
	if _, err := io.ReadFull(service.random, handle); err != nil {
		return nil, errors.New("generate WebAuthn user handle")
	}
	return handle, nil
}

func normalizeAccountName(name string) (normalized, display string, err error) {
	display, err = normalizeDisplayName(name)
	if err != nil {
		return "", "", err
	}
	return strings.ToLower(display), display, nil
}

func normalizeDisplayName(name string) (string, error) {
	display := strings.TrimSpace(name)
	if display == "" || utf8.RuneCountInString(display) > 200 || !utf8.ValidString(display) {
		return "", ErrInvalidAccountDetails
	}
	for _, character := range display {
		if unicode.IsControl(character) {
			return "", ErrInvalidAccountDetails
		}
	}
	return display, nil
}

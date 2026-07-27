package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

// FederatedIdentity is one Account-owned provider Credential.
type FederatedIdentity struct {
	ID       int
	Provider string
	Subject  string
	Revoked  bool
}

// LinkFederatedIdentity attaches a verified provider subject to an authenticated Account.
func (service *Service) LinkFederatedIdentity(
	ctx context.Context,
	actor Account,
	provider string,
	subject string,
	commandID string,
) (FederatedIdentity, error) {
	if service.storageDegraded() {
		return FederatedIdentity{}, ErrStorageDegraded
	}
	if actor.ID <= 0 {
		return FederatedIdentity{}, ErrInvalidSession
	}
	provider, subject, err := validateFederatedIdentity(provider, subject)
	if err != nil || command.ValidateID(commandID) != nil {
		return FederatedIdentity{}, ErrInvalidAccountDetails
	}
	now := service.now().UTC()
	type linkOutcome struct {
		ID        int    `json:"id"`
		AccountID int    `json:"account_id"`
		Provider  string `json:"provider"`
	}
	result, err := command.Execute(actor.Context(ctx), command.Plan[FederatedIdentity]{
		Storage: service.storage,
		Identity: store.CommandIdentity{
			ActorAccountID: actor.ID,
			CommandID:      commandID,
			PayloadHash:    command.PayloadHash(provider, subject),
			Action:         "LinkFederatedIdentity",
			TargetType:     "Credential",
			TargetID:       "unidentified",
			Now:            now,
		},
		Replay: func(outcome string) (FederatedIdentity, error) {
			var original linkOutcome
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return FederatedIdentity{}, restoreRejected(decodeErr)
			}
			return FederatedIdentity{
				ID: original.ID, Provider: original.Provider, Subject: subject,
			}, nil
		},
		Apply: func(transaction *store.CommandTx) (
			command.Execution[FederatedIdentity],
			error,
		) {
			linked, linkErr := transaction.LinkFederatedIdentity(
				actor.Context(ctx),
				actor.ID,
				provider,
				subject,
				now,
			)
			if errors.Is(linkErr, store.ErrAccountExists) {
				return accountRejectionExecution[FederatedIdentity](ErrAccountExists), nil
			}
			if linkErr != nil {
				return command.Execution[FederatedIdentity]{}, linkErr
			}
			encoded, encodeErr := json.Marshal(linkOutcome{
				ID: linked.ID, AccountID: linked.AccountID, Provider: linked.Provider,
			})
			if encodeErr != nil {
				return command.Execution[FederatedIdentity]{}, errors.New(
					"encode Federated Identity link outcome",
				)
			}
			return command.Success(
				federatedIdentity(linked),
				string(encoded),
			).WithTargetID(strconv.Itoa(linked.ID)), nil
		},
	})
	return result, err
}

// ListFederatedIdentities returns Account-visible provider Credential metadata.
func (service *Service) ListFederatedIdentities(
	ctx context.Context,
	actor Account,
) ([]FederatedIdentity, error) {
	if actor.ID <= 0 {
		return nil, ErrInvalidSession
	}
	found, err := service.storage.ListFederatedIdentities(actor.Context(ctx), actor.ID)
	if err != nil {
		return nil, err
	}
	result := make([]FederatedIdentity, 0, len(found))
	for _, identity := range found {
		result = append(result, federatedIdentity(identity))
	}
	return result, nil
}

// FederatedSignIn signs in a provider subject or creates its distinct Account.
func (service *Service) FederatedSignIn(
	ctx context.Context,
	provider string,
	subject string,
	displayName string,
	allowAccountCreation bool,
) (Session, bool, error) {
	if service.storageDegraded() {
		return Session{}, false, ErrStorageDegraded
	}
	provider, subject, err := validateFederatedIdentity(provider, subject)
	if err != nil {
		return Session{}, false, ErrAuthenticationFailed
	}
	displayName, err = normalizeDisplayName(displayName)
	if err != nil {
		displayName = "Federated Account"
	}
	handle := federatedHandle(provider, subject)
	userHandle, err := service.newWebAuthnUserHandle()
	if err != nil {
		return Session{}, false, err
	}
	token, err := service.newToken()
	if err != nil {
		return Session{}, false, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	found, revoked, created, err := service.storage.CreateFederatedSession(
		ctx,
		store.FederatedSessionParams{
			Provider: provider, Subject: subject,
			Name: displayName, NormalizedName: handle, WebAuthnUserHandle: userHandle,
			AllowAccountCreation: allowAccountCreation,
			SessionHash:          tokenDigest(token), Now: now, SessionExpiry: expiresAt,
		},
	)
	if errors.Is(err, store.ErrInvalidSession) {
		return Session{}, false, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, false, err
	}
	service.pruneSessionCache(now, revoked)
	session := newSession(token, expiresAt, found)
	service.rememberSession(token, session.Account, expiresAt)
	return session, created, nil
}

func validateFederatedIdentity(provider, subject string) (string, string, error) {
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	if provider == "" || len(provider) > 64 || subject == "" || len(subject) > 1024 {
		return "", "", ErrInvalidAccountDetails
	}
	for index, char := range provider {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			char == '-' && index > 0 && index < len(provider)-1 {
			continue
		}
		return "", "", ErrInvalidAccountDetails
	}
	return provider, subject, nil
}

func federatedHandle(provider, subject string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + subject))
	return provider + "-" + hex.EncodeToString(sum[:])
}

func federatedIdentity(found store.FederatedIdentity) FederatedIdentity {
	return FederatedIdentity{
		ID: found.ID, Provider: found.Provider, Subject: found.Subject,
		Revoked: found.RevokedAt != nil,
	}
}

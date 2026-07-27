package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

const (
	webAuthnCeremonyTTL   = 5 * time.Minute
	maxWebAuthnCeremonies = 256
)

// WebAuthnRelyingParty identifies one request's installation origin.
type WebAuthnRelyingParty struct {
	ID          string
	Origin      string
	DisplayName string
}

// WebAuthnRegistration starts one authenticated Credential registration.
type WebAuthnRegistration struct {
	CeremonyID string                       `json:"ceremony_id"`
	Options    *protocol.CredentialCreation `json:"options"`
}

// WebAuthnSignIn starts one Account authentication assertion.
type WebAuthnSignIn struct {
	CeremonyID string                        `json:"ceremony_id"`
	Options    *protocol.CredentialAssertion `json:"options"`
}

// WebAuthnCredential is safe Account-visible Credential metadata.
type WebAuthnCredential struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Attachment string     `json:"attachment,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Revoked    bool       `json:"revoked"`
}

type webAuthnCeremony struct {
	kind      string
	rp        WebAuthnRelyingParty
	account   store.WebAuthnAccount
	session   webauthn.SessionData
	expiresAt time.Time
}

type webAuthnUser struct {
	account     store.AccountCredential
	credentials []webauthn.Credential
}

func (user webAuthnUser) WebAuthnID() []byte {
	return slices.Clone(user.account.WebAuthnUserHandle)
}

func (user webAuthnUser) WebAuthnName() string {
	return user.account.Handle
}

func (user webAuthnUser) WebAuthnDisplayName() string {
	return user.account.Name
}

func (user webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return slices.Clone(user.credentials)
}

// BeginWebAuthnRegistration starts one Credential registration for an Account.
func (service *Service) BeginWebAuthnRegistration(
	ctx context.Context,
	actor Account,
	rp WebAuthnRelyingParty,
) (WebAuthnRegistration, error) {
	if service.storageDegraded() {
		return WebAuthnRegistration{}, ErrStorageDegraded
	}
	verifier, err := newWebAuthn(rp)
	if err != nil {
		return WebAuthnRegistration{}, ErrInvalidAccountDetails
	}
	found, err := service.storage.WebAuthnAccountByID(actor.Context(ctx), actor.ID)
	if errors.Is(err, store.ErrInvalidSession) {
		return WebAuthnRegistration{}, ErrInvalidSession
	}
	if err != nil {
		return WebAuthnRegistration{}, err
	}
	if len(found.Account.WebAuthnUserHandle) == 0 {
		return WebAuthnRegistration{}, ErrInvalidSession
	}
	user, err := storedWebAuthnUser(found)
	if err != nil {
		return WebAuthnRegistration{}, err
	}
	options, session, err := verifier.BeginRegistration(user)
	if err != nil {
		return WebAuthnRegistration{}, errors.New("begin WebAuthn registration")
	}
	ceremonyID, err := service.newToken()
	if err != nil {
		return WebAuthnRegistration{}, err
	}
	service.rememberWebAuthnCeremony(ceremonyID, webAuthnCeremony{
		kind: "registration", rp: rp, account: found, session: *session,
		expiresAt: service.now().UTC().Add(webAuthnCeremonyTTL),
	})
	return WebAuthnRegistration{CeremonyID: ceremonyID, Options: options}, nil
}

// FinishWebAuthnRegistration verifies and persists one new Credential.
func (service *Service) FinishWebAuthnRegistration(
	ctx context.Context,
	actor Account,
	rp WebAuthnRelyingParty,
	ceremonyID string,
	name string,
	response []byte,
) (WebAuthnCredential, error) {
	name, err := normalizeDisplayName(name)
	if err != nil {
		return WebAuthnCredential{}, ErrInvalidAccountDetails
	}
	if validateErr := command.ValidateID(ceremonyID); validateErr != nil {
		return WebAuthnCredential{}, ErrAuthenticationFailed
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		service.takeWebAuthnCeremony(ceremonyID, "registration", rp)
		return WebAuthnCredential{}, ErrAuthenticationFailed
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      ceremonyID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(actor.ID),
			name,
			base64.RawURLEncoding.EncodeToString(parsed.RawID),
		),
		Action:     "RegisterWebAuthnCredential",
		TargetType: "Credential",
		TargetID:   "unidentified",
		Now:        service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[WebAuthnCredential]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (WebAuthnCredential, error) {
			var original WebAuthnCredential
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return WebAuthnCredential{}, restoreRejected(decodeErr)
			}
			return original, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[WebAuthnCredential], error) {
			ceremony, ok := service.takeWebAuthnCeremony(ceremonyID, "registration", rp)
			if !ok || ceremony.account.Account.ID != actor.ID {
				return command.Execution[WebAuthnCredential]{}, ErrAuthenticationFailed
			}
			verifier, verifierErr := newWebAuthn(rp)
			if verifierErr != nil {
				return command.Execution[WebAuthnCredential]{}, ErrAuthenticationFailed
			}
			user, userErr := storedWebAuthnUser(ceremony.account)
			if userErr != nil {
				return command.Execution[WebAuthnCredential]{}, userErr
			}
			credential, verifyErr := verifier.CreateCredential(user, ceremony.session, parsed)
			if verifyErr != nil {
				return command.Execution[WebAuthnCredential]{}, ErrAuthenticationFailed
			}
			encoded, encodeErr := json.Marshal(credential)
			if encodeErr != nil {
				return command.Execution[WebAuthnCredential]{},
					errors.New("encode WebAuthn Credential")
			}
			created, createErr := transaction.AddWebAuthnCredential(
				actor.Context(ctx),
				actor.ID,
				credential.ID,
				name,
				encoded,
				string(credential.Authenticator.Attachment),
				identity.Now,
			)
			if errors.Is(createErr, ErrAccountExists) {
				return accountRejectionExecution[WebAuthnCredential](createErr), nil
			}
			if createErr != nil {
				return command.Execution[WebAuthnCredential]{}, createErr
			}
			visible := visibleWebAuthnCredential(created)
			outcome, encodeErr := json.Marshal(visible)
			if encodeErr != nil {
				return command.Execution[WebAuthnCredential]{},
					errors.New("encode WebAuthn Credential registration outcome")
			}
			return command.Success(visible, string(outcome)).
				WithTargetID(strconv.Itoa(created.ID)), nil
		},
	})
}

// BeginWebAuthnSignIn starts a known-Account assertion.
func (service *Service) BeginWebAuthnSignIn(
	ctx context.Context,
	handle string,
	rp WebAuthnRelyingParty,
) (WebAuthnSignIn, error) {
	if service.storageDegraded() {
		return WebAuthnSignIn{}, ErrStorageDegraded
	}
	normalizedName, _, err := normalizeAccountName(handle)
	if err != nil {
		return WebAuthnSignIn{}, ErrAuthenticationFailed
	}
	verifier, err := newWebAuthn(rp)
	if err != nil {
		return WebAuthnSignIn{}, ErrAuthenticationFailed
	}
	found, err := service.storage.WebAuthnAccountByName(ctx, normalizedName)
	if errors.Is(err, store.ErrInvalidSession) {
		return WebAuthnSignIn{}, ErrAuthenticationFailed
	}
	if err != nil {
		return WebAuthnSignIn{}, err
	}
	if len(found.Credentials) == 0 || len(found.Account.WebAuthnUserHandle) == 0 {
		return WebAuthnSignIn{}, ErrAuthenticationFailed
	}
	user, err := storedWebAuthnUser(found)
	if err != nil {
		return WebAuthnSignIn{}, err
	}
	options, session, err := verifier.BeginLogin(user)
	if err != nil {
		return WebAuthnSignIn{}, errors.New("begin WebAuthn sign-in")
	}
	ceremonyID, err := service.newToken()
	if err != nil {
		return WebAuthnSignIn{}, err
	}
	service.rememberWebAuthnCeremony(ceremonyID, webAuthnCeremony{
		kind: "sign-in", rp: rp, account: found, session: *session,
		expiresAt: service.now().UTC().Add(webAuthnCeremonyTTL),
	})
	return WebAuthnSignIn{CeremonyID: ceremonyID, Options: options}, nil
}

// FinishWebAuthnSignIn verifies an assertion and creates the ordinary Account session.
func (service *Service) FinishWebAuthnSignIn(
	ctx context.Context,
	rp WebAuthnRelyingParty,
	ceremonyID string,
	response []byte,
) (Session, error) {
	ceremony, ok := service.takeWebAuthnCeremony(ceremonyID, "sign-in", rp)
	if !ok {
		return Session{}, ErrAuthenticationFailed
	}
	verifier, err := newWebAuthn(rp)
	if err != nil {
		return Session{}, ErrAuthenticationFailed
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return Session{}, ErrAuthenticationFailed
	}
	user, err := storedWebAuthnUser(ceremony.account)
	if err != nil {
		return Session{}, err
	}
	credential, err := verifier.ValidateLogin(user, ceremony.session, parsed)
	if err != nil {
		return Session{}, ErrAuthenticationFailed
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return Session{}, errors.New("encode verified WebAuthn Credential")
	}
	sessionToken, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	found, revoked, err := service.storage.CreateWebAuthnSession(
		ctx,
		ceremony.account.Account.ID,
		credential.ID,
		encoded,
		tokenDigest(sessionToken),
		now,
		expiresAt,
	)
	if errors.Is(err, store.ErrInvalidSession) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	service.pruneSessionCache(now, revoked)
	session := newSession(sessionToken, expiresAt, found)
	service.rememberSession(sessionToken, session.Account, expiresAt)
	return session, nil
}

// WebAuthnCredentials lists Account-visible Credential metadata.
func (service *Service) WebAuthnCredentials(
	ctx context.Context,
	actor Account,
) ([]WebAuthnCredential, error) {
	found, err := service.storage.ListWebAuthnCredentials(actor.Context(ctx), actor.ID)
	if err != nil {
		return nil, err
	}
	result := make([]WebAuthnCredential, 0, len(found))
	for _, credential := range found {
		result = append(result, visibleWebAuthnCredential(credential))
	}
	return result, nil
}

// PasswordActive reports whether an Account has an active password Credential.
func (service *Service) PasswordActive(ctx context.Context, actor Account) (bool, error) {
	return service.storage.PasswordActive(actor.Context(ctx), actor.ID)
}

// RemovePassword removes an Account password only when another Credential remains.
func (service *Service) RemovePassword(
	ctx context.Context,
	actor Account,
	commandID string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil {
		return ErrInvalidAccountDetails
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      commandID,
		PayloadHash:    command.PayloadHash(strconv.Itoa(actor.ID)),
		Action:         "RemovePasswordCredential",
		TargetType:     "Credential",
		TargetID:       "unidentified",
		Now:            service.now().UTC(),
	}
	_, err := command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var original struct{}
			decodeErr := store.DecodeCommandReceipt(outcome, &original)
			return original, restoreRejected(decodeErr)
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			credentialID, removeErr := transaction.RemovePassword(
				actor.Context(ctx),
				actor.ID,
				identity.Now,
			)
			if errors.Is(removeErr, ErrFinalCredential) {
				rejection := accountRejectionExecution[struct{}](removeErr)
				if credentialID > 0 {
					rejection = rejection.WithTargetID(strconv.Itoa(credentialID))
				}
				return rejection, nil
			}
			if removeErr != nil {
				return command.Execution[struct{}]{}, removeErr
			}
			return command.Success(struct{}{}, "{}").
				WithTargetID(strconv.Itoa(credentialID)), nil
		},
	})
	return err
}

// RevokeWebAuthnCredential revokes one Account-owned Credential.
func (service *Service) RevokeWebAuthnCredential(
	ctx context.Context,
	actor Account,
	credentialID int,
	commandID string,
) error {
	if service.storageDegraded() {
		return ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil || credentialID <= 0 {
		return ErrInvalidAccountDetails
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      commandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(actor.ID),
			strconv.Itoa(credentialID),
		),
		Action:     "RevokeWebAuthnCredential",
		TargetType: "Credential",
		TargetID:   strconv.Itoa(credentialID),
		Now:        service.now().UTC(),
	}
	_, err := command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (struct{}, error) {
			var original struct{}
			decodeErr := store.DecodeCommandReceipt(outcome, &original)
			return original, restoreRejected(decodeErr)
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			revokeErr := transaction.RevokeWebAuthnCredential(
				actor.Context(ctx),
				actor.ID,
				credentialID,
				identity.Now,
			)
			switch {
			case errors.Is(revokeErr, ErrFinalCredential),
				errors.Is(revokeErr, store.ErrInvalidSession):
				return accountRejectionExecution[struct{}](revokeErr), nil
			case revokeErr != nil:
				return command.Execution[struct{}]{}, revokeErr
			default:
				return command.Success(struct{}{}, "{}"), nil
			}
		},
	})
	return err
}

func newWebAuthn(rp WebAuthnRelyingParty) (*webauthn.WebAuthn, error) {
	origin, err := url.Parse(rp.Origin)
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" ||
		origin.Fragment != "" || origin.Hostname() != rp.ID || rp.DisplayName == "" ||
		net.ParseIP(rp.ID) != nil {
		return nil, errors.New("invalid WebAuthn Relying Party")
	}
	if origin.Scheme != "https" &&
		(origin.Scheme != "http" || !strings.EqualFold(origin.Hostname(), "localhost")) {
		return nil, errors.New("WebAuthn Relying Party requires a secure origin")
	}
	return webauthn.New(&webauthn.Config{
		RPID:                  rp.ID,
		RPDisplayName:         rp.DisplayName,
		RPOrigins:             []string{rp.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationPreferred,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Registration: webauthn.TimeoutConfig{
				Enforce: true, Timeout: webAuthnCeremonyTTL, TimeoutUVD: webAuthnCeremonyTTL,
			},
			Login: webauthn.TimeoutConfig{
				Enforce: true, Timeout: webAuthnCeremonyTTL, TimeoutUVD: webAuthnCeremonyTTL,
			},
		},
	})
}

func storedWebAuthnUser(found store.WebAuthnAccount) (webAuthnUser, error) {
	user := webAuthnUser{
		account: found.Account, credentials: make([]webauthn.Credential, 0, len(found.Credentials)),
	}
	for _, stored := range found.Credentials {
		var credential webauthn.Credential
		if err := json.Unmarshal(stored.Credential, &credential); err != nil ||
			!bytes.Equal(credential.ID, stored.CredentialID) {
			return webAuthnUser{}, errors.New("decode stored WebAuthn Credential")
		}
		user.credentials = append(user.credentials, credential)
	}
	return user, nil
}

func (service *Service) rememberWebAuthnCeremony(id string, ceremony webAuthnCeremony) {
	now := service.now().UTC()
	service.webAuthnMu.Lock()
	defer service.webAuthnMu.Unlock()
	for existingID, existing := range service.webAuthnCeremonies {
		if !existing.expiresAt.After(now) {
			delete(service.webAuthnCeremonies, existingID)
		}
	}
	if len(service.webAuthnCeremonies) >= maxWebAuthnCeremonies {
		var oldestID string
		var oldestExpiry time.Time
		for existingID, existing := range service.webAuthnCeremonies {
			if oldestID == "" || existing.expiresAt.Before(oldestExpiry) {
				oldestID, oldestExpiry = existingID, existing.expiresAt
			}
		}
		delete(service.webAuthnCeremonies, oldestID)
	}
	service.webAuthnCeremonies[tokenDigest(id)] = ceremony
}

func (service *Service) takeWebAuthnCeremony(
	id string,
	kind string,
	rp WebAuthnRelyingParty,
) (webAuthnCeremony, bool) {
	if !validToken(id) {
		return webAuthnCeremony{}, false
	}
	service.webAuthnMu.Lock()
	defer service.webAuthnMu.Unlock()
	key := tokenDigest(id)
	ceremony, ok := service.webAuthnCeremonies[key]
	delete(service.webAuthnCeremonies, key)
	if !ok || !ceremony.expiresAt.After(service.now().UTC()) ||
		ceremony.kind != kind || ceremony.rp != rp {
		return webAuthnCeremony{}, false
	}
	return ceremony, true
}

func visibleWebAuthnCredential(found store.WebAuthnCredential) WebAuthnCredential {
	return WebAuthnCredential{
		ID: found.ID, Name: found.Name, Attachment: found.Attachment,
		CreatedAt: found.CreatedAt, LastUsedAt: found.LastUsedAt, Revoked: found.RevokedAt != nil,
	}
}

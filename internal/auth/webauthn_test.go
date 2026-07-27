package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"

	virtualwebauthn "github.com/descope/virtualwebauthn"
)

func TestWebAuthnCeremoniesHaveABoundedMemory(t *testing.T) {
	service, account := openAccountTestService(t)
	service.random = rand.Reader
	rp := WebAuthnRelyingParty{
		ID: "beamers.test", Origin: "https://beamers.test", DisplayName: "Beamers",
	}

	for range maxWebAuthnCeremonies + 1 {
		if _, err := service.BeginWebAuthnRegistration(t.Context(), account, rp); err != nil {
			t.Fatalf("fill WebAuthn ceremony limit: %v", err)
		}
	}
	if len(service.webAuthnCeremonies) != maxWebAuthnCeremonies {
		t.Fatalf(
			"WebAuthn ceremonies = %d, want %d",
			len(service.webAuthnCeremonies),
			maxWebAuthnCeremonies,
		)
	}
}

func TestWebAuthnCredentialLifecycleSurvivesRestart(t *testing.T) {
	service, account := openAccountTestService(t)
	service.random = new(incrementingRandomReader)
	rp := WebAuthnRelyingParty{
		ID: "beamers.test", Origin: "https://beamers.test", DisplayName: "Beamers",
	}
	authenticator := virtualwebauthn.NewAuthenticator()
	credential := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	registration, err := service.BeginWebAuthnRegistration(t.Context(), account, rp)
	if err != nil {
		t.Fatalf("begin WebAuthn registration: %v", err)
	}
	optionsJSON, err := json.Marshal(registration.Options)
	if err != nil {
		t.Fatalf("encode WebAuthn registration options: %v", err)
	}
	options, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("parse WebAuthn registration options: %v", err)
	}
	response := virtualwebauthn.CreateAttestationResponse(
		virtualwebauthn.RelyingParty{
			ID: rp.ID, Origin: rp.Origin, Name: rp.DisplayName,
		},
		authenticator,
		credential,
		*options,
	)
	registered, err := service.FinishWebAuthnRegistration(
		t.Context(),
		account,
		rp,
		registration.CeremonyID,
		"Laptop passkey",
		[]byte(response),
	)
	if err != nil {
		t.Fatalf("finish WebAuthn registration: %v", err)
	}
	if registered.Name != "Laptop passkey" || registered.Revoked {
		t.Fatalf("registered WebAuthn Credential = %+v", registered)
	}
	var formatted bytes.Buffer
	if err = json.Indent(&formatted, []byte(response), "", "  "); err != nil {
		t.Fatalf("reformat WebAuthn registration response: %v", err)
	}
	replayed, err := service.FinishWebAuthnRegistration(
		t.Context(),
		account,
		rp,
		registration.CeremonyID,
		"Laptop passkey",
		formatted.Bytes(),
	)
	if err != nil || replayed.ID != registered.ID {
		t.Fatalf("retry WebAuthn registration = %+v, %v", replayed, err)
	}
	authenticator.AddCredential(credential)

	if err = service.RemovePassword(t.Context(), account, "remove-password"); err != nil {
		t.Fatalf("remove password after WebAuthn registration: %v", err)
	}
	if err = service.RemovePassword(t.Context(), account, "remove-password"); err != nil {
		t.Fatalf("retry password removal: %v", err)
	}
	if _, signInErr := service.SignIn(
		t.Context(),
		account.Handle,
		"administrator password",
	); !errors.Is(signInErr, ErrAuthenticationFailed) {
		t.Fatalf("removed password sign-in error = %v", signInErr)
	}

	restarted, err := New(service.storage, Config{
		Now:              service.now,
		Random:           new(incrementingRandomReader),
		BootstrapTTL:     service.bootstrapTTL,
		RecoveryTokenTTL: service.recoveryTokenTTL,
		SessionTTL:       service.sessionTTL,
	})
	if err != nil {
		t.Fatalf("restart authentication service: %v", err)
	}
	signIn, err := restarted.BeginWebAuthnSignIn(t.Context(), account.Handle, rp)
	if err != nil {
		t.Fatalf("begin WebAuthn sign-in after restart: %v", err)
	}
	assertionJSON, err := json.Marshal(signIn.Options)
	if err != nil {
		t.Fatalf("encode WebAuthn sign-in options: %v", err)
	}
	assertion, err := virtualwebauthn.ParseAssertionOptions(string(assertionJSON))
	if err != nil {
		t.Fatalf("parse WebAuthn sign-in options: %v", err)
	}
	assertionResponse := virtualwebauthn.CreateAssertionResponse(
		virtualwebauthn.RelyingParty{
			ID: rp.ID, Origin: rp.Origin, Name: rp.DisplayName,
		},
		authenticator,
		credential,
		*assertion,
	)
	session, err := restarted.FinishWebAuthnSignIn(
		t.Context(),
		rp,
		signIn.CeremonyID,
		[]byte(assertionResponse),
	)
	if err != nil {
		t.Fatalf("finish WebAuthn sign-in after restart: %v", err)
	}
	if session.Account.ID != account.ID || session.Token == "" {
		t.Fatalf("WebAuthn Account session = %+v", session)
	}
	if authenticated, authErr := restarted.Authenticate(
		t.Context(),
		session.Token,
	); authErr != nil || authenticated.ID != account.ID {
		t.Fatalf("authenticate WebAuthn session = %+v, %v", authenticated, authErr)
	}

	if err = restarted.RevokeWebAuthnCredential(
		t.Context(),
		account,
		registered.ID,
		"revoke-final-webauthn",
	); !errors.Is(err, ErrFinalCredential) {
		t.Fatalf("revoke final WebAuthn Credential error = %v, want %v", err, ErrFinalCredential)
	}
	audits, err := restarted.ListAuditEntries(t.Context(), account)
	if err != nil {
		t.Fatalf("list WebAuthn Audit Entries: %v", err)
	}
	found := map[string]AuditEntry{}
	for _, entry := range audits {
		if entry.Action == "RegisterWebAuthnCredential" ||
			entry.Action == "RemovePasswordCredential" ||
			entry.Action == "RevokeWebAuthnCredential" {
			found[entry.Action] = entry
		}
	}
	for _, action := range []string{
		"RegisterWebAuthnCredential",
		"RemovePasswordCredential",
		"RevokeWebAuthnCredential",
	} {
		entry, ok := found[action]
		if !ok || entry.TargetType != "Credential" ||
			entry.TargetID == "" || entry.TargetID == "0" || entry.TargetID == "unidentified" {
			t.Errorf("%s Audit Entry = %+v", action, entry)
		}
	}
}

func TestWebAuthnRegistrationFailsClosedForInvalidCeremonies(t *testing.T) {
	service, account := openAccountTestService(t)
	service.random = new(incrementingRandomReader)
	rp := WebAuthnRelyingParty{
		ID: "beamers.test", Origin: "https://beamers.test", DisplayName: "Beamers",
	}
	authenticator := virtualwebauthn.NewAuthenticator()

	for _, test := range []struct {
		name     string
		response func(virtualwebauthn.AttestationOptions) string
	}{
		{
			name: "origin",
			response: func(options virtualwebauthn.AttestationOptions) string {
				return virtualwebauthn.CreateAttestationResponse(
					virtualwebauthn.RelyingParty{
						ID: rp.ID, Origin: "https://attacker.test", Name: rp.DisplayName,
					},
					authenticator,
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
		{
			name: "challenge",
			response: func(options virtualwebauthn.AttestationOptions) string {
				options.Challenge = []byte("wrong challenge")
				return virtualwebauthn.CreateAttestationResponse(
					virtualwebauthn.RelyingParty{
						ID: rp.ID, Origin: rp.Origin, Name: rp.DisplayName,
					},
					authenticator,
					virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
					options,
				)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registration, err := service.BeginWebAuthnRegistration(t.Context(), account, rp)
			if err != nil {
				t.Fatalf("begin WebAuthn registration: %v", err)
			}
			optionsJSON, err := json.Marshal(registration.Options)
			if err != nil {
				t.Fatalf("encode WebAuthn registration options: %v", err)
			}
			options, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
			if err != nil {
				t.Fatalf("parse WebAuthn registration options: %v", err)
			}
			_, err = service.FinishWebAuthnRegistration(
				t.Context(),
				account,
				rp,
				registration.CeremonyID,
				"Invalid authenticator",
				[]byte(test.response(*options)),
			)
			if !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("invalid %s error = %v, want %v", test.name, err, ErrAuthenticationFailed)
			}
		})
	}
}

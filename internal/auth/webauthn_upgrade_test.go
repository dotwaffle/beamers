package auth

import (
	"encoding/json"
	"os"
	"testing"

	virtualwebauthn "github.com/descope/virtualwebauthn"
)

// The fixture contains a credential created by go-webauthn v0.17.4 through
// BeginRegistration and CreateCredential, plus its test-only signing key.
// Keep its original JSON so this checks compatibility across upgrades.
func TestWebAuthnSignsInWithVersion017Credential(t *testing.T) {
	data, err := os.ReadFile("testdata/webauthn-v0.17.4.json")
	if err != nil {
		t.Fatalf("read old credential fixture: %v", err)
	}
	var fixture struct {
		Credential json.RawMessage            `json:"credential"`
		Signing    virtualwebauthn.Credential `json:"signing"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode old credential fixture: %v", err)
	}
	service, account := openAccountTestService(t)
	ctx := account.Context(t.Context())
	transaction, err := service.storage.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin credential import: %v", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err = transaction.AddWebAuthnCredential(
		ctx, account.ID, fixture.Signing.ID, "Existing passkey", fixture.Credential, "", service.now(),
	); err != nil {
		t.Fatalf("import old credential: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit old credential: %v", err)
	}
	rp := WebAuthnRelyingParty{
		ID: "beamers.test", Origin: "https://beamers.test", DisplayName: "Beamers",
	}
	signIn, err := service.BeginWebAuthnSignIn(t.Context(), account.Handle, rp)
	if err != nil {
		t.Fatalf("begin sign-in with old credential: %v", err)
	}
	optionsJSON, err := json.Marshal(signIn.Options)
	if err != nil {
		t.Fatalf("encode assertion options: %v", err)
	}
	options, err := virtualwebauthn.ParseAssertionOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("parse assertion options: %v", err)
	}
	authenticator := virtualwebauthn.NewAuthenticator()
	authenticator.AddCredential(fixture.Signing)
	response := virtualwebauthn.CreateAssertionResponse(
		virtualwebauthn.RelyingParty{ID: rp.ID, Origin: rp.Origin, Name: rp.DisplayName},
		authenticator, fixture.Signing, *options,
	)
	session, err := service.FinishWebAuthnSignIn(t.Context(), rp, signIn.CeremonyID, []byte(response))
	if err != nil {
		t.Fatalf("finish sign-in with old credential: %v", err)
	}
	if session.Account.ID != account.ID || session.Token == "" {
		t.Fatalf("old credential session = %+v", session)
	}
}

package auth

import (
	"errors"
	"testing"
)

func TestFederatedIdentityLinksAndCreatesOnlyDistinctAccounts(t *testing.T) {
	service, administrator := openAccountTestService(t)

	if _, err := service.LinkFederatedIdentity(
		t.Context(),
		Account{},
		"sceneid",
		"42",
		"link-without-authentication",
	); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unauthenticated link error = %v, want %v", err, ErrInvalidSession)
	}
	linked, err := service.LinkFederatedIdentity(
		t.Context(),
		administrator,
		"sceneid",
		"42",
		"link-administrator-sceneid",
	)
	if err != nil {
		t.Fatalf("link SceneID: %v", err)
	}
	if linked.Provider != "sceneid" || linked.Subject != "42" || linked.Revoked {
		t.Fatalf("linked Federated Identity = %+v", linked)
	}

	session, created, err := service.FederatedSignIn(
		t.Context(),
		"sceneid",
		"42",
		"Changed Display Name",
		true,
	)
	if err != nil || created || session.Account.ID != administrator.ID ||
		session.Account.Name != administrator.Name {
		t.Fatalf("linked federated sign-in = %+v, created %t, %v", session, created, err)
	}

	first, created, err := service.FederatedSignIn(
		t.Context(),
		"conference",
		"subject-one",
		"Same Mutable Profile",
		true,
	)
	if err != nil || !created {
		t.Fatalf("first federated registration = %+v, created %t, %v", first, created, err)
	}
	second, created, err := service.FederatedSignIn(
		t.Context(),
		"conference",
		"subject-two",
		"Same Mutable Profile",
		true,
	)
	if err != nil || !created {
		t.Fatalf("second federated registration = %+v, created %t, %v", second, created, err)
	}
	if first.Account.ID == second.Account.ID ||
		first.Account.Handle == second.Account.Handle {
		t.Fatalf("distinct subjects merged Accounts: %+v and %+v", first.Account, second.Account)
	}

	if _, _, err = service.FederatedSignIn(
		t.Context(),
		"conference",
		"creation-disabled",
		"Ignored",
		false,
	); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("provider-disabled creation error = %v, want %v", err, ErrRegistrationClosed)
	}
	if err = service.SetRegistrationOpen(t.Context(), administrator, false); err != nil {
		t.Fatalf("close Registration Policy: %v", err)
	}
	if _, created, err = service.FederatedSignIn(
		t.Context(),
		"conference",
		"subject-one",
		"Later Changed Name",
		false,
	); err != nil || created {
		t.Fatalf("existing identity while closed = created %t, %v", created, err)
	}
	if _, _, err = service.FederatedSignIn(
		t.Context(),
		"conference",
		"registration-closed",
		"Ignored",
		true,
	); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("closed registration error = %v, want %v", err, ErrRegistrationClosed)
	}
	if err = service.DisableAccount(
		t.Context(),
		administrator,
		first.Account.ID,
		"disable-federated-account",
		"access_revoked",
	); err != nil {
		t.Fatalf("disable federated Account: %v", err)
	}
	if _, _, err = service.FederatedSignIn(
		t.Context(),
		"conference",
		"subject-one",
		"Ignored",
		true,
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("disabled federated Account error = %v, want %v", err, ErrAuthenticationFailed)
	}
}

func TestFederatedIdentityCanReplacePassword(t *testing.T) {
	service, administrator := openAccountTestService(t)
	linked, err := service.LinkFederatedIdentity(
		t.Context(),
		administrator,
		"sceneid",
		"84",
		"link-final-sceneid",
	)
	if err != nil {
		t.Fatalf("link SceneID: %v", err)
	}
	identities, err := service.ListFederatedIdentities(t.Context(), administrator)
	if err != nil || len(identities) != 1 || identities[0] != linked {
		t.Fatalf("Federated Identities = %+v, %v", identities, err)
	}
	if err = service.RemovePassword(
		t.Context(),
		administrator,
		"remove-password-after-sceneid",
	); err != nil {
		t.Fatalf("remove password after SceneID link: %v", err)
	}
	session, created, err := service.FederatedSignIn(
		t.Context(),
		"sceneid",
		"84",
		"Ignored",
		false,
	)
	if err != nil || created || session.Account.ID != administrator.ID {
		t.Fatalf("SceneID after password removal = %+v, created %t, %v", session, created, err)
	}
}

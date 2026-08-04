package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestAdministratorNeedsResultsEventGrant(t *testing.T) {
	administrator := Account{Administrator: true}
	if administrator.HasCapability(1, viewer.ViewResults) {
		t.Fatal("Administrator received implicit Results Access")
	}
	administrator.EventRoles = map[int]viewer.Role{1: viewer.Producer}
	if !administrator.HasCapability(1, viewer.ViewResults) ||
		!administrator.HasCapability(1, viewer.ManageResults) {
		t.Fatal("Producer grant did not supply Results Access")
	}
}

func TestPasswordWorkAdmissionEnforcesMemoryBudget(t *testing.T) {
	service := &Service{passwordWork: make(chan struct{}, passwordConcurrency)}
	if !service.beginPasswordWork() {
		t.Fatal("first password operation was not admitted")
	}
	if !service.beginPasswordWork() {
		t.Fatal("second password operation was not admitted")
	}
	if service.beginPasswordWork() {
		t.Fatal("third password operation exceeded the 128 MiB KDF memory budget")
	}

	service.endPasswordWork()
	if !service.beginPasswordWork() {
		t.Fatal("released password capacity was not reusable")
	}
}

func TestBootstrapFirstAccountRejectsInvalidIdentityFields(t *testing.T) {
	service, _ := openAccountTestService(t)
	token := base64.RawURLEncoding.EncodeToString(make([]byte, tokenBytes))
	for _, testCase := range []struct {
		name        string
		handle      string
		displayName string
	}{
		{name: "handle", handle: "", displayName: "Ada Lovelace"},
		{name: "display name", handle: "ada", displayName: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.BootstrapFirstAccount(
				t.Context(),
				token,
				testCase.handle,
				testCase.displayName,
				"correct horse battery staple",
			)
			if !errors.Is(err, ErrInvalidAccountDetails) {
				t.Fatalf("BootstrapFirstAccount error = %v, want %v", err, ErrInvalidAccountDetails)
			}
		})
	}
}

func TestDemoPasswordRequiresExplicitConfiguration(t *testing.T) {
	service, administrator := openAccountTestService(t)
	_, err := service.CreateAccount(
		t.Context(),
		administrator,
		"demo-user",
		"demo",
		"create-demo-user",
	)
	if !errors.Is(err, ErrInvalidAccountDetails) {
		t.Fatalf("default demo password error = %v, want %v", err, ErrInvalidAccountDetails)
	}
}

func TestAdministratorAccountCreationRequiresDisplayName(t *testing.T) {
	service, administrator := openAccountTestService(t)
	_, err := service.CreateAccountWithDisplayName(
		t.Context(),
		administrator,
		"pat",
		"",
		"participant correct horse battery staple",
		"create-account-without-display-name",
	)
	if !errors.Is(err, ErrInvalidAccountDetails) {
		t.Fatalf("Create Account error = %v, want %v", err, ErrInvalidAccountDetails)
	}
	_, err = service.CreateAccountWithDisplayName(
		t.Context(),
		administrator,
		"pat",
		"pat",
		"participant correct horse battery staple",
		"create-account-without-display-name",
	)
	if !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("corrected Create Account error = %v, want %v", err, ErrCommandConflict)
	}
}

func TestOpenRegistrationSeparatesHandleFromDisplayName(t *testing.T) {
	service, administrator := openAccountTestService(t)
	service.random = rand.Reader

	if open, err := service.RegistrationOpen(t.Context()); err != nil || !open {
		t.Fatalf("default Registration Policy = %t, %v; want open", open, err)
	}
	registered, err := service.Register(
		t.Context(),
		"pat",
		"Pat Participant",
		"participant correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("register Account: %v", err)
	}
	if registered.Handle != "pat" || registered.Name != "Pat Participant" {
		t.Fatalf("registered Account = %+v", registered)
	}
	if _, err = service.Register(
		t.Context(),
		"PAT",
		"Someone Else",
		"different correct horse battery staple",
	); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("case-insensitive duplicate error = %v, want %v", err, ErrAccountExists)
	}
	if _, err = service.Register(
		t.Context(),
		"pat-two",
		"Pat Participant",
		"another correct horse battery staple",
	); err != nil {
		t.Fatalf("register duplicate Display Name: %v", err)
	}
	session, err := service.SignIn(
		t.Context(),
		"PAT",
		"participant correct horse battery staple",
	)
	if err != nil || session.Account.ID != registered.ID {
		t.Fatalf("case-insensitive sign-in = %+v, %v", session.Account, err)
	}

	if err = service.SetRegistrationOpen(t.Context(), registered, false); !errors.Is(
		err,
		ErrAdministratorRequired,
	) {
		t.Fatalf("participant policy update error = %v, want %v", err, ErrAdministratorRequired)
	}
	if err = service.SetRegistrationOpen(t.Context(), administrator, false); err != nil {
		t.Fatalf("close registration: %v", err)
	}
	if _, err = service.Register(
		t.Context(),
		"closed",
		"Closed Registration",
		"closed correct horse battery staple",
	); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("closed registration error = %v, want %v", err, ErrRegistrationClosed)
	}
	if _, err = service.SignIn(
		t.Context(),
		"pat",
		"participant correct horse battery staple",
	); err != nil {
		t.Fatalf("existing sign-in while registration closed: %v", err)
	}
}

func TestPublicProfileIsPrivateByDefaultAndDetachedOnDisable(t *testing.T) {
	service, administrator := openAccountTestService(t)
	service.random = rand.Reader
	registered, err := service.Register(
		t.Context(),
		"profile-owner",
		"Private Person",
		"profile correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("register profile owner: %v", err)
	}
	if _, found, profileErr := service.PublicProfile(
		t.Context(),
		"PROFILE-OWNER",
	); profileErr != nil || found {
		t.Fatalf("private Public Profile = found %t, %v", found, profileErr)
	}
	session, err := service.SignIn(
		t.Context(),
		"profile-owner",
		"profile correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("sign in profile owner: %v", err)
	}
	authenticated, err := service.Authenticate(t.Context(), session.Token)
	if err != nil || authenticated.Handle != "profile-owner" {
		t.Fatalf("authenticated Profile owner = %+v, %v", authenticated, err)
	}
	if err = service.UpdateProfile(
		t.Context(),
		authenticated,
		"Shared Display Name",
		true,
		nil,
		"publish-profile-owner",
	); err != nil {
		t.Fatalf("publish Public Profile: %v", err)
	}
	profile, found, err := service.PublicProfile(t.Context(), "profile-owner")
	if err != nil || !found {
		t.Fatalf("published Public Profile = found %t, %v", found, err)
	}
	if profile.Handle != "profile-owner" ||
		profile.DisplayName != "Shared Display Name" ||
		len(profile.Entries) != 0 {
		t.Fatalf("published Public Profile = %+v", profile)
	}
	if _, err = service.Register(
		t.Context(),
		"disabled-account-2",
		"Collision Guard",
		"collision correct horse battery staple",
	); err != nil {
		t.Fatalf("register collision Handle: %v", err)
	}
	if err = service.DisableAccount(
		t.Context(),
		administrator,
		registered.ID,
		"disable-profile-owner",
		"access_revoked",
	); err != nil {
		t.Fatalf("disable profile owner: %v", err)
	}
	if _, found, err = service.PublicProfile(t.Context(), "profile-owner"); err != nil || found {
		t.Fatalf("disabled Public Profile = found %t, %v", found, err)
	}
}

func TestCreateAccountRetryDoesNotRetainPasswordIdentity(t *testing.T) {
	service, administrator := openAccountTestService(t)

	first, err := service.CreateAccount(
		t.Context(),
		administrator,
		"Pat Producer",
		"correct horse battery staple",
		"create-pat",
	)
	if err != nil {
		t.Fatalf("create Account: %v", err)
	}
	retried, err := service.CreateAccount(
		t.Context(),
		administrator,
		" pat producer ",
		"a different valid password",
		"create-pat",
	)
	if err != nil {
		t.Fatalf("retry Account creation: %v", err)
	}
	if !reflect.DeepEqual(retried, first) {
		t.Fatalf("retry Account = %+v, want original %+v", retried, first)
	}

	accounts, err := service.ListAccounts(t.Context(), administrator)
	if err != nil {
		t.Fatalf("list Accounts: %v", err)
	}
	audits, err := service.ListAuditEntries(t.Context(), administrator)
	if err != nil {
		t.Fatalf("list Audit Entries: %v", err)
	}
	if len(accounts) != 2 || len(audits) != 1 {
		t.Fatalf("Accounts/Audit Entries = %d/%d, want 2/1", len(accounts), len(audits))
	}

	for _, password := range []string{"first rejected password", "second rejected password"} {
		if _, err = service.CreateAccount(
			t.Context(),
			administrator,
			"Pat Producer",
			password,
			"reject-existing-pat",
		); !errors.Is(err, ErrAccountExists) {
			t.Fatalf("rejected Account retry error = %v, want %v", err, ErrAccountExists)
		}
	}
	if _, err = service.CreateAccount(
		t.Context(),
		administrator,
		"Different Account",
		"a different valid password",
		"create-pat",
	); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("different Account reuse error = %v, want %v", err, ErrCommandConflict)
	}
	audits, err = service.ListAuditEntries(t.Context(), administrator)
	if err != nil {
		t.Fatalf("list final Audit Entries: %v", err)
	}
	if len(audits) != 3 {
		t.Fatalf("final Audit Entry count = %d, want success, rejection, conflict", len(audits))
	}
}

func TestAuthenticateUsesOnlyUnexpiredPreviouslyValidatedSessionDuringStorageFailure(
	t *testing.T,
) {
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize authentication storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open authentication storage: %v", err)
	}
	service, err := New(storage, Config{
		Now: func() time.Time {
			return now
		},
		Random:           testRandomReader{},
		BootstrapTTL:     time.Hour,
		RecoveryTokenTTL: time.Hour,
		SessionTTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create authentication service: %v", err)
	}
	bootstrap, err := service.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := service.BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Ada Admin",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	validated, err := service.Authenticate(t.Context(), session.Token)
	if err != nil {
		t.Fatalf("validate Account session: %v", err)
	}
	unwarmedState := &testStorageState{}
	unwarmed, err := New(storage, Config{
		Now: func() time.Time {
			return now
		},
		Random:           testRandomReader{},
		BootstrapTTL:     time.Hour,
		RecoveryTokenTTL: time.Hour,
		SessionTTL:       time.Hour,
		StorageState:     unwarmedState,
	})
	if err != nil {
		t.Fatalf("create unwarmed authentication service: %v", err)
	}
	if _, authenticateErr := unwarmed.AuthenticatePreviouslyValidated(
		t.Context(),
		strings.Repeat("A", len(session.Token)),
	); !errors.Is(authenticateErr, ErrInvalidSession) {
		t.Fatalf("unknown session error = %v", authenticateErr)
	}
	if unwarmedState.prepareCalls != 0 {
		t.Fatalf("storage probes for unknown session = %d, want 0", unwarmedState.prepareCalls)
	}
	unwarmedState.prepareErr = errors.New("command evidence unavailable")
	if _, authenticateErr := unwarmed.AuthenticatePreviouslyValidated(
		t.Context(),
		session.Token,
	); !errors.Is(authenticateErr, ErrStorageDegraded) {
		t.Fatalf("uncached session while degraded error = %v", authenticateErr)
	}
	if unwarmedState.prepareCalls != 1 {
		t.Fatalf("storage probes for valid uncached session = %d, want 1", unwarmedState.prepareCalls)
	}

	signInState := &testStorageState{prepareErr: errors.New("command evidence unavailable")}
	service.storageState = signInState
	_, signInErr := service.SignIn(
		t.Context(),
		"Ada Admin",
		"correct horse battery staple",
	)
	if !errors.Is(signInErr, ErrStorageDegraded) {
		t.Fatalf("new sign-in during undetected failure error = %v", signInErr)
	}
	if signInState.prepareCalls != 1 {
		t.Fatalf("storage probes for valid sign-in = %d, want 1", signInState.prepareCalls)
	}

	cachedState := &testStorageState{prepareErr: errors.New("command evidence unavailable")}
	service.storageState = cachedState
	cached, err := service.AuthenticatePreviouslyValidated(t.Context(), session.Token)
	if err != nil || !reflect.DeepEqual(cached, validated) {
		t.Fatalf("authenticate across newly detected failure = %+v, %v", cached, err)
	}
	if cachedState.prepareCalls != 1 {
		t.Fatalf("storage probes for cached session = %d, want 1", cachedState.prepareCalls)
	}

	service.storageState = staticStorageState(true)
	if _, authenticateErr := service.Authenticate(
		t.Context(),
		session.Token,
	); !errors.Is(authenticateErr, ErrStorageDegraded) {
		t.Fatalf("ordinary authentication while degraded error = %v", authenticateErr)
	}
	cached, err = service.AuthenticatePreviouslyValidated(t.Context(), session.Token)
	if err != nil || !reflect.DeepEqual(cached, validated) {
		t.Fatalf("authenticate while degraded = %+v, %v", cached, err)
	}
	_, signInErr = service.SignIn(
		t.Context(),
		"Ada Admin",
		"correct horse battery staple",
	)
	if !errors.Is(signInErr, ErrStorageDegraded) {
		t.Fatalf("new sign-in while degraded error = %v", signInErr)
	}
	service.storageState = nil
	if closeErr := storage.Close(); closeErr != nil {
		t.Fatalf("fail authentication storage: %v", closeErr)
	}

	if _, authenticateErr := service.Authenticate(
		t.Context(),
		session.Token,
	); authenticateErr == nil {
		t.Fatal("ordinary authentication succeeded without storage")
	}
	cached, err = service.AuthenticatePreviouslyValidated(t.Context(), session.Token)
	if err != nil {
		t.Fatalf("authenticate from pre-fault session snapshot: %v", err)
	}
	if !reflect.DeepEqual(cached, validated) {
		t.Fatalf("cached Account = %+v, want %+v", cached, validated)
	}

	now = now.Add(2 * time.Hour)
	if _, authenticateErr := service.AuthenticatePreviouslyValidated(
		t.Context(),
		session.Token,
	); authenticateErr == nil {
		t.Fatal("expired cached Account session authenticated")
	}
	if _, signInErr = service.SignIn(
		t.Context(),
		"Ada Admin",
		"correct horse battery staple",
	); signInErr == nil {
		t.Fatal("new sign-in succeeded without storage")
	}
}

func TestSignInBoundsSessionsAndPrunesExpiryWithoutTokenReuse(t *testing.T) {
	const expectedLimit = 8
	now := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize authentication storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open authentication storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close authentication storage: %v", closeErr)
		}
	})
	service, err := New(storage, Config{
		Now: func() time.Time {
			return now
		},
		Random:           &incrementingRandomReader{},
		BootstrapTTL:     time.Hour,
		RecoveryTokenTTL: time.Hour,
		SessionTTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create authentication service: %v", err)
	}
	bootstrap, err := service.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	oldest, err := service.BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Ada Admin",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	active := []Session{oldest}
	for range expectedLimit {
		session, signInErr := service.SignIn(
			t.Context(),
			"Ada Admin",
			"correct horse battery staple",
		)
		if signInErr != nil {
			t.Fatalf("sign in: %v", signInErr)
		}
		active = append(active, session)
	}
	counts, err := service.SessionCounts(t.Context())
	if err != nil {
		t.Fatalf("count Account sessions: %v", err)
	}
	if counts.Active != expectedLimit ||
		counts.Cached != expectedLimit ||
		counts.Stored != expectedLimit ||
		counts.PerAccountLimit != expectedLimit {
		t.Fatalf("session counts = %+v", counts)
	}
	if _, err = service.Authenticate(t.Context(), active[0].Token); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("oldest session error = %v, want %v", err, ErrInvalidSession)
	}
	for _, current := range active[1:] {
		if _, err = service.Authenticate(t.Context(), current.Token); err != nil {
			t.Fatalf("current session rejected: %v", err)
		}
	}

	now = now.Add(2 * time.Hour)
	replacement, err := service.SignIn(
		t.Context(),
		"Ada Admin",
		"correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("sign in after expiry: %v", err)
	}
	counts, err = service.SessionCounts(t.Context())
	if err != nil {
		t.Fatalf("count pruned Account sessions: %v", err)
	}
	if counts.Active != 1 || counts.Cached != 1 || counts.Stored != 1 {
		t.Fatalf("pruned session counts = %+v, want one", counts)
	}
	if _, err = service.Authenticate(t.Context(), active[len(active)-1].Token); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("expired session error = %v, want %v", err, ErrInvalidSession)
	}
	if _, err = service.Authenticate(t.Context(), replacement.Token); err != nil {
		t.Fatalf("replacement session rejected: %v", err)
	}
}

func TestAccountRecoveryIsSingleUseDurableAndRevokesSessions(t *testing.T) {
	service, administrator := openAccountTestService(t)
	service.random = &incrementingRandomReader{}
	now := time.Date(2026, time.July, 27, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	participant, err := service.Register(
		t.Context(),
		"pat",
		"Pat Participant",
		"participant correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	oldSession, err := service.SignIn(
		t.Context(),
		participant.Handle,
		"participant correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("sign in participant: %v", err)
	}

	oldCodes, err := service.ReplaceRecoveryCodes(
		t.Context(),
		participant,
		"replace-first-recovery-codes",
	)
	if err != nil {
		t.Fatalf("generate first Recovery Codes: %v", err)
	}
	if _, err = service.ReplaceRecoveryCodes(
		t.Context(),
		participant,
		"replace-first-recovery-codes",
	); !errors.Is(err, ErrRecoveryCodesAlreadyReplaced) {
		t.Fatalf(
			"retried Recovery Code replacement error = %v, want %v",
			err,
			ErrRecoveryCodesAlreadyReplaced,
		)
	}
	codes, err := service.ReplaceRecoveryCodes(
		t.Context(),
		participant,
		"replace-recovery-codes-again",
	)
	if err != nil {
		t.Fatalf("replace Recovery Codes: %v", err)
	}
	if len(codes) != recoveryCodeCount || reflect.DeepEqual(oldCodes, codes) {
		t.Fatalf("replacement Recovery Codes = %v", codes)
	}
	if _, err = service.Recover(
		t.Context(),
		participant.Handle,
		oldCodes[0],
		"replacement correct horse battery staple",
		"recover-with-replaced-code",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("replaced Recovery Code error = %v, want %v", err, ErrAuthenticationFailed)
	}

	restarted, err := New(service.storage, Config{
		Now:              service.now,
		Random:           service.random,
		BootstrapTTL:     time.Hour,
		SessionTTL:       time.Hour,
		RecoveryTokenTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("restart authentication service: %v", err)
	}
	recovered, err := restarted.Recover(
		t.Context(),
		participant.Handle,
		codes[0],
		"replacement correct horse battery staple",
		"recover-with-current-code",
	)
	if err != nil {
		t.Fatalf("recover with Account code after restart: %v", err)
	}
	if recovered.Account.ID != participant.ID {
		t.Fatalf("recovered Account = %+v, want %d", recovered.Account, participant.ID)
	}
	if _, err = restarted.Authenticate(t.Context(), oldSession.Token); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("old session error = %v, want %v", err, ErrInvalidSession)
	}
	if _, err = restarted.Recover(
		t.Context(),
		participant.Handle,
		codes[0],
		"replacement correct horse battery staple",
		"recover-with-current-code",
	); !errors.Is(err, ErrRecoveryAlreadyCompleted) {
		t.Fatalf("retried Recovery command error = %v, want %v", err, ErrRecoveryAlreadyCompleted)
	}
	if _, err = restarted.Recover(
		t.Context(),
		participant.Handle,
		codes[0],
		"another replacement horse battery staple",
		"reuse-current-code",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("reused Recovery Code error = %v, want %v", err, ErrAuthenticationFailed)
	}

	token, err := restarted.IssueRecoveryToken(
		t.Context(),
		administrator,
		participant.ID,
		"verified government ID in person",
		"issue-participant-recovery",
	)
	if err != nil {
		t.Fatalf("issue Administrator Recovery Token: %v", err)
	}
	if token.Token == "" || !token.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("Administrator Recovery Token = %+v", token)
	}
	if _, err = restarted.Authenticate(t.Context(), recovered.Token); !errors.Is(
		err,
		ErrInvalidSession,
	) {
		t.Fatalf("session after Administrator recovery error = %v, want %v", err, ErrInvalidSession)
	}
	now = now.Add(15 * time.Minute)
	if _, err = restarted.Recover(
		t.Context(),
		participant.Handle,
		token.Token,
		"expired token horse battery staple",
		"recover-with-expired-token",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("expired Administrator Recovery Token error = %v, want %v", err, ErrAuthenticationFailed)
	}
	validToken, err := restarted.IssueRecoveryToken(
		t.Context(),
		administrator,
		participant.ID,
		"verified participant again",
		"issue-participant-recovery-again",
	)
	if err != nil {
		t.Fatalf("reissue Administrator Recovery Token: %v", err)
	}
	if _, err = restarted.Recover(
		t.Context(),
		participant.Handle,
		validToken.Token,
		"administrator recovered horse battery staple",
		"recover-with-administrator-token",
	); err != nil {
		t.Fatalf("consume Administrator Recovery Token: %v", err)
	}
	if _, err = restarted.SignIn(
		t.Context(),
		participant.Handle,
		"administrator recovered horse battery staple",
	); err != nil {
		t.Fatalf("sign in with recovered Credential: %v", err)
	}
	if _, err = restarted.SignIn(
		t.Context(),
		participant.Handle,
		"replacement correct horse battery staple",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("replaced Credential error = %v, want %v", err, ErrAuthenticationFailed)
	}
	if _, err = restarted.Recover(
		t.Context(),
		participant.Handle,
		validToken.Token,
		"reused administrator horse battery staple",
		"reuse-administrator-token",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("reused Administrator Recovery Token error = %v, want %v", err, ErrAuthenticationFailed)
	}
	if _, err = restarted.IssueRecoveryToken(
		t.Context(),
		administrator,
		administrator.ID,
		"last Administrator forgot password",
		"issue-last-administrator-recovery",
	); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last Administrator recovery error = %v, want %v", err, ErrLastAdministrator)
	}
	administratorCodes, err := restarted.ReplaceRecoveryCodes(
		t.Context(),
		administrator,
		"replace-last-administrator-codes",
	)
	if err != nil {
		t.Fatalf("replace last Administrator Recovery Codes: %v", err)
	}
	if _, err = restarted.Recover(
		t.Context(),
		administrator.Handle,
		administratorCodes[0],
		"administrator host recovery required",
		"recover-last-administrator-with-code",
	); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf(
			"last Administrator Recovery Code error = %v, want %v",
			err,
			ErrAuthenticationFailed,
		)
	}

	audit, err := restarted.ListAuditEntries(t.Context(), administrator)
	if err != nil {
		t.Fatalf("list recovery Audit Entries: %v", err)
	}
	succeeded := make(map[string]int)
	foundRecoveryAudit := false
	secrets := append(append(append([]string{}, oldCodes...), codes...), administratorCodes...)
	secrets = append(secrets, token.Token, validToken.Token)
	for _, entry := range audit {
		if entry.Outcome == "Succeeded" {
			succeeded[entry.Action]++
		}
		if entry.Action == "IssueAccountRecoveryToken" &&
			entry.Outcome == "Succeeded" &&
			entry.Reason == "verified government ID in person" {
			foundRecoveryAudit = true
		}
		for _, secret := range secrets {
			if strings.Contains(entry.Reason, secret) || strings.Contains(entry.Note, secret) {
				t.Fatalf("recovery Audit Entry contains a secret: %+v", entry)
			}
		}
	}
	if !foundRecoveryAudit {
		t.Fatal("Administrator recovery Audit Entry not found")
	}
	if succeeded["ReplaceRecoveryCodes"] != 3 ||
		succeeded["RecoverAccount"] != 2 ||
		succeeded["IssueAccountRecoveryToken"] != 2 {
		t.Fatalf("recovery Audit Entry actions = %v", succeeded)
	}
}

func openAccountTestService(t *testing.T) (*Service, Account) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize authentication storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open authentication storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close authentication storage: %v", closeErr)
		}
	})
	service, err := New(storage, Config{
		Now:              time.Now,
		Random:           testRandomReader{},
		BootstrapTTL:     time.Hour,
		SessionTTL:       time.Hour,
		RecoveryTokenTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create authentication service: %v", err)
	}
	bootstrap, err := service.IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	session, err := service.BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Ada Admin",
		"administrator password",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	service.random = new(incrementingRandomReader)
	return service, session.Account
}

type testRandomReader struct{}

func (testRandomReader) Read(contents []byte) (int, error) {
	for index := range contents {
		contents[index] = byte(index + 1)
	}
	return len(contents), nil
}

type incrementingRandomReader struct {
	next byte
}

func (reader *incrementingRandomReader) Read(contents []byte) (int, error) {
	reader.next++
	for index := range contents {
		contents[index] = reader.next + byte(index)
	}
	return len(contents), nil
}

type staticStorageState bool

func (state staticStorageState) Degraded() bool {
	return bool(state)
}

func (staticStorageState) PrepareEmergencyStorage(context.Context) error {
	return nil
}

type testStorageState struct {
	degraded     bool
	prepareErr   error
	prepareCalls int
}

func (state *testStorageState) Degraded() bool {
	return state.degraded
}

func (state *testStorageState) PrepareEmergencyStorage(context.Context) error {
	state.prepareCalls++
	if state.prepareErr != nil {
		state.degraded = true
	}
	return state.prepareErr
}

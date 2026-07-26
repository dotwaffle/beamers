package auth

import (
	"context"
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
		Random:       testRandomReader{},
		BootstrapTTL: time.Hour,
		SessionTTL:   time.Hour,
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
		Random:       testRandomReader{},
		BootstrapTTL: time.Hour,
		SessionTTL:   time.Hour,
		StorageState: unwarmedState,
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
		Now:          time.Now,
		Random:       testRandomReader{},
		BootstrapTTL: time.Hour,
		SessionTTL:   time.Hour,
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
	return service, session.Account
}

type testRandomReader struct{}

func (testRandomReader) Read(contents []byte) (int, error) {
	for index := range contents {
		contents[index] = byte(index + 1)
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

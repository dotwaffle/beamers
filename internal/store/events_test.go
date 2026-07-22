package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"

	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/ent/eventgrant"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestEventAndGrantChangesCreateAuditEntries(t *testing.T) {
	installation := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installation, now)
	administratorContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	producer, err := createAccountCommand(t, installation, administratorContext, CreateAccountParams{
		ActorAccountID: administrator.ID,
		Name:           "Pat Producer",
		NormalizedName: "pat producer",
		PasswordHash:   "password hash",
		Now:            now.Add(time.Minute),
		CommandID:      "create-account-pat",
		PayloadHash:    strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatalf("create Producer Account: %v", err)
	}
	createdEvent, err := createEventCommand(t, installation, administratorContext, CreateEventParams{
		ActorAccountID:   administrator.ID,
		Name:             "Revision 2026",
		PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE",
		ContentLanguage: "en-GB", EventDayBoundary: "06:00",
		Now:       now.Add(2 * time.Minute),
		CommandID: "create-event-revision", PayloadHash: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	_, grantErr := grantEventAccessCommand(t, installation, administratorContext, GrantEventAccessParams{
		ActorAccountID: administrator.ID,
		EventID:        createdEvent.ID,
		AccountID:      producer.ID,
		Role:           eventgrant.RoleProducer,
		Now:            now.Add(3 * time.Minute),
		CommandID:      "grant-pat-producer", PayloadHash: strings.Repeat("e", 64),
	})
	if grantErr != nil {
		t.Fatalf("grant Producer access: %v", grantErr)
	}

	entries, err := installation.client.AuditEntry.Query().
		Order(auditentry.ByID(sql.OrderAsc())).
		All(administratorContext)
	if err != nil {
		t.Fatalf("read Audit Entries: %v", err)
	}
	wantActions := []string{"CreateAccount", "CreateEvent", "CreateEventGrant"}
	if len(entries) != len(wantActions) {
		t.Fatalf("Audit Entry count = %d, want %d", len(entries), len(wantActions))
	}
	for index, want := range wantActions {
		if entries[index].Action != want || entries[index].Result != auditentry.ResultSucceeded {
			t.Errorf("Audit Entry %d = %s/%s, want %s/Succeeded", index, entries[index].Action, entries[index].Result, want)
		}
		if entries[index].ActorAccountID != administrator.ID {
			t.Errorf("Audit Entry %d actor = %d, want %d", index, entries[index].ActorAccountID, administrator.ID)
		}
	}
}

func TestEventCommandRetryReturnsOriginalOutcomeAndConflictIsAudited(t *testing.T) {
	installation := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installation, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	params := CreateEventParams{
		ActorAccountID: administrator.ID, CommandID: "create-event-retry",
		PayloadHash: strings.Repeat("a", 64), Name: "Revision 2026",
		PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "00:00", Now: now,
	}
	first, err := createEventCommand(t, installation, ctx, params)
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	retry, err := createEventCommand(t, installation, ctx, params)
	if err != nil {
		t.Fatalf("retry Event creation: %v", err)
	}
	if retry != first {
		t.Errorf("retry outcome = %+v, want original %+v", retry, first)
	}
	params.PayloadHash = strings.Repeat("b", 64)
	_, conflictErr := createEventCommand(t, installation, ctx, params)
	if !errors.Is(conflictErr, ErrCommandConflict) {
		t.Fatalf("conflicting retry error = %v, want %v", conflictErr, ErrCommandConflict)
	}

	eventCount, err := installation.client.Event.Query().Count(viewer.SystemContext(t.Context()))
	if err != nil {
		t.Fatalf("count Events: %v", err)
	}
	receiptCount, err := installation.client.CommandReceipt.Query().Count(viewer.SystemContext(t.Context()))
	if err != nil {
		t.Fatalf("count Command Receipts: %v", err)
	}
	auditCount, err := installation.client.AuditEntry.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count Audit Entries: %v", err)
	}
	if eventCount != 1 || receiptCount != 1 || auditCount != 2 {
		t.Errorf(
			"Events/receipts/audits = %d/%d/%d, want 1/1/2",
			eventCount, receiptCount, auditCount,
		)
	}
}

func TestRejectedCommandRetryKeepsOneAuditWithDomainTarget(t *testing.T) {
	installation := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installation, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})

	for attempt := range 2 {
		transaction, err := installation.BeginCommand(ctx)
		if err != nil {
			t.Fatalf("begin rejected command attempt %d: %v", attempt+1, err)
		}
		identity := CommandIdentity{
			ActorAccountID: administrator.ID, CommandID: "grant-invalid-role",
			PayloadHash: strings.Repeat("f", 64), Action: "CreateEventGrant",
			TargetType: "EventGrant", TargetID: "7:11", Now: now,
		}
		_, retry, err := transaction.LookupReceipt(ctx, identity)
		if err != nil {
			t.Fatalf("lookup rejected command attempt %d: %v", attempt+1, err)
		}
		if !retry {
			if err := transaction.RecordRejection(ctx, identity, CommandRejection{Code: "producer_required"}); err != nil {
				t.Fatalf("record rejected command attempt %d: %v", attempt+1, err)
			}
			if err := transaction.Commit(); err != nil {
				t.Fatalf("commit rejected command attempt %d: %v", attempt+1, err)
			}
		} else if err := transaction.Rollback(); err != nil {
			t.Fatalf("rollback rejected retry attempt %d: %v", attempt+1, err)
		}
		if retry != (attempt == 1) {
			t.Errorf("attempt %d retry = %t, want %t", attempt+1, retry, attempt == 1)
		}
	}

	entries, err := installation.client.AuditEntry.Query().All(ctx)
	if err != nil {
		t.Fatalf("read rejected Audit Entry: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("rejected Audit Entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Result != auditentry.ResultRejected ||
		entry.TargetType != "EventGrant" || entry.TargetID != "7:11" {
		t.Errorf(
			"rejected Audit Entry = %s %s/%s, want Rejected EventGrant/7:11",
			entry.Result, entry.TargetType, entry.TargetID,
		)
	}
}

func TestEventPrivacyDefaultsDenyAndScopesEveryRole(t *testing.T) {
	installation := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installation, now)
	administratorContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	producer := createEventTestAccount(t, installation, administratorContext, administrator.ID, "Pat Producer", now)
	observer := createEventTestAccount(t, installation, administratorContext, administrator.ID, "Oli Observer", now)
	operator := createEventTestAccount(t, installation, administratorContext, administrator.ID, "Opal Operator", now)
	first := createEventTestEvent(t, installation, administratorContext, administrator.ID, "First Event", now)
	second := createEventTestEvent(t, installation, administratorContext, administrator.ID, "Second Event", now)
	grantEventTestRole(
		t, installation, administratorContext, administrator.ID, first.ID, producer.ID,
		eventgrant.RoleProducer, now,
	)
	grantEventTestRole(
		t, installation, administratorContext, administrator.ID, first.ID, operator.ID,
		eventgrant.RoleOperator, now,
	)
	grantEventTestRole(
		t, installation, administratorContext, administrator.ID, first.ID, observer.ID,
		eventgrant.RoleObserver, now,
	)

	producerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: producer.ID, EventRoles: map[int]viewer.Role{first.ID: viewer.Producer},
	})
	observerContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: observer.ID, EventRoles: map[int]viewer.Role{first.ID: viewer.Observer},
	})
	operatorContext := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: operator.ID, EventRoles: map[int]viewer.Role{first.ID: viewer.Operator},
	})
	if _, err := installation.client.Event.Get(producerContext, first.ID); err != nil {
		t.Errorf("Producer read granted Event: %v", err)
	}
	if _, err := installation.client.Event.Get(observerContext, first.ID); err != nil {
		t.Errorf("Observer read granted Event: %v", err)
	}
	if _, err := installation.client.Event.Get(operatorContext, first.ID); err != nil {
		t.Errorf("Operator read granted Event: %v", err)
	}
	if _, err := installation.client.Event.Get(t.Context(), first.ID); !errors.Is(err, privacy.Deny) {
		t.Errorf("missing-viewer read error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Event.Get(administratorContext, first.ID); !errors.Is(err, privacy.Deny) {
		t.Errorf("Administrator-only read error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Event.Get(producerContext, second.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-Event Producer read error = %v, want not found", err)
	}
	if _, err := installation.client.Event.UpdateOneID(first.ID).
		SetName("Observer edit").Save(observerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("Observer mutation error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Event.UpdateOneID(first.ID).
		SetName("Operator edit").Save(operatorContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("Operator mutation error = %v, want privacy denial", err)
	}
	if _, err := installation.client.EventGrant.Query().Count(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("Producer Event Grant query error = %v, want privacy denial", err)
	}
	if _, err := installation.client.EventGrant.Query().Count(administratorContext); err != nil {
		t.Errorf("Administrator Event Grant query: %v", err)
	}
	if _, err := installation.client.Account.Query().Count(t.Context()); !errors.Is(err, privacy.Deny) {
		t.Errorf("missing-viewer Account query error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Account.Query().Count(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("Producer Account query error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Account.Query().Count(administratorContext); err != nil {
		t.Errorf("Administrator Account query: %v", err)
	}
	if _, err := installation.client.PasswordCredential.Query().Count(administratorContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("Administrator credential query error = %v, want privacy denial", err)
	}
	if _, err := installation.client.AuditEntry.Create().
		SetActorAccountID(administrator.ID).
		SetAction("ForgedAudit").
		SetTargetType("Event").
		SetTargetID(strconv.Itoa(first.ID)).
		SetResult(auditentry.ResultRejected).
		Save(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("forged Audit actor error = %v, want privacy denial", err)
	}
	if _, err := installation.client.Event.UpdateOneID(second.ID).
		SetName("Cross-Event edit").Save(producerContext); !errors.Is(err, privacy.Deny) {
		t.Errorf("cross-Event Producer mutation error = %v, want privacy denial", err)
	}
	if _, err := installation.client.AuditEntry.Delete().Exec(viewer.SystemContext(t.Context())); !errors.Is(err, privacy.Deny) {
		t.Errorf("Audit Entry deletion error = %v, want append-only privacy denial", err)
	}
	if _, err := installation.client.CommandReceipt.Delete().Exec(viewer.SystemContext(t.Context())); !errors.Is(err, privacy.Deny) {
		t.Errorf("Command Receipt deletion error = %v, want immutable privacy denial", err)
	}
}

func createEventTestAccount(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	administratorID int,
	name string,
	now time.Time,
) AccountCredential {
	t.Helper()
	created, err := createAccountCommand(t, installation, ctx, CreateAccountParams{
		ActorAccountID: administratorID, Name: name, NormalizedName: strings.ToLower(name),
		PasswordHash: "password hash", Now: now,
		CommandID:   "create-account-" + strings.ReplaceAll(strings.ToLower(name), " ", "-"),
		PayloadHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("create %s Account: %v", name, err)
	}
	return created
}

func createEventTestEvent(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	administratorID int,
	name string,
	now time.Time,
) Event {
	t.Helper()
	created, err := createEventCommand(t, installation, ctx, CreateEventParams{
		ActorAccountID: administratorID, Name: name,
		PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-23",
		Timezone: "Europe/Berlin", EventLocale: "de-DE", EventDayBoundary: "00:00", Now: now,
		CommandID:   "create-event-" + strings.ReplaceAll(strings.ToLower(name), " ", "-"),
		PayloadHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return created
}

func grantEventTestRole(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	administratorID int,
	eventID int,
	accountID int,
	role eventgrant.Role,
	now time.Time,
) {
	t.Helper()
	if _, err := grantEventAccessCommand(t, installation, ctx, GrantEventAccessParams{
		ActorAccountID: administratorID, EventID: eventID, AccountID: accountID,
		Role: role, Now: now,
		CommandID:   "grant-" + strconv.Itoa(eventID) + "-" + strconv.Itoa(accountID),
		PayloadHash: strings.Repeat("c", 64),
	}); err != nil {
		t.Fatalf("grant %s role: %v", role, err)
	}
}

func createEventCommand(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params CreateEventParams,
) (Event, error) {
	t.Helper()
	identity := CommandIdentity{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateEvent", TargetType: "Event", Now: params.Now,
	}
	return executeTestCommand(t, installation, ctx, identity, func(transaction *CommandTx) (Event, error) {
		return transaction.CreateEvent(ctx, params)
	}, func(created Event) string { return strconv.Itoa(created.ID) })
}

func createAccountCommand(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params CreateAccountParams,
) (AccountCredential, error) {
	t.Helper()
	identity := CommandIdentity{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateAccount", TargetType: "Account", Now: params.Now,
	}
	return executeTestCommand(t, installation, ctx, identity, func(transaction *CommandTx) (AccountCredential, error) {
		return transaction.CreateAccount(ctx, params)
	}, func(created AccountCredential) string { return strconv.Itoa(created.ID) })
}

func grantEventAccessCommand(
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	params GrantEventAccessParams,
) (EventGrant, error) {
	t.Helper()
	identity := CommandIdentity{
		ActorAccountID: params.ActorAccountID, CommandID: params.CommandID,
		PayloadHash: params.PayloadHash, Action: "CreateEventGrant", TargetType: "EventGrant",
		TargetID: strconv.Itoa(params.EventID) + ":" + strconv.Itoa(params.AccountID), Now: params.Now,
	}
	return executeTestCommand(t, installation, ctx, identity, func(transaction *CommandTx) (EventGrant, error) {
		return transaction.GrantEventAccess(ctx, params)
	}, nil)
}

func executeTestCommand[T any](
	t *testing.T,
	installation *SQLite,
	ctx context.Context,
	identity CommandIdentity,
	mutate func(*CommandTx) (T, error),
	targetID func(T) string,
) (T, error) {
	t.Helper()
	var zero T
	transaction, err := installation.BeginCommand(ctx)
	if err != nil {
		return zero, err
	}
	defer func() { _ = transaction.Rollback() }()
	outcome, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(ctx, identity); commitErr != nil {
			return zero, commitErr
		}
		return zero, ErrCommandConflict
	}
	if err != nil {
		return zero, err
	}
	if retry {
		var original T
		if decodeErr := DecodeCommandReceipt(outcome, &original); decodeErr != nil {
			return zero, decodeErr
		}
		return original, nil
	}
	result, err := mutate(transaction)
	if err != nil {
		return zero, err
	}
	if targetID != nil {
		identity.TargetID = targetID(result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return zero, err
	}
	if err := transaction.RecordOutcome(ctx, identity, string(encoded), false); err != nil {
		return zero, err
	}
	if err := transaction.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func openEventTestInstallation(t *testing.T) *SQLite {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize Event database: %v", err)
	}
	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open Event database: %v", err)
	}
	t.Cleanup(func() {
		if err := installation.Close(); err != nil {
			t.Errorf("close Event database: %v", err)
		}
	})
	return installation
}

func bootstrapEventTestAdministrator(
	t *testing.T,
	installation *SQLite,
	now time.Time,
) AccountCredential {
	t.Helper()
	bootstrapHash := strings.Repeat("a", 64)
	if err := installation.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Minute)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	administrator, err := installation.BootstrapAdministrator(
		t.Context(),
		BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash, Name: "Ada Admin", NormalizedName: "ada admin",
			PasswordHash: "password hash", SessionHash: strings.Repeat("b", 64),
			Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	return administrator
}

package store

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent/auditentry"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestActivateEventCommitsGenerationReceiptAndAuditTogether(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 17, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installationStore, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	event := createEventTestEvent(t, installationStore, ctx, administrator.ID, "Revision 2026", now)
	transaction, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin activation command: %v", err)
	}
	activated, err := transaction.ActivateEvent(ctx, event.ID, event.Revision, 0, 0)
	if err != nil {
		t.Fatalf("activate Event: %v", err)
	}
	identity := CommandIdentity{
		ActorAccountID: administrator.ID, CommandID: "activate-event-store",
		PayloadHash: strings.Repeat("a", 64), Action: "ActivateEvent",
		TargetType: "Event", TargetID: strconv.Itoa(event.ID), Now: now,
	}
	if recordErr := transaction.RecordOutcome(ctx, identity, `{"event_id":1,"generation":1}`, false); recordErr != nil {
		t.Fatalf("record activation outcome: %v", recordErr)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		t.Fatalf("commit activation: %v", commitErr)
	}
	if activated.EventID != event.ID || activated.Generation != 1 {
		t.Fatalf("activation = %+v, want Event %d generation 1", activated, event.ID)
	}

	active, err := installationStore.LoadActiveEvent(ctx)
	if err != nil {
		t.Fatalf("load Active Event: %v", err)
	}
	if active != activated {
		t.Errorf("Active Event = %+v, want %+v", active, activated)
	}
	receipts, err := installationStore.client.CommandReceipt.Query().All(viewer.SystemContext(t.Context()))
	if err != nil {
		t.Fatalf("read activation Command Receipt: %v", err)
	}
	audits, err := installationStore.client.AuditEntry.Query().All(ctx)
	if err != nil {
		t.Fatalf("read activation Audit Entry: %v", err)
	}
	if len(receipts) != 2 || len(audits) != 2 {
		t.Fatalf("receipts/audits = %d/%d, want 2/2 including Event creation", len(receipts), len(audits))
	}
	activationAudit := audits[len(audits)-1]
	if activationAudit.Action != "ActivateEvent" || activationAudit.Result != auditentry.ResultSucceeded {
		t.Errorf("activation Audit Entry = %s/%s, want ActivateEvent/Succeeded", activationAudit.Action, activationAudit.Result)
	}
}

func TestActivateEventRollbackLeavesNoRoutingChange(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	now := time.Date(2026, time.July, 22, 17, 0, 0, 0, time.UTC)
	administrator := bootstrapEventTestAdministrator(t, installationStore, now)
	ctx := viewer.NewContext(t.Context(), viewer.Identity{
		AccountID: administrator.ID, Administrator: true,
	})
	event := createEventTestEvent(t, installationStore, ctx, administrator.ID, "Revision 2026", now)
	transaction, err := installationStore.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin activation command: %v", err)
	}
	if _, activateErr := transaction.ActivateEvent(ctx, event.ID, event.Revision, 0, 0); activateErr != nil {
		t.Fatalf("activate Event before rollback: %v", activateErr)
	}
	if rollbackErr := transaction.Rollback(); rollbackErr != nil {
		t.Fatalf("roll back activation: %v", rollbackErr)
	}
	active, err := installationStore.LoadActiveEvent(ctx)
	if err != nil {
		t.Fatalf("load Active Event after rollback: %v", err)
	}
	if active != (ActiveEventState{}) {
		t.Errorf("Active Event after rollback = %+v, want none", active)
	}
}

func TestInstallationActivationPrivacyRequiresAdministrator(t *testing.T) {
	installationStore := openEventTestInstallation(t)
	if _, err := installationStore.client.Installation.Query().Only(t.Context()); !errors.Is(err, privacy.Deny) {
		t.Fatalf("missing-viewer Installation query error = %v, want privacy denial", err)
	}
	nonAdministrator := viewer.NewContext(t.Context(), viewer.Identity{AccountID: 42})
	if _, err := installationStore.client.Installation.Query().Only(nonAdministrator); !errors.Is(err, privacy.Deny) {
		t.Fatalf("non-Administrator Installation query error = %v, want privacy denial", err)
	}
	if _, err := installationStore.client.Installation.Update().
		SetActivationGeneration(1).
		Save(nonAdministrator); !errors.Is(err, privacy.Deny) {
		t.Fatalf("non-Administrator Installation mutation error = %v, want privacy denial", err)
	}
	administrator := viewer.NewContext(t.Context(), viewer.Identity{AccountID: 1, Administrator: true})
	if _, err := installationStore.client.Installation.Query().Only(administrator); err != nil {
		t.Fatalf("Administrator Installation query: %v", err)
	}
}

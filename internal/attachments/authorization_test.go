package attachments

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestRefusedAttachmentCommandsLeaveEvidence covers the Attachment actions
// whose refusals left no trace before the Capability Table judged them.
//
// The Crew upload and Reopen Window guards refused ahead of Execute, so a
// Crew Member who was refused could not be shown why and an Administrator
// reviewing history could not see that it happened. Now the refusal is a
// durable rejection like any other: a Command Receipt that makes retrying it
// return the same answer, and a Rejected Audit Entry naming the same code the
// imperative check used.
func TestRefusedAttachmentCommandsLeaveEvidence(t *testing.T) {
	storage, producer, eventID := openAttachmentAuthorizationTest(t)
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	service, err := New(
		t.Context(), storage, t.TempDir(), func() time.Time { return now }, nil,
	)
	if err != nil {
		t.Fatalf("create Attachment service: %v", err)
	}

	// An Operator of this Event who was never granted ManageAttachments.
	operator := producer
	operator.Administrator = false
	operator.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	operator.EventScopes = nil

	refusals := []struct {
		action string
		refuse func() error
	}{
		{
			action: "UploadAttachment",
			refuse: func() error {
				_, err := service.UploadForCrew(t.Context(), operator, CrewUploadInput{
					EventID: eventID, TargetID: 1, TargetType: TargetPresentation,
					CommandID: "refused-crew-upload", Name: "slides",
					OriginalFilename: "slides.bin",
					Body:             strings.NewReader("payload"),
				})
				return err
			},
		},
		{
			action: "CreateReopenWindow",
			refuse: func() error {
				_, err := service.CreateReopenWindow(t.Context(), operator, ReopenInput{
					EventID: eventID, TargetID: 1, TargetType: TargetPresentation,
					Reason: "late substitution", ExpiresAt: now.Add(time.Hour),
					CommandID: "refused-reopen-create",
				})
				return err
			},
		},
		{
			action: "CloseReopenWindow",
			refuse: func() error {
				_, err := service.UpdateReopenWindow(t.Context(), operator, UpdateReopenInput{
					EventID: eventID, WindowID: 1, ExpectedRevision: 1, Close: true,
					CommandID: "refused-reopen-close",
				})
				return err
			},
		},
	}

	for _, refusal := range refusals {
		t.Run(refusal.action, func(t *testing.T) {
			if err := refusal.refuse(); !errors.Is(err, ErrProducerRequired) {
				t.Fatalf("refused %s = %v, want %v", refusal.action, err, ErrProducerRequired)
			}
			if err := refusal.refuse(); !errors.Is(err, ErrProducerRequired) {
				t.Fatalf(
					"retry of refused %s = %v, want the recorded refusal %v",
					refusal.action, err, ErrProducerRequired,
				)
			}
			entries := rejectedAttachmentAudits(t, storage, producer, refusal.action)
			if len(entries) != 1 {
				t.Fatalf(
					"Rejected Audit Entries for %s = %d, want exactly one recorded refusal "+
						"whose retry is answered from its Command Receipt",
					refusal.action, len(entries),
				)
			}
			if entries[0].Reason != "producer_required" {
				t.Errorf(
					"Rejected Audit Entry reason for %s = %q, want the durable code the imperative check used",
					refusal.action, entries[0].Reason,
				)
			}
		})
	}
}

// TestUploadRowAdmitsBothCallers covers the D9 resolution: one UploadAttachment
// row serves two callers with different rules. The Crew caller demands
// ManageAttachments through the row's TargetCapabilities, which a Producer
// holds through role expansion. The Account caller carries no demand and is
// admitted by the table unconditionally, because its rule is upload-target
// ownership, which the store enforces. Both are proven here by reaching the
// ownership refusal — not the capability refusal — for a target that does not
// exist.
func TestUploadRowAdmitsBothCallers(t *testing.T) {
	storage, producer, eventID := openAttachmentAuthorizationTest(t)
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	service, err := New(
		t.Context(), storage, t.TempDir(), func() time.Time { return now }, nil,
	)
	if err != nil {
		t.Fatalf("create Attachment service: %v", err)
	}

	if _, err = service.UploadForCrew(t.Context(), producer, CrewUploadInput{
		EventID: eventID, TargetID: 1, TargetType: TargetPresentation,
		CommandID: "producer-crew-upload", Name: "slides",
		OriginalFilename: "slides.bin", Body: strings.NewReader("payload"),
	}); !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf(
			"Crew upload by Producer = %v, want the ownership refusal %v",
			err, ErrUploadTargetNotFound,
		)
	}

	holder := producer
	holder.Administrator = false
	holder.EventRoles = nil
	holder.EventScopes = nil
	if _, err = service.UploadForAccount(t.Context(), holder, AccountUploadInput{
		EventID: eventID, TargetID: 1, TargetType: TargetPresentation,
		CommandID: "holder-account-upload", Name: "slides",
		OriginalFilename: "slides.bin", Body: strings.NewReader("payload"),
	}); !errors.Is(err, ErrUploadTargetNotFound) {
		t.Fatalf(
			"Account upload by target non-owner = %v, want the ownership refusal %v",
			err, ErrUploadTargetNotFound,
		)
	}
}

// openAttachmentAuthorizationTest returns storage, a Producer, and one Event.
func openAttachmentAuthorizationTest(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()

	dataDir := t.TempDir()
	systemCtx := systemactor.NewContext(t.Context(), systemactor.HostMaintenance)
	if err := store.Initialize(systemCtx, dataDir); err != nil {
		t.Fatalf("initialize Attachment store: %v", err)
	}
	storage, err := store.Open(systemCtx, dataDir)
	if err != nil {
		t.Fatalf("open Attachment store: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	now := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	if err = storage.IssueBootstrap(
		systemCtx, strings.Repeat("b", 64), now, now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue Attachment bootstrap: %v", err)
	}
	created, err := storage.BootstrapAdministrator(
		systemCtx,
		store.BootstrapAdministratorParams{
			BootstrapHash: strings.Repeat("b", 64),
			Name:          "Producer", NormalizedName: "producer", PasswordHash: "password-hash",
			SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Attachment Producer: %v", err)
	}
	producer := auth.Account{ID: created.ID, Name: created.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now }, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), producer, events.CreateInput{
		Name: "Attachment Event", PlannedStartDate: "2026-08-21", PlannedEndDate: "2026-08-21",
		Timezone: "UTC", EventLocale: "en-US", CommandID: "create-attachment-event",
	})
	if err != nil {
		t.Fatalf("create Attachment Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), producer, event.ID, producer.ID, "Producer", "grant-attachment-producer",
	); err != nil {
		t.Fatalf("grant Attachment Producer: %v", err)
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, producer, event.ID
}

// rejectedAttachmentAudits returns the Rejected Audit Entries recorded for one
// command action.
func rejectedAttachmentAudits(
	t *testing.T,
	storage *store.SQLite,
	reader auth.Account,
	action string,
) []store.AuditEntry {
	t.Helper()

	entries, err := storage.ListAuditEntries(reader.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Audit Entries: %v", err)
	}
	rejected := make([]store.AuditEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Action == action && entry.Outcome == "Rejected" {
			rejected = append(rejected, entry)
		}
	}
	return rejected
}

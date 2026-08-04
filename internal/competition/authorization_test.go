package competition

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/store"
)

// TestRefusedCompetitionCommandsLeaveEvidence covers the Competition actions
// whose refusals left no trace before the Capability Table judged them.
//
// Each of these refused inside its own application, or by a check that ran
// without recording anything, so a Crew Member who was refused could not be
// shown why and an Administrator reviewing history could not see that it
// happened. Now the refusal is a durable rejection like any other: a Command
// Receipt that makes retrying it return the same answer, and a Rejected Audit
// Entry naming the same code the imperative check uses.
func TestRefusedCompetitionCommandsLeaveEvidence(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	sessionID := publishNotificationCompetition(t, storage, producer, eventID)
	service, err := New(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create Competition service: %v", err)
	}

	revoked := producer
	revoked.Administrator = false
	revoked.EventRoles = nil
	revoked.EventScopes = nil

	refusals := []struct {
		action string
		refuse func() error
	}{
		{
			action: "ConfigureCompetitionReadiness",
			refuse: func() error {
				_, err := service.ConfigureReadiness(t.Context(), revoked, ConfigureReadinessInput{
					EventID: eventID, SessionID: sessionID, CommandID: "refused-readiness",
					RequireEntryReview: true,
				})
				return err
			},
		},
		{
			action: "ConfigureCompetitionEntryOrder",
			refuse: func() error {
				_, err := service.ConfigureEntryOrder(t.Context(), revoked, ConfigureEntryOrderInput{
					EventID: eventID, SessionID: sessionID, CommandID: "refused-entry-order",
					Policy: EntryOrderSubmission,
				})
				return err
			},
		},
		{
			action: "SetEntryAttachmentReadiness",
			refuse: func() error {
				_, err := service.SetEntryAttachmentReadiness(
					t.Context(), revoked, SetEntryAttachmentReadinessInput{
						EventID: eventID, SessionID: sessionID, EntryID: 1,
						AttachmentVersionID: 1, ExpectedRevision: 1,
						CommandID: "refused-attachment-readiness", Final: true,
					},
				)
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
			entries := rejectedCompetitionAudits(t, storage, producer, refusal.action)
			if len(entries) != 1 {
				t.Fatalf(
					"Rejected Audit Entries for %s = %d, want exactly one recorded refusal "+
						"whose retry is answered from its Command Receipt",
					refusal.action, len(entries),
				)
			}
			if entries[0].Reason != "producer_required" {
				t.Errorf(
					"Rejected Audit Entry reason for %s = %q, want the durable code the imperative check uses",
					refusal.action, entries[0].Reason,
				)
			}
		})
	}
}

// rejectedCompetitionAudits returns the Rejected Audit Entries recorded for one
// command action.
func rejectedCompetitionAudits(
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

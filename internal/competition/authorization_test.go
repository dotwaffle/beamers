package competition

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
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

// TestLiveEntryCommandsAreLaneScoped covers the D4 resolution: a live
// Competition Entry action is judged by the Lanes of the Entry's Competition
// Session, not by Event-wide Producer or Operator authority.
//
// An Operator whose Lane grant does not cover the Session is refused with the
// durable code the Event-wide guard produced, the refusal answers its own
// retry, and it leaves a Rejected Audit Entry. An Operator whose Lane grant
// does cover the Session is admitted — including on Set Release Hold, whose
// old imperative guard demanded a Producer even though its Capability always
// named the Operator class.
func TestLiveEntryCommandsAreLaneScoped(t *testing.T) {
	storage, producer, eventID := openNotificationTest(t)
	sessionID := publishNotificationCompetition(t, storage, producer, eventID)
	laneID := competitionLaneID(t, storage, producer, eventID)
	service, err := New(
		storage,
		func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) },
		nil, nil,
	)
	if err != nil {
		t.Fatalf("create Competition service: %v", err)
	}
	created, err := service.CreateEntry(t.Context(), producer, CreateEntryInput{
		EventID: eventID, SessionID: sessionID, CommandID: "create-lane-scoped-entry",
		Name: "Aurora", PublicDetails: "Public entry",
	})
	if err != nil {
		t.Fatalf("create Competition Entry: %v", err)
	}

	laneOperator := producer
	laneOperator.Administrator = false
	laneOperator.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	laneOperator.EventScopes = map[int]viewer.EventScope{
		eventID: {LaneIDs: map[int]struct{}{laneID: {}}},
	}
	outsider := laneOperator
	outsider.EventScopes = nil

	refuse := func(t *testing.T, action string, want error, run func() error) {
		t.Helper()
		if err := run(); !errors.Is(err, want) {
			t.Fatalf("refused %s = %v, want %v", action, err, want)
		}
		if err := run(); !errors.Is(err, want) {
			t.Fatalf("retry of refused %s = %v, want the recorded refusal %v", action, err, want)
		}
		entries := rejectedCompetitionAudits(t, storage, producer, action)
		if len(entries) != 1 {
			t.Fatalf(
				"Rejected Audit Entries for %s = %d, want exactly one recorded refusal "+
					"whose retry is answered from its Command Receipt", action, len(entries),
			)
		}
	}

	refuse(t, "SetCompetitionEntryReleaseHold", ErrProducerRequired, func() error {
		_, err := service.SetEntryReleaseHold(t.Context(), outsider, SetEntryReleaseHoldInput{
			EventID: eventID, SessionID: sessionID, EntryID: created.ID,
			CommandID: "refused-release-hold", ExpectedRevision: created.Revision,
			Hold: true, CrewReason: "held for review",
		})
		return err
	})
	refuse(t, "RecordCompetitionTechnicalFailure", ErrOperatorRequired, func() error {
		_, err := service.RecordTechnicalFailure(t.Context(), outsider, RecordTechnicalFailureInput{
			EventID: eventID, SessionID: sessionID, EntryID: created.ID,
			CommandID: "refused-technical-failure", ExpectedRevision: created.Revision,
			Reason: "playback failed",
		})
		return err
	})

	// The Lane-scoped Operator is admitted where the old guard demanded a
	// Producer, which is the behavior change D4 decided.
	held, err := service.SetEntryReleaseHold(t.Context(), laneOperator, SetEntryReleaseHoldInput{
		EventID: eventID, SessionID: sessionID, EntryID: created.ID,
		CommandID: "admitted-release-hold", ExpectedRevision: created.Revision,
		Hold: true, CrewReason: "held for review",
	})
	if err != nil {
		t.Fatalf("Set Release Hold by Lane-scoped Operator = %v, want admitted", err)
	}
	if _, err = service.RecordTechnicalFailure(t.Context(), laneOperator, RecordTechnicalFailureInput{
		EventID: eventID, SessionID: sessionID, EntryID: created.ID,
		CommandID: "admitted-technical-failure", ExpectedRevision: held.Revision,
		Reason: "playback failed",
	}); err != nil {
		t.Fatalf("Record Technical Failure by Lane-scoped Operator = %v, want admitted", err)
	}
}

// competitionLaneID returns the single published Lane of the fixture rundown.
func competitionLaneID(
	t *testing.T,
	storage *store.SQLite,
	reader auth.Account,
	eventID int,
) int {
	t.Helper()

	queries, err := rundown.NewQueries(storage, nil)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), reader, eventID)
	if err != nil || len(crew.Lanes) != 1 {
		t.Fatalf("load Competition Lane = %+v, %v", crew, err)
	}
	return crew.Lanes[0].ID
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

package results_test

import (
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// TestRefusedResultsCommandsLeaveEvidence covers the Manage Results saves whose
// refusals left no trace before the Capability Table judged them.
//
// Each of these refused in the Results application before the command path
// opened, so a Crew Member who was refused could not be shown why, an
// Administrator reviewing history could not see that it happened, and a retry
// was judged afresh rather than answered from what the installation recorded.
// Now the refusal is a durable rejection like any other: a Command Receipt that
// makes retrying it return the same answer, and a Rejected Audit Entry naming
// the same code the imperative check used.
func TestRefusedResultsCommandsLeaveEvidence(t *testing.T) {
	storage, producer, eventID, _ := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	}
	_, competitionID := publishPrizegivingSessions(t, storage, producer, eventID, now)
	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}

	// A Crew Member of this Event who may read unreleased Results but was
	// never granted the authority to change them.
	crew := producer
	crew.Administrator = false
	crew.EventRoles = map[int]viewer.Role{eventID: viewer.Observer}
	crew.EventScopes = map[int]viewer.EventScope{
		eventID: {
			Capabilities: map[viewer.Capability]struct{}{viewer.ViewResults: {}},
		},
	}

	refusals := []struct {
		action string
		refuse func() error
	}{
		{
			action: "SaveCompetitionResultsDraft",
			refuse: func() error {
				_, err := service.Save(t.Context(), crew, results.SaveInput{
					EventID: eventID, SessionID: competitionID,
					CommandID: "refused-results-draft", Disposition: results.Publish,
					Score: results.ScorePolicy{Type: results.None},
				})
				return err
			},
		},
		{
			action: "SaveCompetitionAwards",
			refuse: func() error {
				_, err := service.SaveCompetitionAwards(
					t.Context(), crew, results.SaveCompetitionAwardsInput{
						EventID: eventID, SessionID: competitionID,
						CommandID: "refused-competition-awards",
						Awards: []results.Award{{
							Key: "spirit", Name: "Spirit Award", DisplayOrder: 1,
							Recipients: []results.AwardRecipient{{DisplayName: "Volunteers"}},
						}},
					},
				)
				return err
			},
		},
		{
			action: "SaveEventAwardsDraft",
			refuse: func() error {
				_, err := service.SaveEventAwards(
					t.Context(), crew, results.SaveEventAwardsInput{
						EventID: eventID, CommandID: "refused-event-awards",
						Awards: []results.EventAward{{
							Award: results.Award{
								Key: "community", Name: "Community Award", DisplayOrder: 1,
								Recipients: []results.AwardRecipient{{DisplayName: "Volunteers"}},
							},
							ReleasePath: results.AwardReleasePath{
								Kind: results.StandaloneRelease,
							},
						}},
					},
				)
				return err
			},
		},
	}

	for _, refusal := range refusals {
		t.Run(refusal.action, func(t *testing.T) {
			if err := refusal.refuse(); !errors.Is(err, results.ErrManageRequired) {
				t.Fatalf(
					"refused %s = %v, want %v",
					refusal.action, err, results.ErrManageRequired,
				)
			}
			if err := refusal.refuse(); !errors.Is(err, results.ErrManageRequired) {
				t.Fatalf(
					"retry of refused %s = %v, want the recorded refusal %v",
					refusal.action, err, results.ErrManageRequired,
				)
			}
			entries := rejectedResultsAudits(t, storage, producer, refusal.action)
			if len(entries) != 1 {
				t.Fatalf(
					"Rejected Audit Entries for %s = %d, want exactly one recorded refusal "+
						"whose retry is answered from its Command Receipt",
					refusal.action, len(entries),
				)
			}
			if entries[0].Reason != "manage_results_required" {
				t.Errorf(
					"Rejected Audit Entry reason for %s = %q, want the durable code "+
						"the imperative check used",
					refusal.action, entries[0].Reason,
				)
			}
		})
	}
}

// TestManageResultsGrantSufficesWithoutProducerRole covers the D13
// resolution: the Results actions that layered a CanProduceEvent floor over
// their ManageResults rows lost that floor, so a Crew Member holding a
// ManageResults grant — here an Operator, the weakest role that can carry
// one — is admitted where previously only a Producer was. A Producer keeps
// access through role expansion, which the fixture's own setup exercises.
func TestManageResultsGrantSufficesWithoutProducerRole(t *testing.T) {
	storage, producer, eventID, _ := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	}
	_, competitionID := publishPrizegivingSessions(t, storage, producer, eventID, now)
	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}

	grantee := producer
	grantee.Administrator = false
	grantee.EventRoles = map[int]viewer.Role{eventID: viewer.Operator}
	grantee.EventScopes = map[int]viewer.EventScope{
		eventID: {
			Capabilities: map[viewer.Capability]struct{}{viewer.ManageResults: {}},
		},
	}

	saved, err := service.Save(t.Context(), grantee, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "grantee-draft", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save draft by ManageResults grantee = %v, want admitted", err)
	}
	if _, err = service.MarkReady(t.Context(), grantee, results.MarkReadyInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "grantee-mark-ready", ExpectedRevision: saved.Revision,
	}); err != nil {
		t.Fatalf("Mark Ready by ManageResults grantee = %v, want admitted", err)
	}
}

// rejectedResultsAudits returns the Rejected Audit Entries recorded for one
// command action.
func rejectedResultsAudits(
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

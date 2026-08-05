package voting

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/store"
)

// TestRefusedVotingCommandsLeaveEvidence covers the Voting actions whose
// refusals left no trace before the Capability Table judged them.
//
// Each of these refused ahead of Execute, so a Crew Member who was refused
// could not be shown why and an Administrator reviewing history could not see
// that it happened. Now the refusal is a durable rejection like any other: a
// Command Receipt that makes retrying it return the same answer, and a
// Rejected Audit Entry naming the same code the imperative check used.
func TestRefusedVotingCommandsLeaveEvidence(t *testing.T) {
	storage, producer, attendee, eventID, _ := votingFixture(t)
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	service, err := New(
		storage,
		bytes.NewReader(make([]byte, votingTokenBytes)),
		func() time.Time { return now },
		nil,
	)
	if err != nil {
		t.Fatalf("create Voting service: %v", err)
	}

	refusals := []struct {
		action string
		refuse func() error
	}{
		{
			action: "ConfigureCompetitionVoting",
			refuse: func() error {
				_, err := service.Configure(t.Context(), attendee, ConfigureInput{
					EventID: eventID, SessionID: 1, CommandID: "refused-voting-configure",
				})
				return err
			},
		},
		{
			action: "OpenCompetitionVoting",
			refuse: func() error {
				_, err := service.Open(t.Context(), attendee, WindowInput{
					EventID: eventID, SessionID: 1, CommandID: "refused-voting-open",
				})
				return err
			},
		},
		{
			action: "CloseCompetitionVoting",
			refuse: func() error {
				_, err := service.Close(t.Context(), attendee, WindowInput{
					EventID: eventID, SessionID: 1, CommandID: "refused-voting-close",
				})
				return err
			},
		},
		{
			action: "IssueVotingKeys",
			refuse: func() error {
				_, err := service.Issue(t.Context(), attendee, IssueInput{
					EventID: eventID, Count: 1, ExpiresAt: now.Add(time.Hour),
					CommandID: "refused-voting-issue",
				})
				return err
			},
		},
		{
			action: "RevokeVotingKey",
			refuse: func() error {
				_, err := service.Revoke(
					t.Context(), attendee, eventID, 1, "refused-voting-revoke",
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
			entries := rejectedVotingAudits(t, storage, producer, refusal.action)
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

// rejectedVotingAudits returns the Rejected Audit Entries recorded for one
// command action.
func rejectedVotingAudits(
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

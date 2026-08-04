package frontend

import (
	"testing"

	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
)

// TestResultsEventEntriesRetainsIneligibleRecipients confirms the Event
// Award recipient picker keeps a previously saved recipient selectable even
// after their Entry drops out of the Workspace's Included/Eligible
// projection (results.Workspace only lists Included, Eligible Entries, but
// store validation accepts any Entry belonging to the Event).
func TestResultsEventEntriesRetainsIneligibleRecipients(t *testing.T) {
	competitions := []ResultsCompetitionPage{
		{
			Session: rundown.CrewSession{ID: 1, Title: "Heat One"},
			Entries: []ResultsEntryPage{
				{Entry: competition.Entry{ID: 10, Name: "Alex"}},
			},
		},
	}

	t.Run("workspace entries alone", func(t *testing.T) {
		options := resultsEventEntries(competitions, nil)
		if len(options) != 1 || options[0].ID != 10 {
			t.Fatalf("unexpected options: %#v", options)
		}
	})

	t.Run("recipient no longer eligible keeps a labeled option", func(t *testing.T) {
		recipients := []results.AwardRecipient{
			{EntryID: 99, DisplayName: "Sam"},
		}
		options := resultsEventEntries(competitions, recipients)
		if len(options) != 2 {
			t.Fatalf("expected the ineligible recipient to be unioned in, got %#v", options)
		}
		found := false
		for _, option := range options {
			if option.ID == 99 && option.Label == "Sam (no longer an eligible, included Entry)" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing labeled fallback option for Entry 99: %#v", options)
		}
	})

	t.Run("recipient without a display name falls back to Entry number", func(t *testing.T) {
		recipients := []results.AwardRecipient{{EntryID: 42}}
		options := resultsEventEntries(competitions, recipients)
		found := false
		for _, option := range options {
			if option.ID == 42 && option.Label == "Entry #42 (no longer an eligible, included Entry)" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing Entry-number fallback label: %#v", options)
		}
	})

	t.Run("recipient already among workspace entries is not duplicated", func(t *testing.T) {
		recipients := []results.AwardRecipient{{EntryID: 10, DisplayName: "Alex"}}
		options := resultsEventEntries(competitions, recipients)
		if len(options) != 1 {
			t.Fatalf("expected no duplicate option for Entry 10, got %#v", options)
		}
	})
}

// TestResultsCompetitionEntriesRetainsIneligibleRecipients mirrors
// TestResultsEventEntriesRetainsIneligibleRecipients for the
// Competition-scoped Award recipient picker.
func TestResultsCompetitionEntriesRetainsIneligibleRecipients(t *testing.T) {
	competitions := []ResultsCompetitionPage{
		{
			Session: rundown.CrewSession{ID: 1, Title: "Heat One"},
			Entries: []ResultsEntryPage{
				{Entry: competition.Entry{ID: 10, Name: "Alex"}},
			},
		},
	}

	recipients := []results.AwardRecipient{{EntryID: 77, DisplayName: "Riley"}}
	options := resultsCompetitionEntries(competitions, 1, recipients)
	if len(options) != 2 {
		t.Fatalf("expected the ineligible recipient to be unioned in, got %#v", options)
	}

	if resultsCompetitionEntries(competitions, 404, recipients) != nil {
		t.Fatalf("expected nil options for an unknown Competition Session ID")
	}
}

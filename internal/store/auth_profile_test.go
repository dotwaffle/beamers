package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/resultspublication"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

// updateAccountProfileForTest exercises UpdateAccountProfile through its
// command transaction, committing on success like the real command path
// and rolling back on any rejection so fixtures stay reusable across cases.
func updateAccountProfileForTest(
	t *testing.T,
	ctx context.Context,
	installation *SQLite,
	params UpdateAccountProfileParams,
) error {
	t.Helper()
	transaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Profile update command: %v", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	if _, err = transaction.UpdateAccountProfile(ctx, params); err != nil {
		return err
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit Profile update command: %v", err)
	}
	return nil
}

func TestPublicProfileAcceptsOnlyReleasedEntries(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	profileOwner := client.Account.Create().
		SetName("Profile Owner").
		SetNormalizedName("profile-owner").
		SetAdministrator(false).
		SaveX(ctx)
	event := createSchemaTestEvent(t, client)
	competition := createPublishedResultsSession(
		t,
		client,
		event.ID,
		"Competition",
		"Old School",
	)
	entry := client.CompetitionEntry.Create().
		SetEventID(event.ID).
		SetCompetitionSessionID(competition.ID).
		SetName("Released Demo").
		SetDisposition("Included").
		SaveX(ctx)

	if err := updateAccountProfileForTest(t, ctx, installation, UpdateAccountProfileParams{
		AccountID: profileOwner.ID, AccountHandle: profileOwner.NormalizedName,
		DisplayName: "Profile Owner", Published: true, EntryIDs: []int{entry.ID},
	}); !errors.Is(err, ErrProfileEntryUnavailable) {
		t.Fatalf("unreleased Profile Entry error = %v, want %v", err, ErrProfileEntryUnavailable)
	}
	renderedJSON := `{"items":[{"competition":{"placed":[{"entry_id":` +
		strconv.Itoa(entry.ID) +
		`,"name":"Released Demo"}],"unplaced":[],"disqualified":[]}}]}`
	client.ResultsPublication.Create().
		SetEventID(event.ID).
		SetScope(resultspublication.ScopeEvent).
		SetScopeSessionID(event.ID).
		SetRevision(1).
		SetReleasePolicy(resultspublication.ReleasePolicyStandalone).
		SetStatus(resultspublication.StatusFinal).
		SetItems([]prizegivingvalue.ItemRef{}).
		SetRenderedJSON(renderedJSON).
		SetCreatedAt(time.Now()).
		SaveX(ctx)
	if err := upsertReleasedProfileEntries(ctx, client, renderedJSON); err != nil {
		t.Fatalf("project released Profile Entry: %v", err)
	}

	entries, err := installation.ReleasedProfileEntries(ctx)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("released Profile Entries = %+v, %v", entries, err)
	}
	if err = updateAccountProfileForTest(t, ctx, installation, UpdateAccountProfileParams{
		AccountID: profileOwner.ID, AccountHandle: profileOwner.NormalizedName,
		DisplayName: "Profile Owner", Published: true, EntryIDs: []int{entry.ID},
	}); err != nil {
		t.Fatalf("select released Profile Entry: %v", err)
	}
	profile, found, err := installation.PublicProfile(ctx, "profile-owner")
	if err != nil || !found || len(profile.Entries) != 1 ||
		profile.Entries[0].Name != "Released Demo" {
		t.Fatalf("Public Profile = %+v, found %t, %v", profile, found, err)
	}
}

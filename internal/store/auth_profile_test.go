package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/resultspublication"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
	"github.com/dotwaffle/beamers/internal/viewer"
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

func TestAccountAndProfilePrivacyMatrix(t *testing.T) {
	client := openEntTestClient(t)
	internalContext := systemContext(t.Context())
	administrator := client.Account.Create().
		SetName("Administrator").
		SetNormalizedName("administrator").
		SetAdministrator(true).
		SaveX(internalContext)
	client.PasswordCredential.Create().
		SetAccountID(administrator.ID).
		SetPasswordHash("fixture").
		SaveX(internalContext)
	registered := client.Account.Create().
		SetName("Private Person").
		SetNormalizedName("private-person").
		SetAdministrator(false).
		SaveX(internalContext)
	var err error
	ownerContext := viewer.NewContext(
		t.Context(),
		viewer.Identity{AccountID: registered.ID},
	)
	otherContext := viewer.NewContext(t.Context(), viewer.Identity{AccountID: administrator.ID})
	administratorContext := viewer.NewContext(
		t.Context(),
		viewer.Identity{AccountID: administrator.ID, Administrator: true},
	)
	profile := client.AccountProfile.Create().
		SetAccountID(registered.ID).
		SetNormalizedHandle("private-person").
		SetDisplayName("Private Person").
		SaveX(ownerContext)

	if _, err = profile.Update().
		SetDisplayName("Forbidden").
		Save(otherContext); !errors.Is(err, privacy.Deny) {
		t.Fatalf("cross-Account Profile update error = %v, want privacy denial", err)
	}
	if _, err = registered.Update().
		SetName("Mutable Name").
		Save(ownerContext); err != nil {
		t.Fatalf("owner Display Name update: %v", err)
	}
	if _, err = registered.Update().
		SetNormalizedName("mutable-handle").
		Save(ownerContext); !errors.Is(err, privacy.Deny) {
		t.Fatalf("owner Handle update error = %v, want privacy denial", err)
	}
	if _, err = profile.Update().SetPublished(true).Save(ownerContext); err != nil {
		t.Fatalf("publish owner Profile: %v", err)
	}

	client.RegistrationPolicy.Create().
		SetRegistrationOpen(false).
		SaveX(administratorContext)
	if _, err = client.RegistrationPolicy.Update().
		SetRegistrationOpen(true).
		Save(t.Context()); !errors.Is(err, privacy.Deny) {
		t.Fatalf("anonymous Registration Policy update error = %v, want privacy denial", err)
	}
	if _, err = client.Account.Create().
		SetName("Closed").
		SetNormalizedName("closed").
		SetAdministrator(false).
		Save(t.Context()); !errors.Is(err, privacy.Deny) {
		t.Fatalf("closed registration Account error = %v, want privacy denial", err)
	}
	if _, err = client.Account.Query().
		Where(account.IDEQ(registered.ID)).
		Only(otherContext); !errors.Is(err, privacy.Deny) {
		t.Fatalf("non-Administrator Account query error = %v, want privacy denial", err)
	}
	released := client.ReleasedProfileEntry.Create().
		SetEntryID(1).
		SetName("Released").
		SaveX(internalContext)
	if _, err = released.Update().SetName("Changed").Save(t.Context()); !errors.Is(err, privacy.Deny) {
		t.Fatalf("public released Profile Entry mutation error = %v, want privacy denial", err)
	}
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

package store

import (
	"testing"
	"time"
)

func TestSetFavoriteSessionIgnoresOnlyDuplicate(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	owner := client.Account.Create().
		SetName("Favorite Owner").
		SetNormalizedName("favorite-owner").
		SetAdministrator(false).
		SaveX(ctx)
	event := createSchemaTestEvent(t, client)
	session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)

	if err := installation.SetFavoriteSession(ctx, owner.ID, session.ID, true); err != nil {
		t.Fatalf("add Favorite Session: %v", err)
	}
	if err := installation.SetFavoriteSession(ctx, owner.ID, session.ID, true); err != nil {
		t.Fatalf("repeat Favorite Session: %v", err)
	}
	if err := installation.SetFavoriteSession(ctx, owner.ID+1, session.ID, true); err == nil {
		t.Fatal("invalid Favorite Session owner unexpectedly succeeded")
	}
}

func TestDisableAccountDetachesFavoriteSessions(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	owner := client.Account.Create().
		SetName("Favorite Owner").
		SetNormalizedName("favorite-owner").
		SetAdministrator(false).
		SaveX(ctx)
	event := createSchemaTestEvent(t, client)
	session := client.Session.Create().SetEventID(event.ID).SaveX(ctx)
	if err := installation.SetFavoriteSession(ctx, owner.ID, session.ID, true); err != nil {
		t.Fatalf("add Favorite Session: %v", err)
	}

	transaction, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin disable Account: %v", err)
	}
	if _, err = transaction.DisableAccount(ctx, owner.ID, time.Now()); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("disable Account: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		t.Fatalf("commit disable Account: %v", err)
	}
	favorites, err := installation.FavoriteSessionIDs(ctx, owner.ID)
	if err != nil {
		t.Fatalf("read disabled Account Favorites: %v", err)
	}
	if len(favorites) != 0 {
		t.Fatalf("disabled Account Favorites = %v, want none", favorites)
	}
}

package store

import (
	"context"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/favoritesession"
)

// FavoriteSessionIDs returns one Account's private Session bookmarks.
func (installation *SQLite) FavoriteSessionIDs(
	ctx context.Context,
	accountID int,
) ([]int, error) {
	found, err := installation.client.FavoriteSession.Query().
		Where(favoritesession.AccountIDEQ(accountID)).
		Select(favoritesession.FieldSessionID).
		Ints(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("read Favorite Sessions", err)
	}
	return found, nil
}

// SetFavoriteSession adds or removes one Account's Session bookmark.
func (installation *SQLite) SetFavoriteSession(
	ctx context.Context,
	accountID int,
	sessionID int,
	favorite bool,
) error {
	internalContext := systemContext(ctx)
	if !favorite {
		_, err := installation.client.FavoriteSession.Delete().
			Where(
				favoritesession.AccountIDEQ(accountID),
				favoritesession.SessionIDEQ(sessionID),
			).
			Exec(internalContext)
		if err != nil {
			return opaqueError("remove Favorite Session", err)
		}
		return nil
	}
	_, err := installation.client.FavoriteSession.Create().
		SetAccountID(accountID).
		SetSessionID(sessionID).
		Save(internalContext)
	if ent.IsConstraintError(err) {
		exists, existsErr := installation.client.FavoriteSession.Query().
			Where(
				favoritesession.AccountIDEQ(accountID),
				favoritesession.SessionIDEQ(sessionID),
			).
			Exist(internalContext)
		if existsErr != nil {
			return opaqueError("verify duplicate Favorite Session", existsErr)
		}
		if exists {
			return nil
		}
	}
	if err != nil {
		return opaqueError("add Favorite Session", err)
	}
	return nil
}

package store

import (
	"context"
	"time"

	"github.com/dotwaffle/beamers/ent"
)

// AuditEntry is the Administrator-readable projection of one durable action.
type AuditEntry struct {
	ID             int
	ActorAccountID int
	ActorName      string
	ServerTime     time.Time
	Action         string
	TargetType     string
	TargetID       string
	Outcome        string
	Reason         string
	Note           string
}

// ListAuditEntries returns installation-lifetime history in creation order.
func (installation *SQLite) ListAuditEntries(ctx context.Context) ([]AuditEntry, error) {
	found, err := installation.client.AuditEntry.Query().
		WithActor().
		Order(ent.Asc("id")).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Audit Entries", err)
	}
	entries := make([]AuditEntry, 0, len(found))
	for _, item := range found {
		actor, err := item.Edges.ActorOrErr()
		if err != nil {
			return nil, opaqueError("load Audit Entry actor", err)
		}
		entries = append(entries, AuditEntry{
			ID: item.ID, ActorAccountID: item.ActorAccountID, ActorName: actor.Name,
			ServerTime: item.CreatedAt, Action: item.Action,
			TargetType: item.TargetType, TargetID: item.TargetID,
			Outcome: item.Result.String(), Reason: item.Reason, Note: item.Note,
		})
	}
	return entries, nil
}

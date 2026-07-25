package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/auditentry"
)

// AuditEntry is the Administrator-readable projection of one durable action.
type AuditEntry struct {
	ID             int
	ActorKind      string
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

// RecordHostInterfaceMode appends a host-authorized mode transition.
func (installation *SQLite) RecordHostInterfaceMode(
	ctx context.Context,
	target string,
	enabled bool,
) error {
	if installation == nil || installation.client == nil {
		return ErrUninitialized
	}
	if target != "Crew" && target != "Display" {
		return errors.New("invalid insecure Interface target")
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	latest, err := installation.client.AuditEntry.Query().
		Where(
			auditentry.ActionEQ("ConfigureInsecure"+target),
			auditentry.TargetTypeEQ("Interface"),
			auditentry.TargetIDEQ(target),
		).
		Order(ent.Desc(auditentry.FieldID)).
		First(systemContext(ctx))
	switch {
	case ent.IsNotFound(err):
		if !enabled {
			return nil
		}
	case err != nil:
		return opaqueError("load insecure Interface Audit state", err)
	case latest.Note == state:
		return nil
	}
	if _, err = installation.client.AuditEntry.Create().
		SetCreatedAt(time.Now().UTC()).
		SetActorKind(auditentry.ActorKindHost).
		SetAction("ConfigureInsecure" + target).
		SetTargetType("Interface").
		SetTargetID(target).
		SetResult(auditentry.ResultSucceeded).
		SetNote(state).
		Save(systemContext(ctx)); err != nil {
		return opaqueError("record insecure Interface Audit state", err)
	}
	return nil
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
		actorAccountID := 0
		actorName := "Host"
		if item.ActorKind == "Account" {
			actor, actorErr := item.Edges.ActorOrErr()
			if actorErr != nil {
				return nil, opaqueError("load Audit Entry actor", actorErr)
			}
			actorName = actor.Name
			actorAccountID = item.ActorAccountID
		} else if item.ActorKind == "UploadLink" && item.ActorUploadLinkID != 0 {
			actorName = "Upload Link #" + strconv.Itoa(item.ActorUploadLinkID)
		}
		entries = append(entries, AuditEntry{
			ID: item.ID, ActorKind: item.ActorKind.String(),
			ActorAccountID: actorAccountID, ActorName: actorName,
			ServerTime: item.CreatedAt, Action: item.Action,
			TargetType: item.TargetType, TargetID: item.TargetID,
			Outcome: item.Result.String(), Reason: item.Reason, Note: item.Note,
		})
	}
	return entries, nil
}

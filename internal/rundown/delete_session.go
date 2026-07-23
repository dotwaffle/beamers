package rundown

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrDraftSessionDeletion means the Session is not eligible for permanent Draft deletion.
	ErrDraftSessionDeletion = store.ErrDraftSessionDeletion
	// ErrSessionNotFound means the Event has no Session with the requested identity.
	ErrSessionNotFound = store.ErrSessionNotFound
)

// DeleteDraftSessionInput is one permanent Draft-only Session deletion.
type DeleteDraftSessionInput struct {
	EventID               int    `json:"event_id"`
	SessionID             int    `json:"session_id"`
	CommandID             string `json:"command_id"`
	ExpectedDraftRevision int    `json:"expected_draft_revision"`
}

// DeleteDraftSessionResult identifies the deleted Session and new Draft revision.
type DeleteDraftSessionResult struct {
	DraftRevision int `json:"draft_revision"`
	SessionID     int `json:"session_id"`
}

type deleteDraftSessionOutcome struct {
	Result    *DeleteDraftSessionResult `json:"result,omitempty"`
	Rejection *rejection                `json:"rejection,omitempty"`
}

// DeleteDraftSession permanently removes one never-Published, unreferenced Draft Session.
func (commands *Commands) DeleteDraftSession(
	ctx context.Context,
	actor auth.Account,
	input DeleteDraftSessionInput,
) (DeleteDraftSessionResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return DeleteDraftSessionResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedDraftRevision < 0 {
		return DeleteDraftSessionResult{}, &ValidationError{Field: "delete_session", Message: "requires an Event, Session, and non-negative Draft revision"}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return DeleteDraftSessionResult{}, errors.New("encode Delete Draft Session command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "DeleteDraftSession",
		TargetType: "Session", TargetID: strconv.Itoa(input.SessionID), Now: commands.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[DeleteDraftSessionResult]{
		Storage: commands.storage, Identity: identity, Replay: decodeDeleteDraftSessionOutcome,
		Apply: func(transaction *store.CommandTx) (command.Execution[DeleteDraftSessionResult], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return deleteDraftSessionRejection(rejection{Code: "event_access_denied", Message: ErrEventAccessDenied.Error()})
			}
			stored, deleteErr := transaction.DeleteDraftSession(actor.Context(ctx), store.DeleteDraftSessionParams{
				EventID: input.EventID, SessionID: input.SessionID, ExpectedDraftRevision: input.ExpectedDraftRevision,
			})
			if deleteErr != nil {
				var code string
				switch {
				case errors.Is(deleteErr, ErrDraftRevisionConflict):
					code = "draft_revision_conflict"
				case errors.Is(deleteErr, ErrSessionNotFound):
					code = "session_not_found"
				case errors.Is(deleteErr, ErrDraftSessionDeletion):
					code = "draft_session_deletion"
				default:
					return command.Execution[DeleteDraftSessionResult]{}, deleteErr
				}
				return deleteDraftSessionRejection(rejection{Code: code, Message: deleteErr.Error()})
			}
			result := DeleteDraftSessionResult{DraftRevision: stored.DraftRevision, SessionID: stored.SessionID}
			encoded, encodeErr := json.Marshal(deleteDraftSessionOutcome{Result: &result})
			if encodeErr != nil {
				return command.Execution[DeleteDraftSessionResult]{}, errors.New("encode Delete Draft Session outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func deleteDraftSessionRejection(rejected rejection) (command.Execution[DeleteDraftSessionResult], error) {
	encoded, err := json.Marshal(deleteDraftSessionOutcome{Rejection: &rejected})
	if err != nil {
		return command.Execution[DeleteDraftSessionResult]{}, errors.New("encode rejected Delete Draft Session outcome")
	}
	return command.RejectEncoded(DeleteDraftSessionResult{}, string(encoded), deleteDraftSessionError(rejected)), nil
}

func decodeDeleteDraftSessionOutcome(encoded string) (DeleteDraftSessionResult, error) {
	var outcome deleteDraftSessionOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return DeleteDraftSessionResult{}, errors.New("decode Delete Draft Session Command Receipt")
	}
	if outcome.Rejection != nil {
		return DeleteDraftSessionResult{}, deleteDraftSessionError(*outcome.Rejection)
	}
	if outcome.Result == nil {
		return DeleteDraftSessionResult{}, errors.New("delete Draft Session Command Receipt has no outcome")
	}
	return *outcome.Result, nil
}

func deleteDraftSessionError(rejected rejection) error {
	switch rejected.Code {
	case "event_access_denied":
		return ErrEventAccessDenied
	case "draft_revision_conflict":
		return ErrDraftRevisionConflict
	case "session_not_found":
		return ErrSessionNotFound
	case "draft_session_deletion":
		return ErrDraftSessionDeletion
	default:
		return errors.New("delete Draft Session command was rejected")
	}
}

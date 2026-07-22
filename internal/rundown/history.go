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

// DraftHistoryInput identifies one revision-bound Discard or Revert command.
type DraftHistoryInput struct {
	EventID               int    `json:"event_id"`
	ExpectedDraftRevision int    `json:"expected_draft_revision"`
	CommandID             string `json:"command_id"`
	ChangeIDs             []int  `json:"change_ids"`
}

// DiscardDraftChanges restores selected effective facts to their prior values.
func (commands *Commands) DiscardDraftChanges(ctx context.Context, actor auth.Account, input DraftHistoryInput) (EditDraftResult, error) {
	return commands.changeDraftHistory(ctx, actor, "DiscardDraftChanges", input, false)
}

// RevertDraftChange appends an inverse of one historical fact change.
func (commands *Commands) RevertDraftChange(ctx context.Context, actor auth.Account, input DraftHistoryInput) (EditDraftResult, error) {
	return commands.changeDraftHistory(ctx, actor, "RevertDraftChange", input, true)
}

func (commands *Commands) changeDraftHistory(
	ctx context.Context,
	actor auth.Account,
	action string,
	input DraftHistoryInput,
	revert bool,
) (EditDraftResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return EditDraftResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	if input.EventID <= 0 || input.ExpectedDraftRevision < 0 || len(input.ChangeIDs) == 0 {
		return EditDraftResult{}, &ValidationError{Field: "history", Message: "requires an Event, revision, and change selection"}
	}
	if revert && len(input.ChangeIDs) != 1 {
		return EditDraftResult{}, &ValidationError{Field: "change_id", Message: "must identify one change"}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EditDraftResult{}, errors.New("encode Draft history command")
	}
	identity := store.CommandIdentity{ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: action, TargetType: "Event",
		TargetID: strconv.Itoa(input.EventID), Now: commands.now().UTC()}
	return command.Execute(actor.Context(ctx), command.Plan[EditDraftResult]{
		Storage: commands.storage, Identity: identity,
		Replay: func(original string) (EditDraftResult, error) {
			var result EditDraftResult
			if decodeErr := json.Unmarshal([]byte(original), &result); decodeErr != nil {
				return EditDraftResult{}, errors.New("decode Draft history outcome")
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EditDraftResult], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[EditDraftResult]{}, ErrEventAccessDenied
			}
			params := store.DraftHistoryParams{EventID: input.EventID, ActorAccountID: actor.ID,
				ExpectedDraftRevision: input.ExpectedDraftRevision, ChangeIDs: input.ChangeIDs, Now: identity.Now}
			var stored store.EditDraftResult
			var historyErr error
			if revert {
				stored, historyErr = transaction.RevertDraftChange(actor.Context(ctx), params)
			} else {
				stored, historyErr = transaction.DiscardDraftChanges(actor.Context(ctx), params)
			}
			if historyErr != nil {
				return command.Execution[EditDraftResult]{}, historyErr
			}
			result := editDraftResult(stored)
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[EditDraftResult]{}, errors.New("encode Draft history outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

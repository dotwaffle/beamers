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
	transaction, err := commands.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return EditDraftResult{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	original, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return EditDraftResult{}, commitErr
		}
		return EditDraftResult{}, ErrCommandConflict
	}
	if err != nil {
		return EditDraftResult{}, err
	}
	if retry {
		var result EditDraftResult
		if err = json.Unmarshal([]byte(original), &result); err != nil {
			return EditDraftResult{}, errors.New("decode Draft history outcome")
		}
		return result, nil
	}
	if !actor.CanProduceEvent(input.EventID) {
		return EditDraftResult{}, ErrEventAccessDenied
	}
	params := store.DraftHistoryParams{EventID: input.EventID, ActorAccountID: actor.ID,
		ExpectedDraftRevision: input.ExpectedDraftRevision, ChangeIDs: input.ChangeIDs, Now: identity.Now}
	var stored store.EditDraftResult
	if revert {
		stored, err = transaction.RevertDraftChange(actor.Context(ctx), params)
	} else {
		stored, err = transaction.DiscardDraftChanges(actor.Context(ctx), params)
	}
	if err != nil {
		return EditDraftResult{}, err
	}
	result := editDraftResult(stored)
	encoded, err := json.Marshal(result)
	if err != nil {
		return EditDraftResult{}, errors.New("encode Draft history outcome")
	}
	if recordErr := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); recordErr != nil {
		return EditDraftResult{}, recordErr
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		return EditDraftResult{}, commitErr
	}
	return result, nil
}

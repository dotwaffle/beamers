package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// EventAwardPathState records review of one effective release-path revision.
type EventAwardPathState struct {
	ReleasePath      AwardReleasePath
	Revision         int
	Ready            bool
	ReadyByAccountID int
	ReadyAt          time.Time
}

// EventAwardsDraft is one versioned Event Awards proposal and its path reviews.
type EventAwardsDraft struct {
	ID                 int
	EventID            int
	Revision           int
	Awards             []EventAward
	PathStates         []EventAwardPathState
	CreatedByAccountID int
	CreatedAt          time.Time
}

// SaveEventAwardsInput replaces one Event's complete Award assignment snapshot.
type SaveEventAwardsInput struct {
	EventID          int          `json:"event_id"`
	CommandID        string       `json:"command_id"`
	ExpectedRevision int          `json:"expected_revision"`
	Awards           []EventAward `json:"awards"`
}

// MarkEventAwardsReadyInput identifies one exact path revision for review.
type MarkEventAwardsReadyInput struct {
	EventID              int              `json:"event_id"`
	CommandID            string           `json:"command_id"`
	ExpectedRevision     int              `json:"expected_revision"`
	ReleasePath          AwardReleasePath `json:"release_path"`
	ExpectedPathRevision int              `json:"expected_path_revision"`
}

// GetEventAwards returns the current Event Awards Draft to authorized crew.
func (service *Service) GetEventAwards(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (EventAwardsDraft, error) {
	if eventID <= 0 {
		return EventAwardsDraft{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return EventAwardsDraft{}, ErrViewRequired
	}
	found, err := service.storage.LoadEventAwardsDraft(actor.Context(ctx), eventID)
	if err != nil {
		return EventAwardsDraft{}, err
	}
	return eventAwardsDraft(found), nil
}

// SaveEventAwards appends one complete Event Awards snapshot.
func (service *Service) SaveEventAwards(
	ctx context.Context,
	actor auth.Account,
	input SaveEventAwardsInput,
) (EventAwardsDraft, error) {
	if err := validateSaveEventAwardsInput(input); err != nil {
		return EventAwardsDraft{}, err
	}
	if !actor.HasCapability(input.EventID, viewer.ManageResults) {
		return EventAwardsDraft{}, ErrManageRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EventAwardsDraft{}, errors.New("encode Event Awards command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "SaveEventAwardsDraft",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EventAwardsDraft]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: resultsRejections,
		},
		Replay: func(outcome string) (EventAwardsDraft, error) {
			var stored store.EventAwardsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return EventAwardsDraft{}, err
			}
			return eventAwardsDraft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EventAwardsDraft], error) {
			stored, saveErr := transaction.SaveEventAwardsDraft(
				actor.Context(ctx), store.SaveEventAwardsDraftParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					CreatedByAccountID: actor.ID, Now: identity.Now,
					Awards: eventAwardInputs(input.Awards),
				},
			)
			if saveErr != nil {
				return command.Execution[EventAwardsDraft]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[EventAwardsDraft]{}, errors.New("encode Event Awards outcome")
			}
			return command.Success(eventAwardsDraft(stored), string(outcome)), nil
		},
	})
}

// MarkEventAwardsReady records Producer review of one exact release path.
func (service *Service) MarkEventAwardsReady(
	ctx context.Context,
	actor auth.Account,
	input MarkEventAwardsReadyInput,
) (EventAwardsDraft, error) {
	if err := validateMarkEventAwardsReadyInput(input); err != nil {
		return EventAwardsDraft{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return EventAwardsDraft{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EventAwardsDraft{}, errors.New("encode Event Awards review command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "MarkEventAwardsReady",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EventAwardsDraft]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: resultsRejections,
		},
		Replay: func(outcome string) (EventAwardsDraft, error) {
			var stored store.EventAwardsDraft
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return EventAwardsDraft{}, err
			}
			return eventAwardsDraft(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[EventAwardsDraft], error) {
			stored, markErr := transaction.MarkEventAwardsReady(
				actor.Context(ctx), store.MarkEventAwardsReadyParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					ReleasePath:          awardReleasePathInput(input.ReleasePath),
					ExpectedPathRevision: input.ExpectedPathRevision,
					ReviewedByAccountID:  actor.ID, Now: identity.Now,
				},
			)
			if markErr != nil {
				return command.Execution[EventAwardsDraft]{}, markErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[EventAwardsDraft]{}, errors.New("encode Event Awards review outcome")
			}
			return command.Success(eventAwardsDraft(stored), string(outcome)), nil
		},
	})
}

func validateSaveEventAwardsInput(input SaveEventAwardsInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.ExpectedRevision < 0 || len(input.Awards) > 1000 {
		return ErrInvalidInput
	}
	return ValidateEventAwards(input.Awards)
}

func validateMarkEventAwardsReadyInput(input MarkEventAwardsReadyInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 || input.ExpectedRevision <= 0 ||
		input.ExpectedPathRevision <= 0 || !validAwardPath(input.ReleasePath) {
		return ErrInvalidInput
	}
	return nil
}

func eventAwardInputs(values []EventAward) []store.EventAwardInput {
	awards := make([]store.EventAwardInput, 0, len(values))
	for _, value := range values {
		awards = append(awards, store.EventAwardInput{
			Key: value.Key, Name: value.Name, DisplayOrder: value.DisplayOrder,
			Recipients:  awardRecipientInputs(value.Recipients),
			ReleasePath: awardReleasePathInput(value.ReleasePath),
		})
	}
	return awards
}

func awardReleasePathInput(value AwardReleasePath) store.AwardReleasePath {
	return store.AwardReleasePath{
		Kind: string(value.Kind), PrizegivingSessionID: value.PrizegivingSessionID,
	}
}

func awardReleasePath(value store.AwardReleasePath) AwardReleasePath {
	return AwardReleasePath{
		Kind:                 AwardReleasePathKind(value.Kind),
		PrizegivingSessionID: value.PrizegivingSessionID,
	}
}

func eventAwardsDraft(stored store.EventAwardsDraft) EventAwardsDraft {
	result := EventAwardsDraft{
		ID: stored.ID, EventID: stored.EventID, Revision: stored.Revision,
		CreatedByAccountID: stored.CreatedByAccountID, CreatedAt: stored.CreatedAt,
		Awards:     make([]EventAward, 0, len(stored.Awards)),
		PathStates: make([]EventAwardPathState, 0, len(stored.PathStates)),
	}
	for _, award := range stored.Awards {
		result.Awards = append(result.Awards, EventAward{
			Award: Award{
				Key: award.Key, Name: award.Name, DisplayOrder: award.DisplayOrder,
				Recipients: awardRecipients(award.Recipients),
			},
			ReleasePath: awardReleasePath(award.ReleasePath),
		})
	}
	for _, state := range stored.PathStates {
		result.PathStates = append(result.PathStates, EventAwardPathState{
			ReleasePath: awardReleasePath(state.ReleasePath), Revision: state.Revision,
			Ready: state.Ready, ReadyByAccountID: state.ReadyByAccountID,
			ReadyAt: state.ReadyAt,
		})
	}
	return result
}

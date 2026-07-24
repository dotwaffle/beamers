package results

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
	// ErrResultsReleasePolicy means a release command does not match the locked policy.
	ErrResultsReleasePolicy = errors.New("results release policy does not permit this command")
	// ErrResultsPublicationRequired means a release command found no releasable result set.
	ErrResultsPublicationRequired = errors.New("results publication did not advance")
)

// FirePrizegivingResultsCueInput identifies one explicit All at Cue release.
type FirePrizegivingResultsCueInput struct {
	EventID           int    `json:"event_id"`
	CeremonySessionID int    `json:"ceremony_session_id"`
	CommandID         string `json:"command_id"`
}

// FirePrizegivingResultsCue atomically publishes the complete locked set.
func (service *Service) FirePrizegivingResultsCue(
	ctx context.Context,
	actor auth.Account,
	input FirePrizegivingResultsCueInput,
) (Publication, error) {
	if input.EventID <= 0 || input.CeremonySessionID <= 0 {
		return Publication{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Publication{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Publication{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Publication{}, errors.New("encode Results release cue command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "FirePrizegivingResultsCue",
		TargetType:     "Session",
		TargetID:       strconv.Itoa(input.CeremonySessionID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Publication]{
		Storage:  service.storage,
		Identity: identity,
		Replay: func(outcome string) (Publication, error) {
			var result Publication
			if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
				return Publication{}, err
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Publication], error) {
			plan, loadErr := transaction.LoadPrizegivingPlan(
				actor.Context(ctx),
				input.EventID,
				input.CeremonySessionID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			if !plan.Locked {
				return command.Execution[Publication]{}, ErrPrizegivingPreflightRequired
			}
			if plan.ReleasePolicy != ResultsAllAtCue {
				return command.Execution[Publication]{}, ErrResultsReleasePolicy
			}
			current, loadErr := transaction.LoadResultsPublication(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationPrizegiving,
				input.CeremonySessionID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			next, changed, advanceErr := AdvancePublication(PublicationInput{
				Policy:   plan.ReleasePolicy,
				Order:    prizegivingItemRefs(plan.Lock.PublicationOrder),
				Current:  publicationFromStore(current),
				CueFired: true,
			})
			if advanceErr != nil {
				return command.Execution[Publication]{}, advanceErr
			}
			if changed {
				stored, appendErr := transaction.AppendResultsPublication(
					actor.Context(ctx),
					store.AppendResultsPublicationParams{
						EventID:            input.EventID,
						Scope:              store.ResultsPublicationPrizegiving,
						ScopeSessionID:     input.CeremonySessionID,
						ExpectedRevision:   current.Revision,
						Policy:             plan.ReleasePolicy,
						Status:             store.ResultsPublicationStatus(next.Status),
						Items:              prizegivingItemRefInputs(next.Items),
						Lock:               plan.Lock,
						CreatedByAccountID: actor.ID,
						Now:                identity.Now,
					},
				)
				if appendErr != nil {
					return command.Execution[Publication]{}, appendErr
				}
				next = publicationFromStore(stored)
			}
			outcome, marshalErr := json.Marshal(next)
			if marshalErr != nil {
				return command.Execution[Publication]{}, errors.New(
					"encode Results release cue outcome",
				)
			}
			return command.Success(next, string(outcome)), nil
		},
	})
}

func publicationFromStore(value store.ResultsPublication) Publication {
	return Publication{
		Revision: value.Revision,
		Status:   PublicationStatus(value.Status),
		Items:    prizegivingItemRefs(value.Items),
	}
}

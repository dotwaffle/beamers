package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

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

// ReleaseStandaloneResultsInput identifies one unassigned Competition release.
type ReleaseStandaloneResultsInput struct {
	EventID              int    `json:"event_id"`
	CompetitionSessionID int    `json:"competition_session_id"`
	CommandID            string `json:"command_id"`
}

// PrizegivingPublicationTrigger contains one durable release trigger.
type PrizegivingPublicationTrigger struct {
	CueFired      bool
	CeremonyEnded bool
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
			next, _, advanceErr := AdvancePrizegivingPublication(
				actor.Context(ctx),
				actor,
				transaction,
				input.EventID,
				input.CeremonySessionID,
				identity.Now,
				store.ProgramChannelState{},
				PrizegivingPublicationTrigger{CueFired: true},
			)
			if advanceErr != nil {
				return command.Execution[Publication]{}, advanceErr
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

// ReleaseStandaloneResults publishes one exact reviewed unassigned Competition.
func (service *Service) ReleaseStandaloneResults(
	ctx context.Context,
	actor auth.Account,
	input ReleaseStandaloneResultsInput,
) (Publication, error) {
	if input.EventID <= 0 || input.CompetitionSessionID <= 0 {
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
		return Publication{}, errors.New("encode standalone Results release command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "ReleaseStandaloneResults",
		TargetType:     "Competition",
		TargetID:       strconv.Itoa(input.CompetitionSessionID),
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
			current, loadErr := transaction.LoadResultsPublication(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationStandalone,
				input.CompetitionSessionID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			if current.Status == store.ResultsPublicationFinal {
				return publicationExecution(publicationFromStore(current))
			}
			state, loadErr := transaction.LoadStandaloneResultsReleaseState(
				actor.Context(ctx),
				input.EventID,
				input.CompetitionSessionID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			draft := state.Draft
			if draft.Revision == 0 || state.ResolutionRequired {
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			kind := ResultItemCompetition
			switch Disposition(draft.Disposition) {
			case Publish:
				if !draft.Ready {
					return command.Execution[Publication]{}, ErrResultsPublicationRequired
				}
			case NoPublicResults:
				kind = ResultItemNoPublicResults
			default:
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			ref := ResultItemRef{
				Kind:                 kind,
				CompetitionSessionID: input.CompetitionSessionID,
				DisplayOrder:         1,
			}
			next, changed, advanceErr := AdvancePublication(PublicationInput{
				Policy: ResultsStandalone,
				Order:  []ResultItemRef{ref},
				States: []ResultItemStageState{{
					Ref: ref, Status: ResultItemRevealed, Release: ResultReleaseReady,
				}},
				Current:           publicationFromStore(current),
				StandaloneRelease: true,
			})
			if advanceErr != nil {
				return command.Execution[Publication]{}, advanceErr
			}
			if !changed {
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			storedRef := prizegivingItemRefInputs([]ResultItemRef{ref})[0]
			stored, appendErr := transaction.AppendResultsPublication(
				actor.Context(ctx),
				store.AppendResultsPublicationParams{
					EventID:          input.EventID,
					Scope:            store.ResultsPublicationStandalone,
					ScopeSessionID:   input.CompetitionSessionID,
					ExpectedRevision: current.Revision,
					Policy:           ResultsStandalone,
					Status:           store.ResultsPublicationStatus(next.Status),
					Items:            []store.PrizegivingResultItemRef{storedRef},
					Lock: store.PrizegivingPreflightLock{
						ReleasePolicy:    ResultsStandalone,
						PublicationOrder: []store.PrizegivingResultItemRef{storedRef},
						CompetitionSources: []store.PrizegivingCompetitionLock{{
							SessionID: input.CompetitionSessionID,
							DraftID:   draft.ID, DraftRevision: draft.Revision,
							Disposition: draft.Disposition,
						}},
					},
					CreatedByAccountID: actor.ID,
					Now:                identity.Now,
				},
			)
			if appendErr != nil {
				return command.Execution[Publication]{}, appendErr
			}
			return publicationExecution(publicationFromStore(stored))
		},
	})
}

func publicationExecution(
	value Publication,
) (command.Execution[Publication], error) {
	outcome, err := json.Marshal(value)
	if err != nil {
		return command.Execution[Publication]{}, errors.New(
			"encode Results Publication outcome",
		)
	}
	return command.Success(value, string(outcome)), nil
}

// AdvancePrizegivingPublication appends one policy-valid manifest revision.
func AdvancePrizegivingPublication(
	ctx context.Context,
	actor auth.Account,
	transaction *store.CommandTx,
	eventID, ceremonySessionID int,
	now time.Time,
	channel store.ProgramChannelState,
	trigger PrizegivingPublicationTrigger,
) (Publication, bool, error) {
	plan, err := transaction.LoadPrizegivingPlan(
		ctx,
		eventID,
		ceremonySessionID,
	)
	if errors.Is(err, store.ErrPrizegivingSession) && trigger.CeremonyEnded {
		return Publication{}, false, nil
	}
	if err != nil {
		return Publication{}, false, err
	}
	if !plan.Locked {
		return Publication{}, false, ErrPrizegivingPreflightRequired
	}
	if trigger.CueFired && plan.ReleasePolicy != ResultsAllAtCue {
		return Publication{}, false, ErrResultsReleasePolicy
	}
	current, err := transaction.LoadResultsPublication(
		ctx,
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonySessionID,
	)
	if err != nil {
		return Publication{}, false, err
	}
	next, changed, err := AdvancePublication(PublicationInput{
		Policy:        plan.ReleasePolicy,
		Order:         prizegivingItemRefs(plan.Lock.PublicationOrder),
		States:        publicationStageStates(channel.Items),
		Current:       publicationFromStore(current),
		CueFired:      trigger.CueFired,
		CeremonyEnded: trigger.CeremonyEnded,
	})
	if err != nil || !changed {
		return next, changed, err
	}
	stored, err := transaction.AppendResultsPublication(
		ctx,
		store.AppendResultsPublicationParams{
			EventID:            eventID,
			Scope:              store.ResultsPublicationPrizegiving,
			ScopeSessionID:     ceremonySessionID,
			ExpectedRevision:   current.Revision,
			Policy:             plan.ReleasePolicy,
			Status:             store.ResultsPublicationStatus(next.Status),
			Items:              prizegivingItemRefInputs(next.Items),
			Lock:               plan.Lock,
			CreatedByAccountID: actor.ID,
			Now:                now,
		},
	)
	if err != nil {
		return Publication{}, false, err
	}
	return publicationFromStore(stored), true, nil
}

func publicationStageStates(items []store.ProgramItem) []ResultItemStageState {
	states := make([]ResultItemStageState, 0, len(items))
	for _, item := range items {
		if item.Result == nil {
			continue
		}
		states = append(states, ResultItemStageState{
			Ref: ResultItemRef{
				Kind:                 ResultItemKind(item.Result.Ref.Kind),
				CompetitionSessionID: item.Result.Ref.CompetitionSessionID,
				AwardKey:             item.Result.Ref.AwardKey,
				DisplayOrder:         item.Result.Ref.DisplayOrder,
			},
			Status:            item.Result.Status,
			Release:           item.Result.Release,
			TakenAt:           item.Result.TakenAt,
			RevealStartedAt:   item.Result.RevealStartedAt,
			RevealDuration:    item.Result.RevealDuration,
			RevealCompletedAt: item.Result.RevealCompletedAt,
			SkippedAt:         item.Result.SkippedAt,
		})
	}
	return states
}

func publicationFromStore(value store.ResultsPublication) Publication {
	return Publication{
		Revision: value.Revision,
		Status:   PublicationStatus(value.Status),
		Items:    prizegivingItemRefs(value.Items),
	}
}

package results

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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

// StandaloneEventAwardsPreflight binds one exact reviewed standalone Award set.
type StandaloneEventAwardsPreflight struct {
	Lock     PrizegivingPreflightLock      `json:"lock"`
	Findings []PrizegivingPreflightFinding `json:"findings"`
}

// ReleaseStandaloneEventAwardsInput identifies one exact reviewed Award set.
type ReleaseStandaloneEventAwardsInput struct {
	EventID               int    `json:"event_id"`
	CommandID             string `json:"command_id"`
	ExpectedDraftRevision int    `json:"expected_draft_revision"`
	ExpectedPathRevision  int    `json:"expected_path_revision"`
}

type prizegivingPublicationTrigger struct {
	CueFired      bool
	CeremonyEnded bool
	Reconcile     bool
}

// ReconcilePrizegivingPublicationInput identifies one progressive live-state pass.
type ReconcilePrizegivingPublicationInput struct {
	EventID           int
	CeremonySessionID int
	Now               time.Time
}

// CompletePrizegivingPublicationInput identifies one ceremony completion.
type CompletePrizegivingPublicationInput struct {
	EventID           int
	CeremonySessionID int
	Now               time.Time
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
		Replay:   decodeResultsCommandReceipt[Publication],
		Apply: auditResultsRejections(func(
			transaction *store.CommandTx,
		) (command.Execution[Publication], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Publication]{}, ErrProducerRequired
			}
			next, _, advanceErr := service.advancePrizegivingPublication(
				actor.Context(ctx),
				actor,
				transaction,
				input.EventID,
				input.CeremonySessionID,
				identity.Now,
				prizegivingPublicationTrigger{CueFired: true},
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
		}),
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
		Replay:   decodeResultsCommandReceipt[Publication],
		Apply: auditResultsRejections(func(
			transaction *store.CommandTx,
		) (command.Execution[Publication], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Publication]{}, ErrProducerRequired
			}
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
			storedRef := prizegivingItemRefInputs([]ResultItemRef{ref})[0]
			lock := store.PrizegivingPreflightLock{
				ReleasePolicy:    ResultsStandalone,
				PublicationOrder: []store.PrizegivingResultItemRef{storedRef},
				CompetitionSources: []store.PrizegivingCompetitionLock{{
					SessionID: input.CompetitionSessionID,
					DraftID:   draft.ID, DraftRevision: draft.Revision,
					Disposition: draft.Disposition,
				}},
				Template: prizegivingTemplateInput(DefaultResultsTextTemplate()),
			}
			published, changed, publishErr := publishResultsPublication(
				actor.Context(ctx),
				transaction,
				publicationPipelineInput{
					EventID:        input.EventID,
					Scope:          store.ResultsPublicationStandalone,
					ScopeSessionID: input.CompetitionSessionID,
					Current:        current,
					Policy:         ResultsStandalone,
					Lock:           lock,
					Advance: PublicationInput{
						Policy: ResultsStandalone,
						Order:  []ResultItemRef{ref},
						States: []ResultItemStageState{{
							Ref: ref, Status: ResultItemRevealed,
							Release: ResultReleaseReady,
						}},
						StandaloneRelease: true,
					},
					ActorAccountID: actor.ID,
					Now:            identity.Now,
				},
			)
			if publishErr != nil {
				return command.Execution[Publication]{}, publishErr
			}
			if !changed {
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			return publicationExecution(published)
		}),
	})
}

// PreflightStandaloneEventAwards reports blockers without changing release state.
func (service *Service) PreflightStandaloneEventAwards(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (StandaloneEventAwardsPreflight, error) {
	if eventID <= 0 {
		return StandaloneEventAwardsPreflight{}, ErrInvalidInput
	}
	if !actor.CanProduceEvent(eventID) {
		return StandaloneEventAwardsPreflight{}, ErrProducerRequired
	}
	draft, err := service.storage.LoadEventAwardsDraft(actor.Context(ctx), eventID)
	if err != nil {
		return StandaloneEventAwardsPreflight{}, err
	}
	lock, ready := standaloneEventAwardsLock(eventAwardsDraft(draft))
	result := StandaloneEventAwardsPreflight{Lock: lock}
	if len(lock.PublicationOrder) == 0 {
		result.Findings = append(result.Findings, prizegivingFinding(
			"event_awards_missing",
			"No Event Awards are assigned to standalone release",
		))
	} else if !ready {
		result.Findings = append(result.Findings, prizegivingFinding(
			"event_awards_not_ready",
			"Standalone Event Awards lack Ready review",
		))
	}
	return result, nil
}

// ReleaseStandaloneEventAwards publishes one exact reviewed standalone Award set.
func (service *Service) ReleaseStandaloneEventAwards(
	ctx context.Context,
	actor auth.Account,
	input ReleaseStandaloneEventAwardsInput,
) (Publication, error) {
	if input.EventID <= 0 ||
		input.ExpectedDraftRevision <= 0 ||
		input.ExpectedPathRevision <= 0 {
		return Publication{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Publication{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Publication{}, errors.New("encode standalone Event Awards release command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "ReleaseStandaloneEventAwards",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[Publication]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   decodeResultsCommandReceipt[Publication],
		Apply: auditResultsRejections(func(
			transaction *store.CommandTx,
		) (command.Execution[Publication], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Publication]{}, ErrProducerRequired
			}
			current, loadErr := transaction.LoadResultsPublication(
				actor.Context(ctx),
				input.EventID,
				store.ResultsPublicationEventAwards,
				input.EventID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			if current.Status == store.ResultsPublicationFinal {
				return publicationExecution(publicationFromStore(current))
			}
			draft, loadErr := transaction.LoadEventAwardsDraft(
				actor.Context(ctx),
				input.EventID,
			)
			if loadErr != nil {
				return command.Execution[Publication]{}, loadErr
			}
			lock, ready := standaloneEventAwardsLock(eventAwardsDraft(draft))
			if draft.Revision != input.ExpectedDraftRevision ||
				lock.EventAwardsPathRevision != input.ExpectedPathRevision {
				return command.Execution[Publication]{}, ErrEventAwardsRevision
			}
			if !ready || len(lock.PublicationOrder) == 0 {
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			states := make(
				[]ResultItemStageState,
				0,
				len(lock.PublicationOrder),
			)
			for _, ref := range lock.PublicationOrder {
				states = append(states, ResultItemStageState{
					Ref: ref, Status: ResultItemRevealed, Release: ResultReleaseReady,
				})
			}
			lockInput := prizegivingLockInput(lock)
			published, changed, publishErr := publishResultsPublication(
				actor.Context(ctx),
				transaction,
				publicationPipelineInput{
					EventID:        input.EventID,
					Scope:          store.ResultsPublicationEventAwards,
					ScopeSessionID: input.EventID,
					Current:        current,
					Policy:         ResultsStandalone,
					Lock:           lockInput,
					Advance: PublicationInput{
						Policy: ResultsStandalone,
						Order:  lock.PublicationOrder,
						States: states, StandaloneRelease: true,
					},
					ActorAccountID: actor.ID,
					Now:            identity.Now,
				},
			)
			if publishErr != nil {
				return command.Execution[Publication]{}, publishErr
			}
			if !changed {
				return command.Execution[Publication]{}, ErrResultsPublicationRequired
			}
			return publicationExecution(published)
		}),
	})
}

func standaloneEventAwardsLock(
	draft EventAwardsDraft,
) (PrizegivingPreflightLock, bool) {
	lock := PrizegivingPreflightLock{
		EventID: draft.EventID, ReleasePolicy: ResultsStandalone,
		EventAwardsDraftRevision: draft.Revision,
		Template:                 DefaultResultsTextTemplate(),
	}
	ready := false
	for _, state := range draft.PathStates {
		if state.ReleasePath.Kind == StandaloneRelease {
			lock.EventAwardsPathRevision = state.Revision
			ready = state.Ready
			break
		}
	}
	for _, award := range draft.Awards {
		if award.ReleasePath.Kind != StandaloneRelease {
			continue
		}
		lock.PublicationOrder = append(lock.PublicationOrder, ResultItemRef{
			Kind: ResultItemEventAward, AwardKey: award.Key,
			DisplayOrder: award.DisplayOrder,
		})
	}
	return lock, ready
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

type publicationPipelineInput struct {
	EventID                   int
	Scope                     store.ResultsPublicationScope
	ScopeSessionID            int
	Current                   store.ResultsPublication
	Policy                    ReleasePolicy
	Lock                      store.PrizegivingPreflightLock
	Advance                   PublicationInput
	Correction                *CorrectionProposal
	ResultsCorrectionRevision int
	ActorAccountID            int
	Now                       time.Time
}

// ReconcilePrizegivingPublication applies one progressive live-state transition
// inside its caller's command transaction.
func (service *Service) ReconcilePrizegivingPublication(
	ctx context.Context,
	actor auth.Account,
	transaction *store.CommandTx,
	input ReconcilePrizegivingPublicationInput,
) (Publication, bool, error) {
	if transaction == nil || input.EventID <= 0 || input.CeremonySessionID <= 0 {
		return Publication{}, false, ErrInvalidInput
	}
	return service.advancePrizegivingPublication(
		ctx,
		actor,
		transaction,
		input.EventID,
		input.CeremonySessionID,
		input.Now,
		prizegivingPublicationTrigger{Reconcile: true},
	)
}

// CompletePrizegivingPublication applies ceremony completion inside its
// caller's command transaction.
func (service *Service) CompletePrizegivingPublication(
	ctx context.Context,
	actor auth.Account,
	transaction *store.CommandTx,
	input CompletePrizegivingPublicationInput,
) (Publication, bool, error) {
	if transaction == nil || input.EventID <= 0 || input.CeremonySessionID <= 0 {
		return Publication{}, false, ErrInvalidInput
	}
	return service.advancePrizegivingPublication(
		ctx,
		actor,
		transaction,
		input.EventID,
		input.CeremonySessionID,
		input.Now,
		prizegivingPublicationTrigger{CeremonyEnded: true},
	)
}

func (service *Service) advancePrizegivingPublication(
	ctx context.Context,
	actor auth.Account,
	transaction *store.CommandTx,
	eventID, ceremonySessionID int,
	now time.Time,
	trigger prizegivingPublicationTrigger,
) (Publication, bool, error) {
	plan, err := transaction.LoadPrizegivingPlan(
		ctx,
		eventID,
		ceremonySessionID,
	)
	if errors.Is(err, store.ErrPrizegivingSession) &&
		(trigger.CeremonyEnded || trigger.Reconcile) {
		return Publication{}, false, nil
	}
	if err != nil {
		return Publication{}, false, err
	}
	if trigger.Reconcile &&
		(!plan.Locked || plan.ReleasePolicy != ResultsProgressiveOnReveal) {
		return Publication{}, false, nil
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
	var states []ResultItemStageState
	if !trigger.CueFired {
		channel, loadErr := transaction.LoadProgramChannelAt(
			ctx,
			eventID,
			ceremonySessionID,
			now,
		)
		if loadErr != nil {
			return Publication{}, false, loadErr
		}
		states = PrizegivingPublicationStates(channel.Items)
	}
	publicationOrder := plan.Lock.PublicationOrder
	releaseLock := plan.Lock
	if current.Revision > 0 {
		publicationOrder = current.Lock.PublicationOrder
		releaseLock = current.Lock
	}
	return publishResultsPublication(
		ctx,
		transaction,
		publicationPipelineInput{
			EventID:        eventID,
			Scope:          store.ResultsPublicationPrizegiving,
			ScopeSessionID: ceremonySessionID,
			Current:        current,
			Policy:         plan.ReleasePolicy,
			Lock:           releaseLock,
			Advance: PublicationInput{
				Policy:        plan.ReleasePolicy,
				Order:         prizegivingItemRefs(publicationOrder),
				States:        states,
				CueFired:      trigger.CueFired,
				CeremonyEnded: trigger.CeremonyEnded,
			},
			ActorAccountID: actor.ID,
			Now:            now,
		},
	)
}

func publishResultsPublication(
	ctx context.Context,
	transaction *store.CommandTx,
	input publicationPipelineInput,
) (Publication, bool, error) {
	current := publicationFromStore(input.Current)
	var (
		next     Publication
		rendered RenderedPublicResults
		changed  bool
		err      error
	)
	lock := input.Lock
	if input.Correction != nil {
		var currentModel PublicResultsPublication
		if json.Unmarshal([]byte(input.Current.RenderedJSON), &currentModel) != nil {
			return Publication{}, false, ErrCorrectionBase
		}
		value, buildErr := BuildCorrectedResultsPublication(
			currentModel,
			current.Items,
			*input.Correction,
			input.Now,
		)
		if buildErr != nil {
			return Publication{}, false, buildErr
		}
		next = Publication{
			Revision: input.Current.Revision + 1,
			Status:   current.Status,
			Items:    input.Correction.PublicationOrder,
		}
		rendered, err = RenderPublicResults(value, input.Correction.Template)
	} else {
		input.Advance.Current = current
		next, changed, err = AdvancePublication(input.Advance)
		if err != nil || !changed {
			return next, changed, err
		}
		lock, err = freezeResultsRenderSource(
			ctx,
			transaction,
			input.EventID,
			lock,
		)
		if err != nil {
			return Publication{}, false, err
		}
		renderSessionID := input.ScopeSessionID
		if input.Scope == store.ResultsPublicationEventAwards {
			renderSessionID = 0
		}
		rendered, err = renderResultsPublication(
			ctx,
			transaction,
			input.EventID,
			renderSessionID,
			input.Current,
			next,
			lock,
			input.Now,
		)
	}
	if err != nil {
		return Publication{}, false, err
	}
	stored, err := appendScopedResultsPublication(
		ctx,
		transaction,
		store.AppendResultsPublicationParams{
			EventID:                   input.EventID,
			Scope:                     input.Scope,
			ScopeSessionID:            input.ScopeSessionID,
			ExpectedRevision:          input.Current.Revision,
			Policy:                    input.Policy,
			Status:                    store.ResultsPublicationStatus(next.Status),
			Items:                     prizegivingItemRefInputs(next.Items),
			Lock:                      lock,
			Template:                  prizegivingTemplateInput(rendered.Template),
			RenderedHTML:              rendered.HTML,
			RenderedText:              rendered.Text,
			RenderedJSON:              rendered.JSON,
			ResultsCorrectionRevision: input.ResultsCorrectionRevision,
			CreatedByAccountID:        input.ActorAccountID,
			Now:                       input.Now,
		},
	)
	if err != nil {
		return Publication{}, false, err
	}
	return publicationFromStore(stored), true, nil
}

func appendScopedResultsPublication(
	ctx context.Context,
	transaction *store.CommandTx,
	params store.AppendResultsPublicationParams,
) (store.ResultsPublication, error) {
	scoped, err := transaction.AppendResultsPublication(ctx, params)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	state, err := transaction.LoadEventResultsAggregateState(ctx, params.EventID)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	current, err := transaction.LoadResultsPublication(
		ctx,
		params.EventID,
		store.ResultsPublicationEvent,
		params.EventID,
	)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	rendered, aggregateParams, err := buildEventResultsPublication(
		state,
		current,
		scoped,
		params.CreatedByAccountID,
		params.Now,
	)
	if err != nil {
		return store.ResultsPublication{}, err
	}
	aggregateParams.RenderedHTML = rendered.HTML
	aggregateParams.RenderedText = rendered.Text
	aggregateParams.RenderedJSON = rendered.JSON
	if _, err = transaction.AppendResultsPublication(ctx, aggregateParams); err != nil {
		return store.ResultsPublication{}, err
	}
	return scoped, nil
}

type eventResultIdentity struct {
	kind                 ResultItemKind
	competitionSessionID int
	awardKey             string
}

func eventResultRefIdentity(ref ResultItemRef) eventResultIdentity {
	kind := ref.Kind
	if kind == ResultItemNoPublicResults {
		kind = ResultItemCompetition
	}
	return eventResultIdentity{
		kind: kind, competitionSessionID: ref.CompetitionSessionID,
		awardKey: ref.AwardKey,
	}
}

type eventAggregateItem struct {
	ref  ResultItemRef
	item PublicResultsItem
}

func buildEventResultsPublication(
	state store.EventResultsAggregateState,
	current, scoped store.ResultsPublication,
	actorID int,
	now time.Time,
) (RenderedPublicResults, store.AppendResultsPublicationParams, error) {
	order := eventResultsOrder(state, current)
	items := make(map[eventResultIdentity]eventAggregateItem)
	var scopedModel PublicResultsPublication
	if json.Unmarshal([]byte(scoped.RenderedJSON), &scopedModel) != nil {
		return RenderedPublicResults{}, store.AppendResultsPublicationParams{},
			ErrResultsRendering
	}
	var event PublicResultsEvent
	for _, publication := range state.Publications {
		var model PublicResultsPublication
		if json.Unmarshal([]byte(publication.RenderedJSON), &model) != nil ||
			len(model.Items) != len(publication.Items) {
			return RenderedPublicResults{}, store.AppendResultsPublicationParams{},
				ErrResultsRendering
		}
		if event.Name == "" {
			event = model.Event
		}
		for index, item := range model.Items {
			ref := prizegivingItemRefs([]store.PrizegivingResultItemRef{
				publication.Items[index],
			})[0]
			items[eventResultRefIdentity(ref)] = eventAggregateItem{
				ref: ref, item: item,
			}
		}
	}
	if current.Revision > 0 {
		var model PublicResultsPublication
		if json.Unmarshal([]byte(current.RenderedJSON), &model) != nil {
			return RenderedPublicResults{}, store.AppendResultsPublicationParams{},
				ErrResultsRendering
		}
		event = model.Event
	}
	backfillPublicResultsEventIdentity(
		&event,
		scopedModel.Event.ID,
		scopedModel.Event.Slug,
	)
	model := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         event,
		EventTitle:    event.Name,
		Revision:      current.Revision + 1,
		Status:        ResultsPublicationPartial,
		PublishedAt:   now,
	}
	storedItems := make([]store.PrizegivingResultItemRef, 0, len(items))
	releasedCompetitions := make(map[int]struct{})
	for _, locked := range order {
		ref := prizegivingItemRefs([]store.PrizegivingResultItemRef{locked})[0]
		aggregateItem, ok := items[eventResultRefIdentity(ref)]
		if !ok {
			continue
		}
		ref = aggregateItem.ref
		ref.DisplayOrder = len(model.Items) + 1
		model.Items = append(model.Items, aggregateItem.item)
		storedItems = append(
			storedItems,
			prizegivingItemRefInputs([]ResultItemRef{ref})[0],
		)
		if ref.Kind == ResultItemCompetition || ref.Kind == ResultItemNoPublicResults {
			releasedCompetitions[ref.CompetitionSessionID] = struct{}{}
		}
	}
	if len(releasedCompetitions) == len(state.Competitions) {
		model.Status = ResultsPublicationFinal
	}
	if scoped.ResultsCorrectionRevision > 0 {
		var corrected PublicResultsPublication
		if json.Unmarshal([]byte(scoped.RenderedJSON), &corrected) != nil {
			return RenderedPublicResults{}, store.AppendResultsPublicationParams{},
				ErrResultsRendering
		}
		model.Correction = &PublicResultsCorrection{
			PreviousRevision: current.Revision,
			CorrectedAt:      now,
		}
		if corrected.Correction != nil {
			model.Correction.Note = corrected.Correction.Note
		}
	}
	template := DefaultResultsTextTemplate()
	if current.Revision > 0 {
		template = prizegivingTemplate(current.Template)
	} else if scoped.Template.Revision > 0 {
		template = prizegivingTemplate(scoped.Template)
	}
	rendered, err := RenderPublicResults(model, template)
	if err != nil {
		return RenderedPublicResults{}, store.AppendResultsPublicationParams{}, err
	}
	lock := store.PrizegivingPreflightLock{
		PublicationOrder: order,
		Template:         prizegivingTemplateInput(template),
	}
	return rendered, store.AppendResultsPublicationParams{
		EventID: scoped.EventID,
		Scope:   store.ResultsPublicationEvent, ScopeSessionID: scoped.EventID,
		ExpectedRevision:   current.Revision,
		Policy:             ResultsStandalone,
		Status:             store.ResultsPublicationStatus(model.Status),
		Items:              storedItems,
		Lock:               lock,
		Template:           prizegivingTemplateInput(template),
		CreatedByAccountID: actorID,
		Now:                now,
	}, nil
}

func eventResultsOrder(
	state store.EventResultsAggregateState,
	current store.ResultsPublication,
) []store.PrizegivingResultItemRef {
	if current.Revision > 0 {
		return appendMissingEventResultRefs(
			current.Lock.PublicationOrder,
			state.Publications,
		)
	}
	input := PrizegivingDefaultOrderInput{
		Competitions: make(
			[]PrizegivingCompetitionOrderSource,
			0,
			len(state.Competitions),
		),
		EventAwards: make([]EventAward, 0, len(state.EventAwards)),
	}
	for _, competition := range state.Competitions {
		input.Competitions = append(input.Competitions, PrizegivingCompetitionOrderSource{
			SessionID: competition.SessionID, PlannedStart: competition.PlannedStart,
			Draft: draft(competition.Draft),
		})
	}
	for _, award := range state.EventAwards {
		input.EventAwards = append(input.EventAwards, EventAward{
			Award: Award{
				Key: award.Key, Name: award.Name, DisplayOrder: award.DisplayOrder,
				Recipients: awardRecipients(award.Recipients),
			},
			ReleasePath: awardReleasePath(award.ReleasePath),
		})
	}
	_, order := BuildDefaultPrizegivingOrder(input)
	return appendMissingEventResultRefs(
		prizegivingItemRefInputs(order),
		state.Publications,
	)
}

func appendMissingEventResultRefs(
	order []store.PrizegivingResultItemRef,
	publications []store.ResultsPublication,
) []store.PrizegivingResultItemRef {
	identities := make(map[eventResultIdentity]struct{}, len(order))
	for _, stored := range order {
		ref := prizegivingItemRefs([]store.PrizegivingResultItemRef{stored})[0]
		identities[eventResultRefIdentity(ref)] = struct{}{}
	}
	for _, publication := range publications {
		for _, stored := range publication.Items {
			ref := prizegivingItemRefs([]store.PrizegivingResultItemRef{stored})[0]
			identity := eventResultRefIdentity(ref)
			if _, ok := identities[identity]; ok {
				continue
			}
			ref.DisplayOrder = len(order) + 1
			order = append(
				order,
				prizegivingItemRefInputs([]ResultItemRef{ref})[0],
			)
			identities[identity] = struct{}{}
		}
	}
	for index := range order {
		order[index].DisplayOrder = index + 1
	}
	return order
}

func freezeResultsRenderSource(
	ctx context.Context,
	transaction *store.CommandTx,
	eventID int,
	lock store.PrizegivingPreflightLock,
) (store.PrizegivingPreflightLock, error) {
	if len(lock.RenderSource) != 0 {
		return lock, nil
	}
	source, err := transaction.LoadResultsPublicationRenderSource(ctx, eventID, lock)
	if err != nil {
		return store.PrizegivingPreflightLock{}, err
	}
	lock.RenderSource, err = json.Marshal(source)
	if err != nil {
		return store.PrizegivingPreflightLock{}, ErrResultsRendering
	}
	return lock, nil
}

func renderResultsPublication(
	ctx context.Context,
	transaction *store.CommandTx,
	eventID, scopeSessionID int,
	current store.ResultsPublication,
	next Publication,
	lock store.PrizegivingPreflightLock,
	publishedAt time.Time,
) (RenderedPublicResults, error) {
	source, err := transaction.LoadResultsPublicationRenderSource(ctx, eventID, lock)
	if err != nil {
		return RenderedPublicResults{}, err
	}
	publicSource, err := publicResultsSource(source, next, scopeSessionID, publishedAt)
	if err != nil {
		return RenderedPublicResults{}, err
	}
	model, err := BuildPublicResultsModel(publicSource)
	if err != nil {
		return RenderedPublicResults{}, err
	}
	if current.Revision > 0 && current.RenderedJSON != "" {
		var frozen PublicResultsPublication
		if json.Unmarshal([]byte(current.RenderedJSON), &frozen) != nil ||
			len(frozen.Items) != len(current.Items) {
			return RenderedPublicResults{}, ErrResultsRendering
		}
		preservePublishedResults(
			&model,
			frozen,
			publicationFromStore(current).Items,
			next.Items,
		)
	}
	template := TextTemplate{
		Revision: lock.Template.Revision,
		Source:   lock.Template.Source,
	}
	return RenderPublicResults(model, template)
}

func backfillPublicResultsEventIdentity(
	event *PublicResultsEvent,
	eventID int,
	eventSlug string,
) {
	if event.ID <= 0 {
		event.ID = eventID
	}
	if strings.TrimSpace(event.Slug) == "" {
		event.Slug = eventSlug
	}
}

func missingPublicResultsEventIdentity(event PublicResultsEvent) bool {
	return event.ID <= 0 || strings.TrimSpace(event.Slug) == ""
}

func preservePublishedResults(
	model *PublicResultsPublication,
	frozen PublicResultsPublication,
	currentItems, nextItems []ResultItemRef,
) {
	frozenByIdentity := make(
		map[resultItemIdentity]PublicResultsItem,
		len(currentItems),
	)
	for index, ref := range currentItems {
		frozenByIdentity[resultItemIdentityFromRef(ref)] = frozen.Items[index]
	}
	backfillPublicResultsEventIdentity(
		&frozen.Event,
		model.Event.ID,
		model.Event.Slug,
	)
	model.Event = frozen.Event
	model.EventTitle = frozen.Event.Name
	model.Correction = frozen.Correction
	for index, ref := range nextItems {
		if item, ok := frozenByIdentity[resultItemIdentityFromRef(ref)]; ok {
			model.Items[index] = item
		}
	}
}

func publicResultsSource(
	source store.ResultsPublicationRenderSource,
	next Publication,
	scopeSessionID int,
	publishedAt time.Time,
) (PublicResultsSource, error) {
	competitions := make(
		map[int]store.ResultsPublicationCompetitionSource,
		len(source.Competitions),
	)
	entryNames := make(map[int]string)
	for _, competition := range source.Competitions {
		competitions[competition.Draft.SessionID] = competition
		for _, entry := range competition.Entries {
			entryNames[entry.ID] = entry.Name
		}
	}
	for _, entry := range source.RecipientEntries {
		entryNames[entry.ID] = entry.Name
	}
	result := PublicResultsSource{
		EventID: source.EventID, EventSlug: source.EventSlug,
		EventName: source.EventName, EventLocale: source.EventLocale,
		ContentLanguage: source.ContentLanguage, Revision: next.Revision,
		Status: next.Status, PublishedAt: publishedAt,
		Items: make([]PublicResultsSourceItem, 0, len(next.Items)),
	}
	for _, ref := range next.Items {
		item := PublicResultsSourceItem{Ref: ref}
		switch ref.Kind {
		case ResultItemCompetition, ResultItemNoPublicResults:
			competition, ok := competitions[ref.CompetitionSessionID]
			if !ok {
				return PublicResultsSource{}, ErrResultsRendering
			}
			item = publicCompetitionSourceItem(ref, competition, entryNames)
		case ResultItemCompetitionAward:
			competition, ok := competitions[ref.CompetitionSessionID]
			if !ok {
				return PublicResultsSource{}, ErrResultsRendering
			}
			award, ok := findPublicCompetitionAward(
				competition.Draft.Awards,
				ref.AwardKey,
				entryNames,
			)
			if !ok {
				return PublicResultsSource{}, ErrResultsRendering
			}
			item.Award = &award
		case ResultItemEventAward:
			award, ok := findPublicEventAward(
				source.EventAwards,
				ref.AwardKey,
				scopeSessionID,
				entryNames,
			)
			if !ok {
				return PublicResultsSource{}, ErrResultsRendering
			}
			item.Award = &award
		default:
			return PublicResultsSource{}, ErrResultsRendering
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func publicCompetitionSourceItem(
	ref ResultItemRef,
	competition store.ResultsPublicationCompetitionSource,
	entryNames map[int]string,
) PublicResultsSourceItem {
	found := competition.Draft
	item := PublicResultsSourceItem{
		Ref: ref, CompetitionTitle: competition.Title,
		PublicExplanation: found.PublicExplanation,
		Score: ScorePolicy{
			Type: ScoreType(found.ScoreType), Visibility: ScoreVisibility(found.ScoreVisibility),
			Unit: found.ScoreUnit, Precision: found.ScorePrecision,
			Requirement:    ScoreRequirement(found.ScoreRequirement),
			Interpretation: ScoreInterpretation(found.ScoreInterpretation),
		},
	}
	standings := make(map[int]store.CompetitionResultStanding, len(found.Standings))
	for _, standing := range found.Standings {
		standings[standing.EntryID] = standing
	}
	for index, entry := range competition.Entries {
		if entry.Disposition != "Included" {
			continue
		}
		publicEntry := PublicResultsSourceEntry{
			EntryID: entry.ID, Name: entry.Name,
			ResultDisposition:             entry.ResultDisposition,
			PublicDisqualificationMessage: entry.PublicDisqualificationMessage,
			LockedOrder:                   index + 1,
		}
		if standing, ok := standings[entry.ID]; ok {
			publicEntry.Standing = ResultStanding(standing.Standing)
			publicEntry.Placement = standing.Placement
			publicEntry.DisplayOrder = standing.DisplayOrder
			publicEntry.DecimalScore = dereferenceString(standing.DecimalScore)
			if standing.DurationScoreNanos != nil {
				duration := time.Duration(*standing.DurationScoreNanos)
				publicEntry.DurationScore = &duration
			}
		}
		item.Entries = append(item.Entries, publicEntry)
	}
	for _, award := range found.Awards {
		if award.Promoted {
			continue
		}
		item.Awards = append(item.Awards, publicResultsAwardSource(
			award.Key,
			award.Name,
			award.Recipients,
			entryNames,
		))
	}
	return item
}

func findPublicCompetitionAward(
	awards []store.CompetitionAward,
	key string,
	entryNames map[int]string,
) (PublicResultsSourceAward, bool) {
	for _, award := range awards {
		if award.Key == key && award.Promoted {
			return publicResultsAwardSource(
				award.Key,
				award.Name,
				award.Recipients,
				entryNames,
			), true
		}
	}
	return PublicResultsSourceAward{}, false
}

func findPublicEventAward(
	awards []store.EventAward,
	key string,
	scopeSessionID int,
	entryNames map[int]string,
) (PublicResultsSourceAward, bool) {
	for _, award := range awards {
		matchesPath := award.ReleasePath.Kind == "Standalone" && scopeSessionID == 0 ||
			award.ReleasePath.Kind == "Prizegiving" &&
				award.ReleasePath.PrizegivingSessionID == scopeSessionID
		if award.Key == key && matchesPath {
			return publicResultsAwardSource(
				award.Key,
				award.Name,
				award.Recipients,
				entryNames,
			), true
		}
	}
	return PublicResultsSourceAward{}, false
}

func publicResultsAwardSource(
	key string,
	name string,
	recipients []store.AwardRecipientInput,
	entryNames map[int]string,
) PublicResultsSourceAward {
	result := PublicResultsSourceAward{
		Key: key, Name: name, Recipients: make([]string, 0, len(recipients)),
	}
	for _, recipient := range recipients {
		displayName := recipient.DisplayName
		if recipient.EntryID > 0 {
			displayName = entryNames[recipient.EntryID]
		}
		result.Recipients = append(result.Recipients, displayName)
	}
	return result
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// PrizegivingPublicationStates projects canonical release state for publication.
func PrizegivingPublicationStates(items []store.ProgramItem) []ResultItemStageState {
	states := make([]ResultItemStageState, 0, len(items))
	for _, item := range items {
		if item.Result == nil {
			continue
		}
		states = append(states, ResultItemStageStateFromProgramResult(item.Result))
	}
	return states
}

// ResultItemStageStateFromProgramResult restores one canonical Result state
// from a Program projection.
func ResultItemStageStateFromProgramResult(
	item *store.ProgramResult,
) ResultItemStageState {
	if item == nil {
		return ResultItemStageState{}
	}
	return ResultItemStageState{
		Ref: ResultItemRef{
			Kind:                 ResultItemKind(item.Ref.Kind),
			CompetitionSessionID: item.Ref.CompetitionSessionID,
			AwardKey:             item.Ref.AwardKey,
			DisplayOrder:         item.Ref.DisplayOrder,
		},
		Status:               item.Status,
		Release:              item.Release,
		TakenAt:              item.TakenAt,
		RevealStartedAt:      item.RevealStartedAt,
		RevealDuration:       item.RevealDuration,
		RevealPausedAt:       item.RevealPausedAt,
		RevealPausedDuration: item.RevealPausedDuration,
		RevealCompletedAt:    item.RevealCompletedAt,
		SkippedAt:            item.SkippedAt,
	}
}

func publicationFromStore(value store.ResultsPublication) Publication {
	return Publication{
		Revision: value.Revision,
		Status:   PublicationStatus(value.Status),
		Items:    prizegivingItemRefs(value.Items),
	}
}

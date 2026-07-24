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
			lock, renderErr := freezeResultsRenderSource(
				actor.Context(ctx),
				transaction,
				input.EventID,
				lock,
			)
			if renderErr != nil {
				return command.Execution[Publication]{}, renderErr
			}
			rendered, renderErr := renderResultsPublication(
				actor.Context(ctx),
				transaction,
				input.EventID,
				input.CompetitionSessionID,
				current,
				next,
				lock,
				identity.Now,
			)
			if renderErr != nil {
				return command.Execution[Publication]{}, renderErr
			}
			stored, appendErr := transaction.AppendResultsPublication(
				actor.Context(ctx),
				store.AppendResultsPublicationParams{
					EventID:            input.EventID,
					Scope:              store.ResultsPublicationStandalone,
					ScopeSessionID:     input.CompetitionSessionID,
					ExpectedRevision:   current.Revision,
					Policy:             ResultsStandalone,
					Status:             store.ResultsPublicationStatus(next.Status),
					Items:              []store.PrizegivingResultItemRef{storedRef},
					Lock:               lock,
					Template:           prizegivingTemplateInput(rendered.Template),
					RenderedHTML:       rendered.HTML,
					RenderedText:       rendered.Text,
					RenderedJSON:       rendered.JSON,
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
	publicationOrder := plan.Lock.PublicationOrder
	if current.Revision > 0 {
		publicationOrder = current.Lock.PublicationOrder
	}
	next, changed, err := AdvancePublication(PublicationInput{
		Policy:        plan.ReleasePolicy,
		Order:         prizegivingItemRefs(publicationOrder),
		States:        PrizegivingPublicationStates(channel.Items),
		Current:       publicationFromStore(current),
		CueFired:      trigger.CueFired,
		CeremonyEnded: trigger.CeremonyEnded,
	})
	if err != nil || !changed {
		return next, changed, err
	}
	releaseLock := plan.Lock
	if current.Revision > 0 {
		releaseLock = current.Lock
	}
	releaseLock, err = freezeResultsRenderSource(ctx, transaction, eventID, releaseLock)
	if err != nil {
		return Publication{}, false, err
	}
	rendered, err := renderResultsPublication(
		ctx,
		transaction,
		eventID,
		ceremonySessionID,
		current,
		next,
		releaseLock,
		now,
	)
	if err != nil {
		return Publication{}, false, err
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
			Lock:               releaseLock,
			Template:           prizegivingTemplateInput(rendered.Template),
			RenderedHTML:       rendered.HTML,
			RenderedText:       rendered.Text,
			RenderedJSON:       rendered.JSON,
			CreatedByAccountID: actor.ID,
			Now:                now,
		},
	)
	if err != nil {
		return Publication{}, false, err
	}
	return publicationFromStore(stored), true, nil
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
		EventName: source.EventName, Revision: next.Revision,
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
		if award.Key == key &&
			award.ReleasePath.Kind == "Prizegiving" &&
			award.ReleasePath.PrizegivingSessionID == scopeSessionID {
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

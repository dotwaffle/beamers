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
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrPrizegivingPlanRevision means a command used stale plan state.
	ErrPrizegivingPlanRevision = store.ErrPrizegivingPlanRevision
	// ErrCompetitionPrizegivingAssignment means a Competition is assigned twice
	// or crosses its Event/type boundary.
	ErrCompetitionPrizegivingAssignment = store.ErrCompetitionPrizegivingAssignment
	// ErrPrizegivingLocked means successful Preflight froze the plan.
	ErrPrizegivingLocked = store.ErrPrizegivingLocked
	// ErrPrizegivingPreflightBlocked means one or more release blockers remain.
	ErrPrizegivingPreflightBlocked = errors.New("Prizegiving Preflight is blocked")
	// ErrPrizegivingPreflightRequired means Preview lacks a successful lock.
	ErrPrizegivingPreflightRequired = errors.New("Prizegiving Preflight is required")
)

const prizegivingPreviewWatermark = "PREVIEW — NOT PROGRAM OUTPUT"

// PrizegivingPlan is one editable or Preflight-locked release plan.
type PrizegivingPlan struct {
	ID                    int                      `json:"id"`
	EventID               int                      `json:"event_id"`
	CeremonySessionID     int                      `json:"ceremony_session_id"`
	Revision              int                      `json:"revision"`
	CompetitionSessionIDs []int                    `json:"competition_session_ids"`
	Sequence              []ResultItem             `json:"sequence"`
	PublicationOrder      []ResultItemRef          `json:"publication_order"`
	Template              TextTemplate             `json:"template"`
	Locked                bool                     `json:"locked"`
	Lock                  PrizegivingPreflightLock `json:"lock"`
	LockedByAccountID     int                      `json:"locked_by_account_id,omitempty"`
	LockedAt              time.Time                `json:"locked_at,omitzero"`
}

// SavePrizegivingPlanInput replaces one complete editable plan.
type SavePrizegivingPlanInput struct {
	EventID               int             `json:"event_id"`
	CeremonySessionID     int             `json:"ceremony_session_id"`
	CommandID             string          `json:"command_id"`
	ExpectedRevision      int             `json:"expected_revision"`
	CompetitionSessionIDs []int           `json:"competition_session_ids"`
	Sequence              []ResultItem    `json:"sequence"`
	PublicationOrder      []ResultItemRef `json:"publication_order"`
	Template              TextTemplate    `json:"template"`
}

// RunPrizegivingPreflightInput identifies one exact plan to review and lock.
type RunPrizegivingPreflightInput struct {
	EventID           int    `json:"event_id"`
	CeremonySessionID int    `json:"ceremony_session_id"`
	CommandID         string `json:"command_id"`
	ExpectedRevision  int    `json:"expected_revision"`
}

// PrizegivingPreflight reports blockers or the resulting immutable plan.
type PrizegivingPreflight struct {
	Plan     PrizegivingPlan               `json:"plan"`
	Findings []PrizegivingPreflightFinding `json:"findings"`
}

// PrizegivingPreviewMode distinguishes inspection from rehearsal.
type PrizegivingPreviewMode string

const (
	// PrizegivingPreviewModePreview inspects the exact locked content.
	PrizegivingPreviewModePreview PrizegivingPreviewMode = "Preview"
	// PrizegivingPreviewModeRehearsal runs the same content without side effects.
	PrizegivingPreviewModeRehearsal PrizegivingPreviewMode = "Rehearsal"
)

// PrizegivingPreview is a watermarked, side-effect-free locked projection.
type PrizegivingPreview struct {
	Mode               PrizegivingPreviewMode `json:"mode"`
	Watermark          string                 `json:"watermark"`
	Plan               PrizegivingPlan        `json:"plan"`
	CompetitionResults []Draft                `json:"competition_results"`
	EventAwards        []EventAward           `json:"event_awards"`
}

// GetPrizegivingPlan returns one designated plan to Results viewers.
func (service *Service) GetPrizegivingPlan(
	ctx context.Context,
	actor auth.Account,
	eventID, ceremonySessionID int,
) (PrizegivingPlan, error) {
	if eventID <= 0 || ceremonySessionID <= 0 {
		return PrizegivingPlan{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return PrizegivingPlan{}, ErrViewRequired
	}
	found, err := service.storage.LoadPrizegivingPlan(
		actor.Context(ctx),
		eventID,
		ceremonySessionID,
	)
	if err != nil {
		return PrizegivingPlan{}, err
	}
	return prizegivingPlan(found), nil
}

// SavePrizegivingPlan atomically replaces assignments and both explicit orders.
func (service *Service) SavePrizegivingPlan(
	ctx context.Context,
	actor auth.Account,
	input SavePrizegivingPlanInput,
) (PrizegivingPlan, error) {
	if err := validateSavePrizegivingPlanInput(input); err != nil {
		return PrizegivingPlan{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return PrizegivingPlan{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return PrizegivingPlan{}, errors.New("encode Prizegiving plan command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "SavePrizegivingPlan", TargetType: "Session",
		TargetID: strconv.Itoa(input.CeremonySessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[PrizegivingPlan]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (PrizegivingPlan, error) {
			var stored store.PrizegivingPlan
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
				return PrizegivingPlan{}, err
			}
			return prizegivingPlan(stored), nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[PrizegivingPlan], error) {
			sequence := input.Sequence
			publicationOrder := input.PublicationOrder
			if len(sequence) == 0 && len(publicationOrder) == 0 {
				state, loadErr := transaction.LoadPrizegivingDefaultOrderState(
					actor.Context(ctx),
					input.EventID,
					input.CeremonySessionID,
					input.CompetitionSessionIDs,
				)
				if loadErr != nil {
					return command.Execution[PrizegivingPlan]{}, loadErr
				}
				sequence, publicationOrder = BuildDefaultPrizegivingOrder(
					prizegivingDefaultOrderInput(state),
				)
			}
			stored, saveErr := transaction.SavePrizegivingPlan(
				actor.Context(ctx),
				store.SavePrizegivingPlanParams{
					EventID: input.EventID, CeremonySessionID: input.CeremonySessionID,
					ExpectedRevision: input.ExpectedRevision,
					CompetitionSessionIDs: append(
						[]int(nil),
						input.CompetitionSessionIDs...,
					),
					Sequence:         prizegivingItemInputs(sequence),
					PublicationOrder: prizegivingItemRefInputs(publicationOrder),
					Template:         prizegivingTemplateInput(input.Template),
				},
			)
			if saveErr != nil {
				return command.Execution[PrizegivingPlan]{}, saveErr
			}
			outcome, marshalErr := json.Marshal(stored)
			if marshalErr != nil {
				return command.Execution[PrizegivingPlan]{}, errors.New("encode Prizegiving plan outcome")
			}
			return command.Success(prizegivingPlan(stored), string(outcome)), nil
		},
	})
}

// RunPrizegivingPreflight validates every source and locks an exact snapshot.
func (service *Service) RunPrizegivingPreflight(
	ctx context.Context,
	actor auth.Account,
	input RunPrizegivingPreflightInput,
) (PrizegivingPreflight, error) {
	if err := validateRunPrizegivingPreflightInput(input); err != nil {
		return PrizegivingPreflight{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return PrizegivingPreflight{}, ErrProducerRequired
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return PrizegivingPreflight{}, errors.New("encode Prizegiving Preflight command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "RunPrizegivingPreflight", TargetType: "Session",
		TargetID: strconv.Itoa(input.CeremonySessionID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[PrizegivingPreflight]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (PrizegivingPreflight, error) {
			var result PrizegivingPreflight
			if err := store.DecodeCommandReceipt(outcome, &result); err != nil {
				return PrizegivingPreflight{}, err
			}
			if len(result.Findings) != 0 {
				return result, ErrPrizegivingPreflightBlocked
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[PrizegivingPreflight], error) {
			state, loadErr := transaction.LoadPrizegivingPreflightState(
				actor.Context(ctx),
				input.EventID,
				input.CeremonySessionID,
			)
			if loadErr != nil {
				return command.Execution[PrizegivingPreflight]{}, loadErr
			}
			if state.Plan.Locked {
				return command.Execution[PrizegivingPreflight]{}, ErrPrizegivingLocked
			}
			if state.Plan.Revision != input.ExpectedRevision {
				return command.Execution[PrizegivingPreflight]{}, ErrPrizegivingPlanRevision
			}
			lock, findings := BuildPrizegivingPreflight(
				prizegivingPreflightInput(state),
				input.CommandID,
			)
			if len(findings) != 0 {
				result := PrizegivingPreflight{
					Plan: prizegivingPlan(state.Plan), Findings: findings,
				}
				outcome, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					return command.Execution[PrizegivingPreflight]{}, errors.New(
						"encode blocked Prizegiving Preflight",
					)
				}
				return command.RejectEncoded(
					result,
					string(outcome),
					ErrPrizegivingPreflightBlocked,
				), nil
			}
			stored, lockErr := transaction.LockPrizegivingPlan(
				actor.Context(ctx),
				input.EventID,
				input.CeremonySessionID,
				input.ExpectedRevision,
				prizegivingLockInput(lock),
				actor.ID,
				identity.Now,
			)
			if lockErr != nil {
				return command.Execution[PrizegivingPreflight]{}, lockErr
			}
			result := PrizegivingPreflight{Plan: prizegivingPlan(stored)}
			outcome, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				return command.Execution[PrizegivingPreflight]{}, errors.New(
					"encode Prizegiving Preflight outcome",
				)
			}
			return command.Success(result, string(outcome)), nil
		},
	})
}

// PreviewPrizegiving returns the same immutable lock for Preview or rehearsal.
func (service *Service) PreviewPrizegiving(
	ctx context.Context,
	actor auth.Account,
	eventID, ceremonySessionID int,
	mode PrizegivingPreviewMode,
) (PrizegivingPreview, error) {
	if eventID <= 0 ||
		ceremonySessionID <= 0 ||
		mode != PrizegivingPreviewModePreview &&
			mode != PrizegivingPreviewModeRehearsal {
		return PrizegivingPreview{}, ErrInvalidInput
	}
	if !actor.HasCapability(eventID, viewer.ViewResults) {
		return PrizegivingPreview{}, ErrViewRequired
	}
	found, err := service.storage.LoadPrizegivingPreview(
		actor.Context(ctx),
		eventID,
		ceremonySessionID,
	)
	if err != nil {
		return PrizegivingPreview{}, err
	}
	if !found.Plan.Locked {
		return PrizegivingPreview{}, ErrPrizegivingPreflightRequired
	}
	competitionResults := make([]Draft, 0, len(found.CompetitionResults))
	for _, stored := range found.CompetitionResults {
		competitionResults = append(competitionResults, draft(stored))
	}
	eventAwards := eventAwardsDraft(store.EventAwardsDraft{
		Awards: found.EventAwards,
	}).Awards
	return projectPrizegivingPreview(
		prizegivingPlan(found.Plan),
		competitionResults,
		eventAwards,
		mode,
	)
}

func projectPrizegivingPreview(
	plan PrizegivingPlan,
	competitionResults []Draft,
	eventAwards []EventAward,
	mode PrizegivingPreviewMode,
) (PrizegivingPreview, error) {
	if !plan.Locked {
		return PrizegivingPreview{}, ErrPrizegivingPreflightRequired
	}
	if mode != PrizegivingPreviewModePreview &&
		mode != PrizegivingPreviewModeRehearsal {
		return PrizegivingPreview{}, ErrInvalidInput
	}
	return PrizegivingPreview{
		Mode: mode, Watermark: prizegivingPreviewWatermark,
		Plan:               clonePrizegivingPlan(plan),
		CompetitionResults: cloneDrafts(competitionResults),
		EventAwards:        cloneEventAwards(eventAwards),
	}, nil
}

func clonePrizegivingPlan(value PrizegivingPlan) PrizegivingPlan {
	value.CompetitionSessionIDs = append([]int(nil), value.CompetitionSessionIDs...)
	value.Sequence = append([]ResultItem(nil), value.Sequence...)
	value.PublicationOrder = append([]ResultItemRef(nil), value.PublicationOrder...)
	value.Lock.CompetitionSources = append(
		[]PrizegivingCompetitionLock(nil),
		value.Lock.CompetitionSources...,
	)
	value.Lock.Sequence = append([]LockedResultItem(nil), value.Lock.Sequence...)
	value.Lock.PublicationOrder = append(
		[]ResultItemRef(nil),
		value.Lock.PublicationOrder...,
	)
	return value
}

func cloneDrafts(values []Draft) []Draft {
	result := make([]Draft, 0, len(values))
	for _, value := range values {
		value.Standings = append([]Standing(nil), value.Standings...)
		value.Awards = cloneAwards(value.Awards)
		result = append(result, value)
	}
	return result
}

func cloneEventAwards(values []EventAward) []EventAward {
	result := make([]EventAward, 0, len(values))
	for _, value := range values {
		value.Recipients = append([]AwardRecipient(nil), value.Recipients...)
		result = append(result, value)
	}
	return result
}

func cloneAwards(values []Award) []Award {
	result := make([]Award, 0, len(values))
	for _, value := range values {
		value.Recipients = append([]AwardRecipient(nil), value.Recipients...)
		result = append(result, value)
	}
	return result
}

func validateSavePrizegivingPlanInput(input SavePrizegivingPlanInput) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 ||
		input.CeremonySessionID <= 0 ||
		input.ExpectedRevision < 0 ||
		len(input.CompetitionSessionIDs) > 1000 ||
		len(input.Sequence) > 3000 ||
		len(input.PublicationOrder) > 3000 ||
		!boundedResultsTextTemplate(input.Template) {
		return ErrInvalidInput
	}
	seenCompetitions := make(map[int]struct{}, len(input.CompetitionSessionIDs))
	for _, competitionSessionID := range input.CompetitionSessionIDs {
		if competitionSessionID <= 0 {
			return ErrInvalidInput
		}
		if _, duplicate := seenCompetitions[competitionSessionID]; duplicate {
			return ErrInvalidInput
		}
		seenCompetitions[competitionSessionID] = struct{}{}
	}
	sequenceItems := make(map[resultItemIdentity]int, len(input.Sequence))
	for index, item := range input.Sequence {
		if item.DisplayOrder != index+1 ||
			!validResultItemRef(item.Ref(item.DisplayOrder)) ||
			item.RevealMethod == "" ||
			len(item.RevealMethod) > 100 {
			return ErrInvalidInput
		}
		sequenceItems[resultItemIdentity{
			Kind: item.Kind, CompetitionSessionID: item.CompetitionSessionID,
			AwardKey: item.AwardKey,
		}]++
	}
	if !exactPrizegivingPublicationOrder(input.PublicationOrder, sequenceItems) {
		return ErrInvalidInput
	}
	return nil
}

func validateRunPrizegivingPreflightInput(
	input RunPrizegivingPreflightInput,
) error {
	if err := command.ValidateID(input.CommandID); err != nil {
		return err
	}
	if input.EventID <= 0 ||
		input.CeremonySessionID <= 0 ||
		input.ExpectedRevision <= 0 {
		return ErrInvalidInput
	}
	return nil
}

func prizegivingPlan(value store.PrizegivingPlan) PrizegivingPlan {
	return PrizegivingPlan{
		ID: value.ID, EventID: value.EventID,
		CeremonySessionID: value.CeremonySessionID, Revision: value.Revision,
		CompetitionSessionIDs: append([]int(nil), value.CompetitionSessionIDs...),
		Sequence:              prizegivingItems(value.Sequence),
		PublicationOrder:      prizegivingItemRefs(value.PublicationOrder),
		Template:              prizegivingTemplate(value.Template),
		Locked:                value.Locked,
		Lock:                  prizegivingLock(value.Lock),
		LockedByAccountID:     value.LockedByAccountID,
		LockedAt:              value.LockedAt,
	}
}

func prizegivingDefaultOrderInput(
	value store.PrizegivingDefaultOrderState,
) PrizegivingDefaultOrderInput {
	result := PrizegivingDefaultOrderInput{
		Competitions: make(
			[]PrizegivingCompetitionOrderSource,
			0,
			len(value.Competitions),
		),
		EventAwards: eventAwardsDraft(store.EventAwardsDraft{
			Awards: value.EventAwards,
		}).Awards,
	}
	for _, competition := range value.Competitions {
		result.Competitions = append(
			result.Competitions,
			PrizegivingCompetitionOrderSource{
				SessionID:    competition.SessionID,
				PlannedStart: competition.PlannedStart,
				Draft:        draft(competition.Draft),
			},
		)
	}
	return result
}

func prizegivingPreflightInput(
	value store.PrizegivingPreflightState,
) PrizegivingPreflightInput {
	result := PrizegivingPreflightInput{
		EventID: value.Plan.EventID, CeremonySessionID: value.Plan.CeremonySessionID,
		PlanRevision:          value.Plan.Revision,
		CompetitionSessionIDs: append([]int(nil), value.Plan.CompetitionSessionIDs...),
		Sequence:              prizegivingItems(value.Plan.Sequence),
		PublicationOrder:      prizegivingItemRefs(value.Plan.PublicationOrder),
		Template:              prizegivingTemplate(value.Plan.Template),
		EventAwards: PrizegivingEventAwardsSource{
			DraftRevision: value.EventAwards.DraftRevision,
			PathRevision:  value.EventAwards.PathRevision,
			Ready:         value.EventAwards.Ready,
			Awards: eventAwardsDraft(store.EventAwardsDraft{
				Awards: value.EventAwards.Awards,
			}).Awards,
		},
	}
	for _, source := range value.Competitions {
		result.Competitions = append(
			result.Competitions,
			PrizegivingCompetitionSource{
				Draft:              draft(source.Draft),
				ResolutionRequired: source.ResolutionRequired,
			},
		)
	}
	return result
}

func prizegivingItemInputs(values []ResultItem) []store.PrizegivingResultItem {
	result := make([]store.PrizegivingResultItem, 0, len(values))
	for _, value := range values {
		result = append(result, store.PrizegivingResultItem{
			Kind: string(value.Kind), CompetitionSessionID: value.CompetitionSessionID,
			AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
			RevealMethod: string(value.RevealMethod),
		})
	}
	return result
}

func prizegivingItems(values []store.PrizegivingResultItem) []ResultItem {
	result := make([]ResultItem, 0, len(values))
	for _, value := range values {
		result = append(result, ResultItem{
			Kind:                 ResultItemKind(value.Kind),
			CompetitionSessionID: value.CompetitionSessionID,
			AwardKey:             value.AwardKey,
			DisplayOrder:         value.DisplayOrder,
			RevealMethod:         RevealMethod(value.RevealMethod),
		})
	}
	return result
}

func prizegivingItemRefInputs(
	values []ResultItemRef,
) []store.PrizegivingResultItemRef {
	result := make([]store.PrizegivingResultItemRef, 0, len(values))
	for _, value := range values {
		result = append(result, store.PrizegivingResultItemRef{
			Kind: string(value.Kind), CompetitionSessionID: value.CompetitionSessionID,
			AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
		})
	}
	return result
}

func prizegivingItemRefs(
	values []store.PrizegivingResultItemRef,
) []ResultItemRef {
	result := make([]ResultItemRef, 0, len(values))
	for _, value := range values {
		result = append(result, ResultItemRef{
			Kind:                 ResultItemKind(value.Kind),
			CompetitionSessionID: value.CompetitionSessionID,
			AwardKey:             value.AwardKey,
			DisplayOrder:         value.DisplayOrder,
		})
	}
	return result
}

func prizegivingTemplateInput(
	value TextTemplate,
) store.PrizegivingResultsTextTemplate {
	return store.PrizegivingResultsTextTemplate{
		Revision: value.Revision,
		Source:   value.Source,
	}
}

func prizegivingTemplate(
	value store.PrizegivingResultsTextTemplate,
) TextTemplate {
	return TextTemplate{Revision: value.Revision, Source: value.Source}
}

func prizegivingLockInput(
	value PrizegivingPreflightLock,
) store.PrizegivingPreflightLock {
	result := store.PrizegivingPreflightLock{
		PlanRevision:             value.PlanRevision,
		EventAwardsDraftRevision: value.EventAwardsDraftRevision,
		EventAwardsPathRevision:  value.EventAwardsPathRevision,
		PublicationOrder:         prizegivingItemRefInputs(value.PublicationOrder),
		Template:                 prizegivingTemplateInput(value.Template),
	}
	for _, source := range value.CompetitionSources {
		result.CompetitionSources = append(
			result.CompetitionSources,
			store.PrizegivingCompetitionLock{
				SessionID: source.SessionID, DraftID: source.DraftID,
				DraftRevision: source.DraftRevision,
				Disposition:   string(source.Disposition),
			},
		)
	}
	for _, item := range value.Sequence {
		stored := prizegivingItemInputs([]ResultItem{item.ResultItem})[0]
		result.Sequence = append(result.Sequence, store.PrizegivingLockedResultItem{
			PrizegivingResultItem: stored, RevealSeed: item.RevealSeed,
		})
	}
	return result
}

func prizegivingLock(
	value store.PrizegivingPreflightLock,
) PrizegivingPreflightLock {
	result := PrizegivingPreflightLock{
		PlanRevision:             value.PlanRevision,
		EventAwardsDraftRevision: value.EventAwardsDraftRevision,
		EventAwardsPathRevision:  value.EventAwardsPathRevision,
		PublicationOrder:         prizegivingItemRefs(value.PublicationOrder),
		Template:                 prizegivingTemplate(value.Template),
	}
	for _, source := range value.CompetitionSources {
		result.CompetitionSources = append(
			result.CompetitionSources,
			PrizegivingCompetitionLock{
				SessionID: source.SessionID, DraftID: source.DraftID,
				DraftRevision: source.DraftRevision,
				Disposition:   Disposition(source.Disposition),
			},
		)
	}
	for _, item := range value.Sequence {
		domain := prizegivingItems(
			[]store.PrizegivingResultItem{item.PrizegivingResultItem},
		)[0]
		result.Sequence = append(result.Sequence, LockedResultItem{
			ResultItem: domain, RevealSeed: item.RevealSeed,
		})
	}
	return result
}

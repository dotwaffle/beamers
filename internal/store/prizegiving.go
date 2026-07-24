package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/eventawardsdraft"
	"github.com/dotwaffle/beamers/ent/prizegiving"
	"github.com/dotwaffle/beamers/ent/prizegivingcompetition"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

var (
	// ErrCompetitionPrizegivingAssignment means a Competition already belongs
	// to another Prizegiving or crosses an Event/type boundary.
	ErrCompetitionPrizegivingAssignment = errors.New("invalid Competition Prizegiving assignment")
	// ErrPrizegivingPlanRevision means a command used stale Prizegiving plan state.
	ErrPrizegivingPlanRevision = errors.New("Prizegiving plan revision conflict")
	// ErrPrizegivingLocked means Preflight has frozen the Prizegiving plan.
	ErrPrizegivingLocked = errors.New("Prizegiving plan is locked")
)

// PrizegivingResultItemRef identifies one ordered Result Item.
type PrizegivingResultItemRef struct {
	Kind                 string `json:"kind"`
	CompetitionSessionID int    `json:"competition_session_id,omitempty"`
	AwardKey             string `json:"award_key,omitempty"`
	DisplayOrder         int    `json:"display_order"`
}

// PrizegivingResultItem adds presentation configuration.
type PrizegivingResultItem struct {
	Kind                 string `json:"kind"`
	CompetitionSessionID int    `json:"competition_session_id,omitempty"`
	AwardKey             string `json:"award_key,omitempty"`
	DisplayOrder         int    `json:"display_order"`
	RevealMethod         string `json:"reveal_method"`
}

// PrizegivingResultsTextTemplate is one exact template revision.
type PrizegivingResultsTextTemplate struct {
	Revision int    `json:"revision"`
	Source   string `json:"source"`
}

// PrizegivingCompetitionLock identifies one immutable Results Draft.
type PrizegivingCompetitionLock struct {
	SessionID     int    `json:"session_id"`
	DraftID       int    `json:"draft_id"`
	DraftRevision int    `json:"draft_revision"`
	Disposition   string `json:"disposition"`
}

// PrizegivingLockedResultItem binds one item to a Reveal Seed.
type PrizegivingLockedResultItem struct {
	PrizegivingResultItem
	RevealSeed uint64 `json:"reveal_seed"`
}

// PrizegivingPreflightLock is one immutable successful Preflight snapshot.
type PrizegivingPreflightLock struct {
	PlanRevision             int                            `json:"plan_revision"`
	CompetitionSources       []PrizegivingCompetitionLock   `json:"competition_sources"`
	EventAwardsDraftRevision int                            `json:"event_awards_draft_revision"`
	EventAwardsPathRevision  int                            `json:"event_awards_path_revision"`
	Sequence                 []PrizegivingLockedResultItem  `json:"sequence"`
	PublicationOrder         []PrizegivingResultItemRef     `json:"publication_order"`
	Template                 PrizegivingResultsTextTemplate `json:"template"`
}

// PrizegivingPlan is one editable or Preflight-locked release plan.
type PrizegivingPlan struct {
	ID                    int                            `json:"id"`
	EventID               int                            `json:"event_id"`
	CeremonySessionID     int                            `json:"ceremony_session_id"`
	Revision              int                            `json:"revision"`
	CompetitionSessionIDs []int                          `json:"competition_session_ids"`
	Sequence              []PrizegivingResultItem        `json:"sequence"`
	PublicationOrder      []PrizegivingResultItemRef     `json:"publication_order"`
	Template              PrizegivingResultsTextTemplate `json:"template"`
	Locked                bool                           `json:"locked"`
	Lock                  PrizegivingPreflightLock       `json:"lock"`
	LockedByAccountID     int                            `json:"locked_by_account_id,omitempty"`
	LockedAt              time.Time                      `json:"locked_at,omitzero"`
	CreatedByAccountID    int                            `json:"created_by_account_id"`
	CreatedAt             time.Time                      `json:"created_at"`
}

// SavePrizegivingPlanParams contains one whole editable plan snapshot.
type SavePrizegivingPlanParams struct {
	EventID, CeremonySessionID int
	ExpectedRevision           int
	CompetitionSessionIDs      []int
	Sequence                   []PrizegivingResultItem
	PublicationOrder           []PrizegivingResultItemRef
	Template                   PrizegivingResultsTextTemplate
}

// PrizegivingCompetitionPreflightState contains one assigned Competition source.
type PrizegivingCompetitionPreflightState struct {
	Draft              CompetitionResultsDraft
	ResolutionRequired bool
}

// PrizegivingEventAwardsPreflightState contains the assigned Award path source.
type PrizegivingEventAwardsPreflightState struct {
	DraftRevision int
	PathRevision  int
	Ready         bool
	Awards        []EventAward
}

// PrizegivingPreflightState is the complete current state for domain review.
type PrizegivingPreflightState struct {
	Plan         PrizegivingPlan
	Competitions []PrizegivingCompetitionPreflightState
	EventAwards  PrizegivingEventAwardsPreflightState
}

// PrizegivingPreviewState contains the exact immutable sources locked by Preflight.
type PrizegivingPreviewState struct {
	Plan               PrizegivingPlan
	CompetitionResults []CompetitionResultsDraft
	EventAwards        []EventAward
}

// LoadPrizegivingPlan returns one designated Prizegiving's current plan.
func (installation *SQLite) LoadPrizegivingPlan(
	ctx context.Context,
	eventID, ceremonySessionID int,
) (PrizegivingPlan, error) {
	found, err := installation.client.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(eventID),
			prizegiving.CeremonySessionIDEQ(ceremonySessionID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingSession
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("load Prizegiving plan", err)
	}
	return storedPrizegivingPlan(found), nil
}

// LoadPrizegivingPreview returns the exact immutable sources locked by Preflight.
func (installation *SQLite) LoadPrizegivingPreview(
	ctx context.Context,
	eventID, ceremonySessionID int,
) (PrizegivingPreviewState, error) {
	plan, err := installation.LoadPrizegivingPlan(ctx, eventID, ceremonySessionID)
	if err != nil {
		return PrizegivingPreviewState{}, err
	}
	state := PrizegivingPreviewState{Plan: plan}
	if !plan.Locked {
		return state, nil
	}
	state.CompetitionResults = make(
		[]CompetitionResultsDraft,
		0,
		len(plan.Lock.CompetitionSources),
	)
	for _, source := range plan.Lock.CompetitionSources {
		draft, loadErr := loadCompetitionResultsDraftByID(
			ctx,
			installation.client,
			source.DraftID,
		)
		if loadErr != nil {
			return PrizegivingPreviewState{}, loadErr
		}
		if draft.EventID != eventID ||
			draft.SessionID != source.SessionID ||
			draft.Revision != source.DraftRevision {
			return PrizegivingPreviewState{}, errors.New(
				"locked Competition Results source does not match Prizegiving",
			)
		}
		state.CompetitionResults = append(state.CompetitionResults, draft)
	}
	if plan.Lock.EventAwardsDraftRevision == 0 {
		return state, nil
	}
	awardsDraft, err := installation.client.EventAwardsDraft.Query().
		Where(
			eventawardsdraft.EventIDEQ(eventID),
			eventawardsdraft.RevisionEQ(plan.Lock.EventAwardsDraftRevision),
		).
		Only(ctx)
	if err != nil {
		return PrizegivingPreviewState{}, opaqueError(
			"load locked Event Awards Draft",
			err,
		)
	}
	state.EventAwards = eventAwardsForPath(
		eventAwardsDraft(awardsDraft).Awards,
		AwardReleasePath{
			Kind: "Prizegiving", PrizegivingSessionID: ceremonySessionID,
		},
	)
	return state, nil
}

// LoadPrizegivingPlan returns the current plan inside a command transaction.
func (transaction *CommandTx) LoadPrizegivingPlan(
	ctx context.Context,
	eventID, ceremonySessionID int,
) (PrizegivingPlan, error) {
	found, err := transaction.transaction.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(eventID),
			prizegiving.CeremonySessionIDEQ(ceremonySessionID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingSession
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("load Prizegiving plan", err)
	}
	return storedPrizegivingPlan(found), nil
}

// LoadPrizegivingPreflightState returns every source reviewed by Preflight.
func (transaction *CommandTx) LoadPrizegivingPreflightState(
	ctx context.Context,
	eventID, ceremonySessionID int,
) (PrizegivingPreflightState, error) {
	plan, err := transaction.LoadPrizegivingPlan(ctx, eventID, ceremonySessionID)
	if err != nil {
		return PrizegivingPreflightState{}, err
	}
	ceremony, err := transaction.transaction.Session.Get(ctx, ceremonySessionID)
	if err != nil {
		return PrizegivingPreflightState{}, ErrPrizegivingSession
	}
	version, err := ceremony.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if err != nil || version.Type != sessionpublishedversion.TypeCeremony {
		return PrizegivingPreflightState{}, ErrPrizegivingSession
	}
	state := PrizegivingPreflightState{
		Plan: plan,
		Competitions: make(
			[]PrizegivingCompetitionPreflightState,
			0,
			len(plan.CompetitionSessionIDs),
		),
	}
	for _, competitionSessionID := range plan.CompetitionSessionIDs {
		draft, loadErr := transaction.LoadCompetitionResultsDraft(
			ctx,
			eventID,
			competitionSessionID,
		)
		if loadErr != nil {
			return PrizegivingPreflightState{}, loadErr
		}
		resolutionRequired, resolutionErr := transaction.transaction.CompetitionEntry.Query().
			Where(
				competitionentry.EventIDEQ(eventID),
				competitionentry.CompetitionSessionIDEQ(competitionSessionID),
				competitionentry.ResolutionRequiredEQ(true),
			).
			Exist(ctx)
		if resolutionErr != nil {
			return PrizegivingPreflightState{}, opaqueError(
				"load Prizegiving required resolutions",
				resolutionErr,
			)
		}
		state.Competitions = append(
			state.Competitions,
			PrizegivingCompetitionPreflightState{
				Draft: draft, ResolutionRequired: resolutionRequired,
			},
		)
	}
	eventAwards, err := transaction.LoadEventAwardsDraft(ctx, eventID)
	if err != nil {
		return PrizegivingPreflightState{}, err
	}
	path := AwardReleasePath{
		Kind: "Prizegiving", PrizegivingSessionID: ceremonySessionID,
	}
	state.EventAwards.DraftRevision = eventAwards.Revision
	state.EventAwards.Awards = eventAwardsForPath(eventAwards.Awards, path)
	for _, pathState := range eventAwards.PathStates {
		if pathState.ReleasePath == path {
			state.EventAwards.PathRevision = pathState.Revision
			state.EventAwards.Ready = pathState.Ready
			break
		}
	}
	return state, nil
}

// SavePrizegivingPlan atomically replaces assignments and editable ordering.
func (transaction *CommandTx) SavePrizegivingPlan(
	ctx context.Context,
	params SavePrizegivingPlanParams,
) (PrizegivingPlan, error) {
	found, err := transaction.transaction.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(params.EventID),
			prizegiving.CeremonySessionIDEQ(params.CeremonySessionID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingSession
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("load Prizegiving plan for update", err)
	}
	if found.Locked {
		return storedPrizegivingPlan(found), ErrPrizegivingLocked
	}
	if found.Revision != params.ExpectedRevision {
		return storedPrizegivingPlan(found), ErrPrizegivingPlanRevision
	}
	if assignmentErr := transaction.validatePrizegivingCompetitionAssignments(
		ctx,
		found,
		params.CompetitionSessionIDs,
	); assignmentErr != nil {
		return PrizegivingPlan{}, assignmentErr
	}
	if len(params.Sequence) == 0 && len(params.PublicationOrder) == 0 {
		params.Sequence, params.PublicationOrder, err =
			transaction.defaultPrizegivingOrder(ctx, params)
		if err != nil {
			return PrizegivingPlan{}, err
		}
	}
	currentAssignments, err := transaction.transaction.PrizegivingCompetition.Query().
		Where(prizegivingcompetition.PrizegivingIDEQ(found.ID)).
		All(ctx)
	if err != nil {
		return PrizegivingPlan{}, opaqueError("load Prizegiving Competition assignments", err)
	}
	for _, assignment := range currentAssignments {
		if err = transaction.transaction.PrizegivingCompetition.DeleteOne(assignment).
			Exec(ctx); err != nil {
			return PrizegivingPlan{}, opaqueError("replace Prizegiving Competition assignments", err)
		}
	}
	if len(params.CompetitionSessionIDs) != 0 {
		builders := make([]*ent.PrizegivingCompetitionCreate, 0, len(params.CompetitionSessionIDs))
		for _, competitionSessionID := range params.CompetitionSessionIDs {
			builders = append(builders, transaction.transaction.PrizegivingCompetition.Create().
				SetEventID(params.EventID).
				SetPrizegivingID(found.ID).
				SetCompetitionSessionID(competitionSessionID))
		}
		if _, err = transaction.transaction.PrizegivingCompetition.CreateBulk(builders...).
			Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return PrizegivingPlan{}, ErrCompetitionPrizegivingAssignment
			}
			return PrizegivingPlan{}, opaqueError("assign Prizegiving Competitions", err)
		}
	}
	updated, err := transaction.transaction.Prizegiving.UpdateOne(found).
		Where(
			prizegiving.RevisionEQ(params.ExpectedRevision),
			prizegiving.LockedEQ(false),
		).
		SetRevision(params.ExpectedRevision + 1).
		SetCompetitionSessionIds(slices.Clone(params.CompetitionSessionIDs)).
		SetSequence(prizegivingItemValues(params.Sequence)).
		SetPublicationOrder(prizegivingItemRefValues(params.PublicationOrder)).
		SetResultsTextTemplate(prizegivingTemplateValue(params.Template)).
		Save(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingPlanRevision
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("save Prizegiving plan", err)
	}
	return storedPrizegivingPlan(updated), nil
}

type prizegivingCompetitionOrder struct {
	sessionID    int
	plannedStart time.Time
	draft        CompetitionResultsDraft
}

func (transaction *CommandTx) defaultPrizegivingOrder(
	ctx context.Context,
	params SavePrizegivingPlanParams,
) ([]PrizegivingResultItem, []PrizegivingResultItemRef, error) {
	competitions := make(
		[]prizegivingCompetitionOrder,
		0,
		len(params.CompetitionSessionIDs),
	)
	for _, sessionID := range params.CompetitionSessionIDs {
		version, err := transaction.transaction.SessionPublishedVersion.Query().
			Where(sessionpublishedversion.SessionIDEQ(sessionID)).
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if err != nil {
			return nil, nil, opaqueError(
				"load Competition order for Prizegiving",
				err,
			)
		}
		draft, err := transaction.LoadCompetitionResultsDraft(
			ctx,
			params.EventID,
			sessionID,
		)
		if err != nil {
			return nil, nil, err
		}
		competitions = append(competitions, prizegivingCompetitionOrder{
			sessionID: sessionID, plannedStart: version.PlannedStart, draft: draft,
		})
	}
	slices.SortFunc(
		competitions,
		func(first, second prizegivingCompetitionOrder) int {
			if order := first.plannedStart.Compare(second.plannedStart); order != 0 {
				return order
			}
			return cmp.Compare(first.sessionID, second.sessionID)
		},
	)
	items := make([]PrizegivingResultItem, 0, len(competitions))
	appendItem := func(kind string, sessionID int, awardKey string) {
		displayOrder := len(items) + 1
		items = append(items, PrizegivingResultItem{
			Kind: kind, CompetitionSessionID: sessionID, AwardKey: awardKey,
			DisplayOrder: displayOrder, RevealMethod: "StaticResult",
		})
	}
	for _, competition := range competitions {
		kind := "CompetitionResults"
		if competition.draft.Disposition == "NoPublicResults" {
			kind = "NoPublicResults"
		}
		appendItem(kind, competition.sessionID, "")
		for _, award := range competition.draft.Awards {
			if award.Promoted {
				appendItem("CompetitionAward", competition.sessionID, award.Key)
			}
		}
	}
	eventAwards, err := transaction.LoadEventAwardsDraft(ctx, params.EventID)
	if err != nil {
		return nil, nil, err
	}
	for _, award := range eventAwardsForPath(eventAwards.Awards, AwardReleasePath{
		Kind: "Prizegiving", PrizegivingSessionID: params.CeremonySessionID,
	}) {
		appendItem("EventAward", 0, award.Key)
	}
	publicationOrder := make([]PrizegivingResultItemRef, 0, len(items))
	for _, item := range items {
		publicationOrder = append(publicationOrder, PrizegivingResultItemRef{
			Kind: item.Kind, CompetitionSessionID: item.CompetitionSessionID,
			AwardKey: item.AwardKey, DisplayOrder: item.DisplayOrder,
		})
	}
	return items, publicationOrder, nil
}

func (transaction *CommandTx) validatePrizegivingCompetitionAssignments(
	ctx context.Context,
	found *ent.Prizegiving,
	competitionSessionIDs []int,
) error {
	seen := make(map[int]struct{}, len(competitionSessionIDs))
	for _, competitionSessionID := range competitionSessionIDs {
		if competitionSessionID <= 0 {
			return ErrCompetitionPrizegivingAssignment
		}
		if _, duplicate := seen[competitionSessionID]; duplicate {
			return ErrCompetitionPrizegivingAssignment
		}
		seen[competitionSessionID] = struct{}{}
	}
	if len(competitionSessionIDs) == 0 {
		return nil
	}
	assignedElsewhere, err := transaction.transaction.PrizegivingCompetition.Query().
		Where(
			prizegivingcompetition.CompetitionSessionIDIn(competitionSessionIDs...),
			prizegivingcompetition.PrizegivingIDNEQ(found.ID),
		).
		Exist(ctx)
	if err != nil {
		return opaqueError("check Competition Prizegiving assignment", err)
	}
	if assignedElsewhere {
		return ErrCompetitionPrizegivingAssignment
	}
	competitions, err := transaction.transaction.Session.Query().
		Where(
			session.EventIDEQ(found.EventID),
			session.IDIn(competitionSessionIDs...),
		).
		All(ctx)
	if err != nil {
		return opaqueError("load Prizegiving Competitions", err)
	}
	if len(competitions) != len(competitionSessionIDs) {
		return ErrCompetitionPrizegivingAssignment
	}
	for _, competition := range competitions {
		version, versionErr := competition.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if versionErr != nil || version.Type != sessionpublishedversion.TypeCompetition {
			return ErrCompetitionPrizegivingAssignment
		}
	}
	return nil
}

// LockPrizegivingPlan freezes one exact successful Preflight snapshot.
func (transaction *CommandTx) LockPrizegivingPlan(
	ctx context.Context,
	eventID, ceremonySessionID, expectedRevision int,
	lock PrizegivingPreflightLock,
	lockedByAccountID int,
	lockedAt time.Time,
) (PrizegivingPlan, error) {
	found, err := transaction.transaction.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(eventID),
			prizegiving.CeremonySessionIDEQ(ceremonySessionID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingSession
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("load Prizegiving plan for lock", err)
	}
	if found.Locked {
		return storedPrizegivingPlan(found), ErrPrizegivingLocked
	}
	if found.Revision != expectedRevision {
		return storedPrizegivingPlan(found), ErrPrizegivingPlanRevision
	}
	updated, err := transaction.transaction.Prizegiving.UpdateOne(found).
		Where(
			prizegiving.RevisionEQ(expectedRevision),
			prizegiving.LockedEQ(false),
		).
		SetLocked(true).
		SetPreflightLock(prizegivingLockValue(lock)).
		SetLockedByAccountID(lockedByAccountID).
		SetLockedAt(lockedAt).
		Save(ctx)
	if ent.IsNotFound(err) {
		return PrizegivingPlan{}, ErrPrizegivingPlanRevision
	}
	if err != nil {
		return PrizegivingPlan{}, opaqueError("lock Prizegiving plan", err)
	}
	return storedPrizegivingPlan(updated), nil
}

func storedPrizegivingPlan(found *ent.Prizegiving) PrizegivingPlan {
	result := PrizegivingPlan{
		ID: found.ID, EventID: found.EventID,
		CeremonySessionID: found.CeremonySessionID, Revision: found.Revision,
		CompetitionSessionIDs: slices.Clone(found.CompetitionSessionIds),
		Sequence:              prizegivingItems(found.Sequence),
		PublicationOrder:      prizegivingItemRefs(found.PublicationOrder),
		Template:              prizegivingTemplate(found.ResultsTextTemplate),
		Locked:                found.Locked,
		Lock:                  prizegivingLock(found.PreflightLock),
		CreatedByAccountID:    found.CreatedByAccountID,
		CreatedAt:             found.CreatedAt,
	}
	if found.LockedByAccountID != nil {
		result.LockedByAccountID = *found.LockedByAccountID
	}
	if found.LockedAt != nil {
		result.LockedAt = *found.LockedAt
	}
	return result
}

func prizegivingItemRefValue(value PrizegivingResultItemRef) prizegivingvalue.ItemRef {
	return prizegivingvalue.ItemRef{
		Kind: value.Kind, CompetitionSessionID: value.CompetitionSessionID,
		AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
	}
}

func prizegivingItemRefValues(values []PrizegivingResultItemRef) []prizegivingvalue.ItemRef {
	result := make([]prizegivingvalue.ItemRef, 0, len(values))
	for _, value := range values {
		result = append(result, prizegivingItemRefValue(value))
	}
	return result
}

func prizegivingItemRefs(values []prizegivingvalue.ItemRef) []PrizegivingResultItemRef {
	result := make([]PrizegivingResultItemRef, 0, len(values))
	for _, value := range values {
		result = append(result, PrizegivingResultItemRef{
			Kind: value.Kind, CompetitionSessionID: value.CompetitionSessionID,
			AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
		})
	}
	return result
}

func prizegivingItemValues(values []PrizegivingResultItem) []prizegivingvalue.Item {
	result := make([]prizegivingvalue.Item, 0, len(values))
	for _, value := range values {
		result = append(result, prizegivingvalue.Item{
			ItemRef: prizegivingItemRefValue(PrizegivingResultItemRef{
				Kind: value.Kind, CompetitionSessionID: value.CompetitionSessionID,
				AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
			}),
			RevealMethod: value.RevealMethod,
		})
	}
	return result
}

func prizegivingItems(values []prizegivingvalue.Item) []PrizegivingResultItem {
	result := make([]PrizegivingResultItem, 0, len(values))
	for _, value := range values {
		result = append(result, PrizegivingResultItem{
			Kind: value.Kind, CompetitionSessionID: value.CompetitionSessionID,
			AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
			RevealMethod: value.RevealMethod,
		})
	}
	return result
}

func prizegivingTemplateValue(
	value PrizegivingResultsTextTemplate,
) prizegivingvalue.Template {
	return prizegivingvalue.Template{Revision: value.Revision, Source: value.Source}
}

func prizegivingTemplate(
	value prizegivingvalue.Template,
) PrizegivingResultsTextTemplate {
	return PrizegivingResultsTextTemplate{Revision: value.Revision, Source: value.Source}
}

func prizegivingLockValue(value PrizegivingPreflightLock) prizegivingvalue.Lock {
	result := prizegivingvalue.Lock{
		PlanRevision:             value.PlanRevision,
		EventAwardsDraftRevision: value.EventAwardsDraftRevision,
		EventAwardsPathRevision:  value.EventAwardsPathRevision,
		PublicationOrder:         prizegivingItemRefValues(value.PublicationOrder),
		Template:                 prizegivingTemplateValue(value.Template),
	}
	for _, source := range value.CompetitionSources {
		result.CompetitionSources = append(
			result.CompetitionSources,
			prizegivingvalue.CompetitionLock{
				SessionID: source.SessionID, DraftID: source.DraftID,
				DraftRevision: source.DraftRevision, Disposition: source.Disposition,
			},
		)
	}
	for _, item := range value.Sequence {
		result.Sequence = append(result.Sequence, prizegivingvalue.LockedItem{
			Item:       prizegivingItemValues([]PrizegivingResultItem{item.PrizegivingResultItem})[0],
			RevealSeed: item.RevealSeed,
		})
	}
	return result
}

func prizegivingLock(value prizegivingvalue.Lock) PrizegivingPreflightLock {
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
				DraftRevision: source.DraftRevision, Disposition: source.Disposition,
			},
		)
	}
	for _, item := range value.Sequence {
		result.Sequence = append(result.Sequence, PrizegivingLockedResultItem{
			PrizegivingResultItem: prizegivingItems(
				[]prizegivingvalue.Item{item.Item},
			)[0],
			RevealSeed: item.RevealSeed,
		})
	}
	return result
}

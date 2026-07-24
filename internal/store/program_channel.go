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
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

var (
	// ErrProgramRevision means Program Output changed after it was observed.
	ErrProgramRevision = errors.New("program Channel revision conflict")
	// ErrProgramItem means Preview selected no current Competition Program Item.
	ErrProgramItem = errors.New("invalid Program Item")
	// ErrPrizegivingNotLive means a Result Item Take targeted a non-Live Ceremony.
	ErrPrizegivingNotLive = errors.New("Prizegiving Ceremony is not Live")
)

// ProgramItemKind identifies one built-in Competition Slide or Standby.
type ProgramItemKind string

const (
	// ProgramItemStandby identifies branded idle output.
	ProgramItemStandby ProgramItemKind = "Standby"
	// ProgramItemUpcoming identifies the pre-start Competition slide.
	ProgramItemUpcoming ProgramItemKind = "Upcoming"
	// ProgramItemStarting identifies the Competition opening slide.
	ProgramItemStarting ProgramItemKind = "Starting"
	// ProgramItemEntry identifies one Included Entry slide.
	ProgramItemEntry ProgramItemKind = "Entry"
	// ProgramItemEnding identifies the Competition closing slide.
	ProgramItemEnding ProgramItemKind = "Ending"
	// ProgramItemResult identifies one locked Prizegiving Result Item.
	ProgramItemResult ProgramItemKind = "Result"
)

// PrizegivingStageState is one Result Item's durable presentation state.
type PrizegivingStageState struct {
	Ref                  PrizegivingResultItemRef      `json:"ref"`
	Status               prizegivingvalue.StageStatus  `json:"status"`
	Release              prizegivingvalue.ReleaseState `json:"release"`
	TakenAt              time.Time                     `json:"taken_at,omitzero"`
	RevealStartedAt      time.Time                     `json:"reveal_started_at,omitzero"`
	RevealDuration       time.Duration                 `json:"reveal_duration,omitempty"`
	RevealPausedAt       time.Time                     `json:"reveal_paused_at,omitzero"`
	RevealPausedDuration time.Duration                 `json:"reveal_paused_duration,omitempty"`
	RevealCompletedAt    time.Time                     `json:"reveal_completed_at,omitzero"`
	SkippedAt            time.Time                     `json:"skipped_at,omitzero"`
}

// ProgramResult is one exact locked Prizegiving presentation source.
type ProgramResult struct {
	Ref                       PrizegivingResultItemRef      `json:"ref"`
	RevealMethod              prizegivingvalue.RevealMethod `json:"reveal_method"`
	ReducedMotionRevealMethod prizegivingvalue.RevealMethod `json:"reduced_motion_reveal_method"`
	RevealSeed                uint64                        `json:"reveal_seed"`
	Status                    prizegivingvalue.StageStatus  `json:"status"`
	Release                   prizegivingvalue.ReleaseState `json:"release"`
	TakenAt                   time.Time                     `json:"taken_at,omitzero"`
	RevealStartedAt           time.Time                     `json:"reveal_started_at,omitzero"`
	RevealDuration            time.Duration                 `json:"reveal_duration,omitempty"`
	RevealPausedAt            time.Time                     `json:"reveal_paused_at,omitzero"`
	RevealPausedDuration      time.Duration                 `json:"reveal_paused_duration,omitempty"`
	RevealCompletedAt         time.Time                     `json:"reveal_completed_at,omitzero"`
	SkippedAt                 time.Time                     `json:"skipped_at,omitzero"`
	Replay                    bool                          `json:"replay,omitempty"`
	PresentationStartedAt     time.Time                     `json:"presentation_started_at,omitzero"`
	PresentationDuration      time.Duration                 `json:"presentation_duration,omitempty"`
	CompetitionResults        CompetitionResultsDraft       `json:"competition_results,omitzero"`
	EventAward                EventAward                    `json:"event_award,omitzero"`
	ScoreBars                 []ProgramScoreBar             `json:"score_bars,omitempty"`
}

// ProgramScoreBar is one non-sensitive relative score presentation value.
type ProgramScoreBar struct {
	EntryID     int    `json:"entry_id"`
	BasisPoints uint32 `json:"basis_points"`
}

// ProgramItem is one exact selectable built-in presentation state.
type ProgramItem struct {
	Kind    ProgramItemKind `json:"kind"`
	EntryID int             `json:"entry_id,omitempty"`
	Title   string          `json:"title"`
	Retry   bool            `json:"retry,omitempty"`
	Result  *ProgramResult  `json:"result,omitempty"`
}

// SameIdentity reports whether two values name the same canonical Program Item.
func (item ProgramItem) SameIdentity(other ProgramItem) bool {
	if item.Kind != other.Kind ||
		item.EntryID != other.EntryID ||
		item.Retry != other.Retry {
		return false
	}
	if item.Kind != ProgramItemResult {
		return true
	}
	return item.Result != nil &&
		other.Result != nil &&
		item.Result.Ref == other.Result.Ref
}

// ProgramChannelState is durable Program Output plus its canonical context.
type ProgramChannelState struct {
	EventID     int           `json:"event_id"`
	SessionID   int           `json:"session_id"`
	Name        string        `json:"name"`
	Revision    int           `json:"revision"`
	LocationIDs []int         `json:"location_ids"`
	Items       []ProgramItem `json:"items"`
	Previous    ProgramItem   `json:"previous"`
	Current     ProgramItem   `json:"current"`
	Next        ProgramItem   `json:"next"`
	Output      ProgramItem   `json:"output"`
	TakenAt     time.Time     `json:"taken_at,omitzero"`
}

// TakeProgramItemParams commits one exact Preview as Program Output.
type TakeProgramItemParams struct {
	EventID, SessionID         int
	ExpectedRevision           int
	Item                       ProgramItem
	ExpectedEntryOrderRevision int
	EntryOrderFingerprint      string
	Now                        time.Time
	ResultState                *PrizegivingStageState
}

// PrizegivingPresentationRun is one server-timestamped Result presentation.
type PrizegivingPresentationRun struct {
	Replay    bool
	StartedAt time.Time
	Duration  time.Duration
}

// PrizegivingResultActionParams persists one explicit domain transition.
type PrizegivingResultActionParams struct {
	EventID, SessionID int
	ExpectedRevision   int
	Item               ProgramItem
	State              PrizegivingStageState
	Presentation       PrizegivingPresentationRun
	ObservedAt         time.Time
}

// SkipPrizegivingResultFromStageParams records one unpresented locked Result.
type SkipPrizegivingResultFromStageParams struct {
	EventID, SessionID int
	ExpectedRevision   int
	Item               ProgramItem
	State              PrizegivingStageState
}

// LoadProgramChannel returns one authorized Competition Program Channel.
func (installation *SQLite) LoadProgramChannel(
	ctx context.Context,
	eventID, sessionID int,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, installation.client, eventID, sessionID, time.Time{},
	)
}

// LoadProgramChannelAt includes Result completion derived from server time.
func (installation *SQLite) LoadProgramChannelAt(
	ctx context.Context,
	eventID, sessionID int,
	observedAt time.Time,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, installation.client, eventID, sessionID, observedAt,
	)
}

// LoadProgramChannel returns transaction-consistent Program Channel state.
func (transaction *CommandTx) LoadProgramChannel(
	ctx context.Context,
	eventID, sessionID int,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, transaction.transaction.Client(), eventID, sessionID, time.Time{},
	)
}

// LoadProgramChannelAt returns transaction-consistent, time-projected state.
func (transaction *CommandTx) LoadProgramChannelAt(
	ctx context.Context,
	eventID, sessionID int,
	observedAt time.Time,
) (ProgramChannelState, error) {
	return loadProgramChannel(
		ctx, transaction.transaction.Client(), eventID, sessionID, observedAt,
	)
}

// TakeProgramItem atomically commits Program Output and any first Entry lock.
func (transaction *CommandTx) TakeProgramItem(
	ctx context.Context,
	params TakeProgramItemParams,
) (ProgramChannelState, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return ProgramChannelState{}, err
	}
	state, err := loadProgramChannel(
		ctx, transaction.transaction.Client(), params.EventID, params.SessionID, params.Now,
	)
	if err != nil {
		return ProgramChannelState{}, err
	}
	if state.Revision != params.ExpectedRevision {
		return state, ErrProgramRevision
	}
	selected, index, ok := findProgramItem(state.Items, params.Item)
	if !ok {
		return ProgramChannelState{}, ErrProgramItem
	}
	if selected.Kind == ProgramItemEntry {
		if _, err = transaction.TakeCompetitionEntrySlide(ctx, TakeEntrySlideParams{
			EventID: params.EventID, SessionID: params.SessionID,
			ExpectedRevision:   params.ExpectedEntryOrderRevision,
			PreviewFingerprint: params.EntryOrderFingerprint,
			EntryID:            selected.EntryID, Now: params.Now,
		}); err != nil {
			return ProgramChannelState{}, err
		}
	}
	if selected.Kind == ProgramItemResult {
		if params.ResultState == nil ||
			selected.Result == nil ||
			params.ResultState.Ref != selected.Result.Ref {
			return ProgramChannelState{}, ErrProgramItem
		}
		ceremony, loadErr := transaction.transaction.Session.Get(ctx, params.SessionID)
		if loadErr != nil {
			return ProgramChannelState{}, opaqueError(
				"load Prizegiving Ceremony before Take",
				loadErr,
			)
		}
		if ceremony.Lifecycle != session.LifecycleLive {
			return ProgramChannelState{}, ErrPrizegivingNotLive
		}
		if updateErr := transaction.savePrizegivingStageState(
			ctx,
			params.EventID,
			params.SessionID,
			params.ExpectedRevision,
			*params.ResultState,
		); updateErr != nil {
			return ProgramChannelState{}, updateErr
		}
	}
	cursor := stateCursor(state)
	if index == cursor+1 {
		cursor = index
	}
	update := transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(session.ProgramOutputRevisionEQ(params.ExpectedRevision)).
		SetProgramOutputKind(session.ProgramOutputKind(selected.Kind)).
		SetProgramOutputRevision(params.ExpectedRevision + 1).
		SetProgramCursor(cursor).
		SetProgramOutputTakenAt(params.Now)
	switch selected.Kind {
	case ProgramItemEntry:
		update.SetProgramOutputEntryID(selected.EntryID)
		update.SetProgramOutputResult(prizegivingvalue.ProgramOutput{})
	case ProgramItemResult:
		update.ClearProgramOutputEntryID()
		update.SetProgramOutputResult(prizegivingvalue.ProgramOutput{
			ItemRef: prizegivingItemRefValue(selected.Result.Ref),
		})
	default:
		update.ClearProgramOutputEntryID()
		update.SetProgramOutputResult(prizegivingvalue.ProgramOutput{})
	}
	_, err = update.Save(ctx)
	if ent.IsNotFound(err) {
		return state, ErrProgramRevision
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError("commit Program Output", err)
	}
	return loadProgramChannel(
		ctx, transaction.transaction.Client(), params.EventID, params.SessionID, params.Now,
	)
}

// ApplyPrizegivingResultAction atomically persists stage and Program Output state.
func (transaction *CommandTx) ApplyPrizegivingResultAction(
	ctx context.Context,
	params PrizegivingResultActionParams,
) (ProgramChannelState, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return ProgramChannelState{}, err
	}
	current, err := transaction.LoadProgramChannelAt(
		ctx,
		params.EventID,
		params.SessionID,
		params.ObservedAt,
	)
	if err != nil {
		return ProgramChannelState{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return current, ErrProgramRevision
	}
	if current.Output.Kind != ProgramItemResult ||
		current.Output.Result == nil ||
		params.Item.Kind != ProgramItemResult ||
		params.Item.Result == nil ||
		current.Output.Result.Ref != params.Item.Result.Ref ||
		params.State.Ref != params.Item.Result.Ref {
		return ProgramChannelState{}, ErrProgramItem
	}
	if updateErr := transaction.savePrizegivingStageState(
		ctx,
		params.EventID,
		params.SessionID,
		params.ExpectedRevision,
		params.State,
	); updateErr != nil {
		return ProgramChannelState{}, updateErr
	}
	_, err = transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(
			session.EventIDEQ(params.EventID),
			session.ProgramOutputRevisionEQ(params.ExpectedRevision),
			session.ProgramOutputKindEQ(session.ProgramOutputKindResult),
		).
		SetProgramOutputRevision(params.ExpectedRevision + 1).
		SetProgramOutputResult(prizegivingvalue.ProgramOutput{
			ItemRef:       prizegivingItemRefValue(params.State.Ref),
			Replay:        params.Presentation.Replay,
			StartedAt:     params.Presentation.StartedAt,
			DurationNanos: int64(params.Presentation.Duration),
		}).
		Save(ctx)
	if ent.IsNotFound(err) {
		return current, ErrProgramRevision
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError(
			"persist Prizegiving Result presentation",
			err,
		)
	}
	return transaction.LoadProgramChannelAt(
		ctx, params.EventID, params.SessionID, params.ObservedAt,
	)
}

// SkipPrizegivingResultFromStage advances past one unpresented Result while
// preserving the current Program Output.
func (transaction *CommandTx) SkipPrizegivingResultFromStage(
	ctx context.Context,
	params SkipPrizegivingResultFromStageParams,
) (ProgramChannelState, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return ProgramChannelState{}, err
	}
	current, err := transaction.LoadProgramChannel(
		ctx,
		params.EventID,
		params.SessionID,
	)
	if err != nil {
		return ProgramChannelState{}, err
	}
	if current.Revision != params.ExpectedRevision {
		return current, ErrProgramRevision
	}
	selected, index, ok := findProgramItem(current.Items, params.Item)
	if !ok ||
		selected.Kind != ProgramItemResult ||
		selected.Result == nil ||
		params.State.Ref != selected.Result.Ref ||
		!current.Next.SameIdentity(selected) {
		return ProgramChannelState{}, ErrProgramItem
	}
	if updateErr := transaction.savePrizegivingStageState(
		ctx,
		params.EventID,
		params.SessionID,
		params.ExpectedRevision,
		params.State,
	); updateErr != nil {
		return ProgramChannelState{}, updateErr
	}
	_, err = transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(
			session.EventIDEQ(params.EventID),
			session.ProgramOutputRevisionEQ(params.ExpectedRevision),
		).
		SetProgramOutputRevision(params.ExpectedRevision + 1).
		SetProgramCursor(index).
		Save(ctx)
	if ent.IsNotFound(err) {
		return current, ErrProgramRevision
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError(
			"skip Prizegiving Result from stage",
			err,
		)
	}
	return transaction.LoadProgramChannel(ctx, params.EventID, params.SessionID)
}

func loadProgramChannel(
	ctx context.Context,
	client *ent.Client,
	eventID, sessionID int,
	observedAt time.Time,
) (ProgramChannelState, error) {
	found, err := client.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ProgramChannelState{}, ErrProgramItem
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Session", err)
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Session version", err)
	}
	switch version.Type {
	case sessionpublishedversion.TypeCompetition:
		return loadCompetitionProgramChannel(ctx, client, found, version)
	case sessionpublishedversion.TypeCeremony:
		return loadPrizegivingProgramChannel(
			ctx, client, found, version, observedAt,
		)
	default:
		return ProgramChannelState{}, ErrProgramItem
	}
}

func loadCompetitionProgramChannel(
	ctx context.Context,
	client *ent.Client,
	found *ent.Session,
	version *ent.SessionPublishedVersion,
) (ProgramChannelState, error) {
	entries, err := client.CompetitionEntry.Query().
		Where(
			competitionentry.CompetitionSessionIDEQ(found.ID),
			competitionentry.DispositionEQ(competitionentry.DispositionIncluded),
		).
		Order(ent.Asc(competitionentry.FieldCreatedAt), ent.Asc(competitionentry.FieldID)).
		All(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Entries", err)
	}
	order, _, err := competitionEntryOrder(found, entries)
	if err != nil {
		return ProgramChannelState{}, err
	}
	items := competitionProgramItems(version.Title, order.EntryIDs, entries)
	locationIDs, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError("load Program Channel Locations", err)
	}
	state := ProgramChannelState{
		EventID: found.EventID, SessionID: found.ID, Name: version.Title,
		Revision: found.ProgramOutputRevision, LocationIDs: locationIDs, Items: items,
		Output: ProgramItem{
			Kind:    ProgramItemKind(found.ProgramOutputKind.String()),
			EntryID: valueOrZero(found.ProgramOutputEntryID),
		},
		TakenAt: found.ProgramOutputTakenAt,
	}
	state.Output.Title = programItemTitle(state.Output, version.Title, entries)
	setProgramContext(&state, found.ProgramCursor)
	return state, nil
}

func loadPrizegivingProgramChannel(
	ctx context.Context,
	client *ent.Client,
	found *ent.Session,
	version *ent.SessionPublishedVersion,
	observedAt time.Time,
) (ProgramChannelState, error) {
	internalContext := systemContext(ctx)
	plan, err := client.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(found.EventID),
			prizegiving.CeremonySessionIDEQ(found.ID),
			prizegiving.LockedEQ(true),
		).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return ProgramChannelState{}, ErrProgramItem
	}
	if err != nil {
		return ProgramChannelState{}, opaqueError(
			"load locked Prizegiving Program Channel",
			err,
		)
	}
	states := make(
		map[PrizegivingResultItemRef]PrizegivingStageState,
		len(plan.ItemStates),
	)
	for _, value := range plan.ItemStates {
		if !observedAt.IsZero() {
			var transitions []prizegivingvalue.RevealCoverageTransition
			if !value.RevealPausedAt.IsZero() {
				var coverageErr error
				transitions, coverageErr = prizegivingRevealCoverageTransitions(
					internalContext,
					client,
					found.EventID,
					found.ID,
					value.RevealPausedAt,
					observedAt,
				)
				if coverageErr != nil {
					return ProgramChannelState{}, coverageErr
				}
			}
			value = prizegivingvalue.ReconcileRevealCoverageState(
				value,
				transitions,
				observedAt,
			)
		}
		state := prizegivingStageState(value)
		states[state.Ref] = state
	}
	var eventAwards []EventAward
	if plan.PreflightLock.EventAwardsDraftRevision > 0 {
		draft, loadErr := client.EventAwardsDraft.Query().
			Where(
				eventawardsdraft.EventIDEQ(found.EventID),
				eventawardsdraft.RevisionEQ(
					plan.PreflightLock.EventAwardsDraftRevision,
				),
			).
			Only(internalContext)
		if loadErr != nil {
			return ProgramChannelState{}, opaqueError(
				"load locked Prizegiving Event Awards",
				loadErr,
			)
		}
		eventAwards = eventAwardsForPath(
			eventAwardsDraft(draft).Awards,
			AwardReleasePath{
				Kind: "Prizegiving", PrizegivingSessionID: found.ID,
			},
		)
	}
	items := make(
		[]ProgramItem,
		0,
		len(plan.PreflightLock.Sequence),
	)
	for _, locked := range plan.PreflightLock.Sequence {
		item, loadErr := loadPrizegivingProgramItem(
			internalContext,
			client,
			version.Title,
			plan.PreflightLock.CompetitionSources,
			eventAwards,
			states,
			locked,
		)
		if loadErr != nil {
			return ProgramChannelState{}, loadErr
		}
		items = append(items, item)
	}
	state := ProgramChannelState{
		EventID: found.EventID, SessionID: found.ID, Name: version.Title,
		Revision: found.ProgramOutputRevision, Items: items,
		TakenAt: found.ProgramOutputTakenAt,
	}
	locationIDs, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return ProgramChannelState{}, opaqueError(
			"load Prizegiving Program Channel Locations",
			err,
		)
	}
	state.LocationIDs = locationIDs
	if found.ProgramOutputKind == session.ProgramOutputKindResult {
		wanted := ProgramItem{
			Kind: ProgramItemResult,
			Result: &ProgramResult{
				Ref: prizegivingItemRef(found.ProgramOutputResult.ItemRef),
			},
		}
		if output, _, ok := findProgramItem(items, wanted); ok {
			output.Result.Replay = found.ProgramOutputResult.Replay
			presentationStartedAt := found.ProgramOutputResult.StartedAt
			if !output.Result.Replay {
				presentationStartedAt = presentationStartedAt.Add(
					output.Result.RevealPausedDuration,
				)
				if !observedAt.IsZero() &&
					!output.Result.RevealPausedAt.IsZero() &&
					observedAt.After(output.Result.RevealPausedAt) {
					presentationStartedAt = presentationStartedAt.Add(
						observedAt.Sub(output.Result.RevealPausedAt),
					)
				}
			}
			output.Result.PresentationStartedAt = presentationStartedAt
			output.Result.PresentationDuration = time.Duration(
				found.ProgramOutputResult.DurationNanos,
			)
			state.Output = output
		}
	} else {
		state.Output = ProgramItem{
			Kind:    ProgramItemKind(found.ProgramOutputKind.String()),
			EntryID: valueOrZero(found.ProgramOutputEntryID),
		}
		state.Output.Title = programItemTitle(state.Output, version.Title, nil)
	}
	setProgramContext(&state, found.ProgramCursor)
	return state, nil
}

func loadPrizegivingProgramItem(
	ctx context.Context,
	client *ent.Client,
	ceremonyTitle string,
	sources []prizegivingvalue.CompetitionLock,
	eventAwards []EventAward,
	states map[PrizegivingResultItemRef]PrizegivingStageState,
	locked prizegivingvalue.LockedItem,
) (ProgramItem, error) {
	ref := prizegivingItemRef(locked.ItemRef)
	result := ProgramResult{
		Ref: ref, RevealMethod: locked.RevealMethod,
		ReducedMotionRevealMethod: prizegivingvalue.RevealStatic,
		RevealSeed:                locked.RevealSeed,
		Status:                    prizegivingvalue.StagePending,
		Release:                   prizegivingvalue.ReleaseHeld,
	}
	if state, ok := states[ref]; ok {
		result.Status = state.Status
		result.Release = state.Release
		result.TakenAt = state.TakenAt
		result.RevealStartedAt = state.RevealStartedAt
		result.RevealDuration = state.RevealDuration
		result.RevealPausedAt = state.RevealPausedAt
		result.RevealPausedDuration = state.RevealPausedDuration
		result.RevealCompletedAt = state.RevealCompletedAt
		result.SkippedAt = state.SkippedAt
	}
	title := prizegivingResultTitle(ref, ceremonyTitle)
	switch ref.Kind {
	case prizegivingvalue.ItemCompetitionResults,
		prizegivingvalue.ItemNoPublicResults,
		prizegivingvalue.ItemCompetitionAward:
		source, ok := findPrizegivingCompetitionSource(
			sources,
			ref.CompetitionSessionID,
		)
		if !ok {
			return ProgramItem{}, ErrProgramItem
		}
		lockedResults, err := loadCompetitionResultsDraftByID(
			ctx,
			client,
			source.DraftID,
		)
		if err != nil {
			return ProgramItem{}, err
		}
		if locked.RevealMethod == prizegivingvalue.RevealAnimatedScoreBars {
			result.ScoreBars = programScoreBars(lockedResults)
		}
		result.CompetitionResults = programCompetitionResults(lockedResults, ref)
		title = prizegivingCompetitionResultTitle(result, title)
	case prizegivingvalue.ItemEventAward:
		award, ok := findPrizegivingEventAward(eventAwards, ref.AwardKey)
		if !ok {
			return ProgramItem{}, ErrProgramItem
		}
		result.EventAward = award
		title = result.EventAward.Name
	default:
		return ProgramItem{}, ErrProgramItem
	}
	return ProgramItem{
		Kind: ProgramItemResult, Title: title, Result: &result,
	}, nil
}

func (transaction *CommandTx) savePrizegivingStageState(
	ctx context.Context,
	eventID, ceremonySessionID, expectedRevision int,
	state PrizegivingStageState,
) error {
	found, err := transaction.transaction.Prizegiving.Query().
		Where(
			prizegiving.EventIDEQ(eventID),
			prizegiving.CeremonySessionIDEQ(ceremonySessionID),
			prizegiving.LockedEQ(true),
			prizegiving.OperationRevisionEQ(expectedRevision),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ErrProgramRevision
	}
	if err != nil {
		return opaqueError("load Prizegiving stage state for update", err)
	}
	states := slices.Clone(found.ItemStates)
	replaced := false
	for index, value := range states {
		if value.ItemRef == prizegivingItemRefValue(state.Ref) {
			states[index] = prizegivingStageStateValue(state)
			replaced = true
			break
		}
	}
	if !replaced {
		states = append(states, prizegivingStageStateValue(state))
	}
	_, err = transaction.transaction.Prizegiving.UpdateOne(found).
		Where(prizegiving.OperationRevisionEQ(expectedRevision)).
		AddOperationRevision(1).
		SetItemStates(states).
		Save(ctx)
	if ent.IsNotFound(err) {
		return ErrProgramRevision
	}
	if err != nil {
		return opaqueError("save Prizegiving stage state", err)
	}
	return nil
}

func prizegivingStageState(
	value prizegivingvalue.StageState,
) PrizegivingStageState {
	return PrizegivingStageState{
		Ref: prizegivingItemRef(value.ItemRef), Status: value.Status,
		Release: value.Release,
		TakenAt: value.TakenAt, RevealStartedAt: value.RevealStartedAt,
		RevealDuration:       time.Duration(value.RevealDurationNanos),
		RevealPausedAt:       value.RevealPausedAt,
		RevealPausedDuration: time.Duration(value.RevealPausedNanos),
		RevealCompletedAt:    value.RevealCompletedAt, SkippedAt: value.SkippedAt,
	}
}

func prizegivingItemRef(value prizegivingvalue.ItemRef) PrizegivingResultItemRef {
	return PrizegivingResultItemRef{
		Kind: value.Kind, CompetitionSessionID: value.CompetitionSessionID,
		AwardKey: value.AwardKey, DisplayOrder: value.DisplayOrder,
	}
}

func prizegivingStageStateValue(
	value PrizegivingStageState,
) prizegivingvalue.StageState {
	return prizegivingvalue.StageState{
		ItemRef: prizegivingItemRefValue(value.Ref), Status: value.Status,
		Release: value.Release,
		TakenAt: value.TakenAt, RevealStartedAt: value.RevealStartedAt,
		RevealDurationNanos: int64(value.RevealDuration),
		RevealPausedAt:      value.RevealPausedAt,
		RevealPausedNanos:   int64(value.RevealPausedDuration),
		RevealCompletedAt:   value.RevealCompletedAt, SkippedAt: value.SkippedAt,
	}
}

func findPrizegivingCompetitionSource(
	values []prizegivingvalue.CompetitionLock,
	sessionID int,
) (prizegivingvalue.CompetitionLock, bool) {
	for _, value := range values {
		if value.SessionID == sessionID {
			return value, true
		}
	}
	return prizegivingvalue.CompetitionLock{}, false
}

func findPrizegivingEventAward(
	values []EventAward,
	key string,
) (EventAward, bool) {
	for _, value := range values {
		if value.Key == key {
			return value, true
		}
	}
	return EventAward{}, false
}

func prizegivingResultTitle(ref PrizegivingResultItemRef, ceremonyTitle string) string {
	if ref.AwardKey != "" {
		return ref.AwardKey
	}
	return ceremonyTitle + " results"
}

func prizegivingCompetitionResultTitle(
	result ProgramResult,
	fallback string,
) string {
	if result.Ref.Kind != "CompetitionAward" {
		return fallback
	}
	for _, award := range result.CompetitionResults.Awards {
		if award.Key == result.Ref.AwardKey {
			return award.Name
		}
	}
	return fallback
}

func competitionProgramItems(
	title string,
	entryOrder []int,
	entries []*ent.CompetitionEntry,
) []ProgramItem {
	items := []ProgramItem{
		{Kind: ProgramItemUpcoming, Title: title + " upcoming"},
		{Kind: ProgramItemStarting, Title: title + " starting"},
	}
	for _, entryID := range entryOrder {
		item := ProgramItem{Kind: ProgramItemEntry, EntryID: entryID}
		item.Title = programItemTitle(item, title, entries)
		items = append(items, item)
	}
	for _, entry := range deferredEntries(entries) {
		item := ProgramItem{Kind: ProgramItemEntry, EntryID: entry.ID, Retry: true}
		item.Title = programItemTitle(item, title, entries)
		items = append(items, item)
	}
	return append(items,
		ProgramItem{Kind: ProgramItemEnding, Title: title + " ending"},
		ProgramItem{Kind: ProgramItemStandby, Title: "Standby"},
	)
}

func programItemTitle(item ProgramItem, competitionTitle string, entries []*ent.CompetitionEntry) string {
	switch item.Kind {
	case ProgramItemStandby:
		return "Standby"
	case ProgramItemUpcoming:
		return competitionTitle + " upcoming"
	case ProgramItemStarting:
		return competitionTitle + " starting"
	case ProgramItemEnding:
		return competitionTitle + " ending"
	case ProgramItemEntry:
		for _, entry := range entries {
			if entry.ID == item.EntryID {
				return entry.Name
			}
		}
	case ProgramItemResult:
		return item.Title
	}
	return ""
}

func findProgramItem(items []ProgramItem, wanted ProgramItem) (ProgramItem, int, bool) {
	for index, item := range items {
		if item.SameIdentity(wanted) {
			return item, index, true
		}
	}
	return ProgramItem{}, 0, false
}

func stateCursor(state ProgramChannelState) int {
	for index, item := range state.Items {
		if item.SameIdentity(state.Current) {
			return index
		}
	}
	return -1
}

func setProgramContext(state *ProgramChannelState, cursor int) {
	if cursor >= 0 && cursor < len(state.Items) {
		state.Current = state.Items[cursor]
	}
	if cursor > 0 && cursor-1 < len(state.Items) {
		state.Previous = state.Items[cursor-1]
	}
	if cursor+1 >= 0 && cursor+1 < len(state.Items) {
		state.Next = state.Items[cursor+1]
	}
}

func deferredEntries(entries []*ent.CompetitionEntry) []*ent.CompetitionEntry {
	deferred := make([]*ent.CompetitionEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.DeferredSequence > 0 {
			deferred = append(deferred, entry)
		}
	}
	slices.SortFunc(deferred, func(left, right *ent.CompetitionEntry) int {
		return cmp.Compare(left.DeferredSequence, right.DeferredSequence)
	})
	return deferred
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

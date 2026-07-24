package results

import (
	"errors"
	"time"

	"github.com/dotwaffle/beamers/internal/prizegivingvalue"
)

var (
	// ErrResultItemTransition means an action is invalid for the current stage state.
	ErrResultItemTransition = errors.New("invalid Prizegiving Result Item transition")
	// ErrResultRevealRunning means a timed Reveal has not reached its duration.
	ErrResultRevealRunning = errors.New("Prizegiving Result Reveal is still running")
)

// ResultItemStageStatus is one Result Item's monotonic stage outcome.
type ResultItemStageStatus = prizegivingvalue.StageStatus

const (
	// ResultItemPending has not entered Program Output.
	ResultItemPending = prizegivingvalue.StagePending
	// ResultItemTaken is unrevealed in Program Output.
	ResultItemTaken = prizegivingvalue.StageTaken
	// ResultItemRevealing is running a timed presentation.
	ResultItemRevealing = prizegivingvalue.StageRevealing
	// ResultItemRevealed reached its immutable final truth.
	ResultItemRevealed = prizegivingvalue.StageRevealed
	// ResultItemSkipped was deliberately omitted from stage presentation.
	ResultItemSkipped = prizegivingvalue.StageSkipped
)

// ResultReleaseState describes when a resolved item becomes publishable.
type ResultReleaseState = prizegivingvalue.ReleaseState

const (
	// ResultReleaseHeld keeps an unresolved item out of publication.
	ResultReleaseHeld = prizegivingvalue.ReleaseHeld
	// ResultReleaseReady permits the configured reveal policy to publish it.
	ResultReleaseReady = prizegivingvalue.ReleaseReady
	// ResultReleaseCeremonyEnd queues a stage-skipped item for normal completion.
	ResultReleaseCeremonyEnd = prizegivingvalue.ReleaseCeremonyEnd
)

// ResultItemStageState is one durable Result Item presentation state.
type ResultItemStageState struct {
	Ref               ResultItemRef
	Status            ResultItemStageStatus
	Release           ResultReleaseState
	TakenAt           time.Time
	RevealStartedAt   time.Time
	RevealDuration    time.Duration
	RevealCompletedAt time.Time
	SkippedAt         time.Time
}

// ResultPresentation describes one deterministic Reveal run.
type ResultPresentation struct {
	Item                LockedResultItem
	Method              RevealMethod
	ReducedMotionMethod RevealMethod
	RevealSeed          uint64
	StartedAt           time.Time
	Duration            time.Duration
}

// TakePrizegivingResultItem places one unrevealed locked item into Program Output.
func TakePrizegivingResultItem(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) (ResultItemStageState, error) {
	if current.Status != "" && current.Status != ResultItemPending {
		return current, ErrResultItemTransition
	}
	next := ResultItemStageState{
		Ref: item.Ref(item.DisplayOrder), Status: ResultItemTaken,
		Release: ResultReleaseHeld, TakenAt: now,
	}
	return next, nil
}

// StartPrizegivingReveal starts one deterministic presentation of locked truth.
func StartPrizegivingReveal(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) (ResultItemStageState, ResultPresentation, error) {
	if current.Status != ResultItemTaken ||
		current.Ref != item.Ref(item.DisplayOrder) {
		return current, ResultPresentation{}, ErrResultItemTransition
	}
	duration, ok := revealDuration(item.RevealMethod)
	if !ok {
		return current, ResultPresentation{}, ErrResultItemTransition
	}
	next := current
	next.Status = ResultItemRevealing
	next.RevealStartedAt = now
	next.RevealDuration = duration
	if duration == 0 {
		next.Status = ResultItemRevealed
		next.Release = ResultReleaseReady
		next.RevealCompletedAt = now
	}
	return next, ResultPresentation{
		Item: item, Method: item.RevealMethod,
		ReducedMotionMethod: RevealStatic, RevealSeed: item.RevealSeed,
		StartedAt: now, Duration: duration,
	}, nil
}

// SkipPrizegivingResultToFinal immediately reaches immutable final truth.
func SkipPrizegivingResultToFinal(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) (ResultItemStageState, error) {
	if current.Ref != item.Ref(item.DisplayOrder) ||
		current.Status != ResultItemTaken &&
			current.Status != ResultItemRevealing {
		return current, ErrResultItemTransition
	}
	next := current
	next.Status = ResultItemRevealed
	next.Release = ResultReleaseReady
	if next.RevealStartedAt.IsZero() {
		next.RevealStartedAt = now
	}
	next.RevealCompletedAt = now
	return next, nil
}

// SkipPrizegivingResultFromStage records deliberate omission without revealing.
func SkipPrizegivingResultFromStage(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) (ResultItemStageState, error) {
	if current.Status != "" && current.Status != ResultItemPending {
		return current, ErrResultItemTransition
	}
	return ResultItemStageState{
		Ref: item.Ref(item.DisplayOrder), Status: ResultItemSkipped,
		Release: ResultReleaseCeremonyEnd, SkippedAt: now,
	}, nil
}

// AdvanceElapsedPrizegivingReveal derives deterministic server-time completion.
func AdvanceElapsedPrizegivingReveal(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) ResultItemStageState {
	if current.Ref != item.Ref(item.DisplayOrder) {
		return current
	}
	effective := (prizegivingvalue.StageState{
		Status:              current.Status,
		Release:             current.Release,
		RevealStartedAt:     current.RevealStartedAt,
		RevealDurationNanos: int64(current.RevealDuration),
		RevealCompletedAt:   current.RevealCompletedAt,
	}).EffectiveAt(now)
	current.Status = effective.Status
	current.Release = effective.Release
	current.RevealCompletedAt = effective.RevealCompletedAt
	return current
}

// ReplayPrizegivingReveal reruns presentation without changing canonical state.
func ReplayPrizegivingReveal(
	item LockedResultItem,
	current ResultItemStageState,
	now time.Time,
) (ResultItemStageState, ResultPresentation, error) {
	if current.Status != ResultItemRevealed ||
		current.Ref != item.Ref(item.DisplayOrder) {
		return current, ResultPresentation{}, ErrResultItemTransition
	}
	duration, ok := revealDuration(item.RevealMethod)
	if !ok {
		return current, ResultPresentation{}, ErrResultItemTransition
	}
	return current, ResultPresentation{
		Item: item, Method: item.RevealMethod,
		ReducedMotionMethod: RevealStatic, RevealSeed: item.RevealSeed,
		StartedAt: now, Duration: duration,
	}, nil
}

// UnresolvedPrizegivingResultItems lists canonical items not revealed or skipped.
func UnresolvedPrizegivingResultItems(
	sequence []LockedResultItem,
	states []ResultItemStageState,
) []ResultItemRef {
	byRef := make(map[ResultItemRef]ResultItemStageStatus, len(states))
	for _, state := range states {
		byRef[state.Ref] = state.Status
	}
	unresolved := make([]ResultItemRef, 0)
	for _, item := range sequence {
		ref := item.Ref(item.DisplayOrder)
		status := byRef[ref]
		if status != ResultItemRevealed && status != ResultItemSkipped {
			unresolved = append(unresolved, ref)
		}
	}
	return unresolved
}

func revealDuration(method RevealMethod) (time.Duration, bool) {
	switch method {
	case RevealStatic:
		return 0, true
	case RevealSequentialPodium:
		return 3 * time.Second, true
	case RevealAnimatedScoreBars:
		return 5 * time.Second, true
	default:
		return 0, false
	}
}

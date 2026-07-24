// Package prizegivingvalue defines persisted Prizegiving plan value objects.
package prizegivingvalue

import "time"

// ReleasePolicy selects when locked Result Items become public.
type ReleasePolicy string

// Results release policies.
const (
	ReleaseAllAtCue            ReleasePolicy = "AllAtCue"
	ReleaseProgressiveOnReveal ReleasePolicy = "ProgressiveOnReveal"
	ReleaseAtCeremonyEnd       ReleasePolicy = "AtCeremonyEnd"
	ReleaseStandalone          ReleasePolicy = "Standalone"
)

// ItemKind identifies one persisted Result Item source.
type ItemKind string

// Persisted Result Item kinds.
const (
	ItemCompetitionResults ItemKind = "CompetitionResults"
	ItemNoPublicResults    ItemKind = "NoPublicResults"
	ItemCompetitionAward   ItemKind = "CompetitionAward"
	ItemEventAward         ItemKind = "EventAward"
)

// RevealMethod identifies one persisted presentation algorithm.
type RevealMethod string

// Persisted reveal algorithms.
const (
	RevealStatic            RevealMethod = "StaticResult"
	RevealSequentialPodium  RevealMethod = "SequentialPodium"
	RevealAnimatedScoreBars RevealMethod = "AnimatedScoreBars"
)

// StageStatus identifies one persisted Result presentation state.
type StageStatus string

// Persisted Result presentation states.
const (
	StagePending   StageStatus = "Pending"
	StageTaken     StageStatus = "Taken"
	StageRevealing StageStatus = "Revealing"
	StageRevealed  StageStatus = "Revealed"
	StageSkipped   StageStatus = "Skipped"
)

// ReleaseState identifies a Result Item's downstream publication eligibility.
type ReleaseState string

// Persisted downstream publication eligibility states.
const (
	ReleaseHeld        ReleaseState = "Held"
	ReleaseReady       ReleaseState = "Ready"
	ReleaseCeremonyEnd ReleaseState = "CeremonyEnd"
)

// ItemRef identifies one ordered Result Item.
type ItemRef struct {
	Kind                 ItemKind `json:"kind"`
	CompetitionSessionID int      `json:"competition_session_id,omitempty"`
	AwardKey             string   `json:"award_key,omitempty"`
	DisplayOrder         int      `json:"display_order"`
}

// Item adds presentation configuration to one Result Item.
type Item struct {
	ItemRef
	RevealMethod RevealMethod `json:"reveal_method"`
}

// Template is one exact Results Text Template revision.
type Template struct {
	Revision int    `json:"revision"`
	Source   string `json:"source"`
}

// CompetitionLock identifies one exact immutable Competition Results source.
type CompetitionLock struct {
	SessionID     int    `json:"session_id"`
	DraftID       int    `json:"draft_id"`
	DraftRevision int    `json:"draft_revision"`
	Disposition   string `json:"disposition"`
}

// LockedItem binds one staged item to its reproducible Reveal Seed.
type LockedItem struct {
	Item
	RevealSeed uint64 `json:"reveal_seed"`
}

// Lock is one complete successful Prizegiving Preflight snapshot.
type Lock struct {
	PlanRevision             int               `json:"plan_revision"`
	ReleasePolicy            ReleasePolicy     `json:"release_policy"`
	CompetitionSources       []CompetitionLock `json:"competition_sources"`
	EventAwardsDraftRevision int               `json:"event_awards_draft_revision"`
	EventAwardsPathRevision  int               `json:"event_awards_path_revision"`
	Sequence                 []LockedItem      `json:"sequence"`
	PublicationOrder         []ItemRef         `json:"publication_order"`
	Template                 Template          `json:"template"`
}

// StageState is one Result Item's durable presentation outcome.
type StageState struct {
	ItemRef
	Status              StageStatus  `json:"status"`
	Release             ReleaseState `json:"release"`
	TakenAt             time.Time    `json:"taken_at,omitzero"`
	RevealStartedAt     time.Time    `json:"reveal_started_at,omitzero"`
	RevealDurationNanos int64        `json:"reveal_duration_nanos,omitempty"`
	RevealPausedAt      time.Time    `json:"reveal_paused_at,omitzero"`
	RevealPausedNanos   int64        `json:"reveal_paused_nanos,omitempty"`
	RevealCompletedAt   time.Time    `json:"reveal_completed_at,omitzero"`
	SkippedAt           time.Time    `json:"skipped_at,omitzero"`
}

// WithRevealPaused applies full Replace Override coverage to an active Reveal.
func (state StageState) WithRevealPaused(paused bool, now time.Time) StageState {
	if state.Status != StageRevealing {
		return state
	}
	if paused {
		if state.RevealPausedAt.IsZero() {
			state.RevealPausedAt = now
		}
		return state
	}
	if state.RevealPausedAt.IsZero() {
		return state
	}
	if now.After(state.RevealPausedAt) {
		state.RevealPausedNanos += int64(now.Sub(state.RevealPausedAt))
	}
	state.RevealPausedAt = time.Time{}
	return state
}

// EffectiveAt derives completion of a timed Reveal from durable server facts.
func (state StageState) EffectiveAt(now time.Time) StageState {
	if state.Status != StageRevealing ||
		state.RevealStartedAt.IsZero() ||
		!state.RevealPausedAt.IsZero() ||
		now.Before(
			state.RevealStartedAt.
				Add(time.Duration(state.RevealDurationNanos)).
				Add(time.Duration(state.RevealPausedNanos)),
		) {
		return state
	}
	state.Status = StageRevealed
	state.Release = ReleaseReady
	state.RevealCompletedAt = state.RevealStartedAt.Add(
		time.Duration(state.RevealDurationNanos),
	).Add(time.Duration(state.RevealPausedNanos))
	return state
}

// ProgramOutput identifies one Result Item presentation run.
type ProgramOutput struct {
	ItemRef
	Replay        bool      `json:"replay,omitempty"`
	StartedAt     time.Time `json:"started_at,omitzero"`
	DurationNanos int64     `json:"duration_nanos,omitempty"`
}

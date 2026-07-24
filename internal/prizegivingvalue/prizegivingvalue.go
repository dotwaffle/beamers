// Package prizegivingvalue defines persisted Prizegiving plan value objects.
package prizegivingvalue

// ItemRef identifies one ordered Result Item.
type ItemRef struct {
	Kind                 string `json:"kind"`
	CompetitionSessionID int    `json:"competition_session_id,omitempty"`
	AwardKey             string `json:"award_key,omitempty"`
	DisplayOrder         int    `json:"display_order"`
}

// Item adds presentation configuration to one Result Item.
type Item struct {
	ItemRef
	RevealMethod string `json:"reveal_method"`
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
	CompetitionSources       []CompetitionLock `json:"competition_sources"`
	EventAwardsDraftRevision int               `json:"event_awards_draft_revision"`
	EventAwardsPathRevision  int               `json:"event_awards_path_revision"`
	Sequence                 []LockedItem      `json:"sequence"`
	PublicationOrder         []ItemRef         `json:"publication_order"`
	Template                 Template          `json:"template"`
}

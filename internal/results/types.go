package results

import "time"

// Disposition controls whether a Competition will publish Results.
type Disposition string

const (
	// Pending means the Competition Results decision is unresolved.
	Pending Disposition = "Pending"
	// Publish means the reviewed Results will become public.
	Publish Disposition = "Publish"
	// NoPublicResults records deliberate non-publication.
	NoPublicResults Disposition = "NoPublicResults"
)

// ScoreType is one exact canonical score representation.
type ScoreType string

const (
	// None means a Competition does not use scores.
	None ScoreType = "None"
	// Decimal stores exact base-10 values.
	Decimal ScoreType = "Decimal"
	// Duration stores exact elapsed durations.
	Duration ScoreType = "Duration"
)

// ScorePolicy defines one Competition's score representation.
type ScorePolicy struct {
	Type           ScoreType           `json:"type"`
	Visibility     ScoreVisibility     `json:"visibility"`
	Unit           string              `json:"unit,omitempty"`
	Precision      int                 `json:"precision"`
	Requirement    ScoreRequirement    `json:"requirement"`
	Interpretation ScoreInterpretation `json:"interpretation"`
}

// ScoreVisibility controls whether exact Scores become public.
type ScoreVisibility string

const (
	// ScorePublic permits exact Scores in public Results.
	ScorePublic ScoreVisibility = "Public"
	// ScoreCrewOnly keeps exact Scores out of public Results.
	ScoreCrewOnly ScoreVisibility = "CrewOnly"
)

// ScoreRequirement controls whether every eligible Entry needs a Score.
type ScoreRequirement string

const (
	// ScoreOptional permits an absent Score.
	ScoreOptional ScoreRequirement = "Optional"
	// ScoreRequired blocks Ready while an eligible Entry lacks a Score.
	ScoreRequired ScoreRequirement = "Required"
)

// ScoreInterpretation explains how presentation should interpret Scores.
type ScoreInterpretation string

const (
	// HigherWins means larger Scores are conventionally stronger.
	HigherWins ScoreInterpretation = "HigherWins"
	// LowerWins means smaller Scores are conventionally stronger.
	LowerWins ScoreInterpretation = "LowerWins"
	// Informational means Scores do not imply competitive ordering.
	Informational ScoreInterpretation = "Informational"
)

// ScoreValue contains exactly one configured canonical Score representation.
type ScoreValue struct {
	Decimal  *string        `json:"decimal,omitempty"`
	Duration *time.Duration `json:"duration,omitempty"`
}

// ResultStanding states whether one eligible Entry placed.
type ResultStanding string

const (
	// Placed assigns an authoritative ordinal Placement.
	Placed ResultStanding = "Placed"
	// Unplaced retains participation without an ordinal Placement.
	Unplaced ResultStanding = "Unplaced"
)

// Standing is one Entry's explicit result in a Draft.
type Standing struct {
	EntryID      int            `json:"entry_id"`
	Standing     ResultStanding `json:"standing"`
	Placement    int            `json:"placement,omitempty"`
	DisplayOrder int            `json:"display_order"`
	Score        ScoreValue     `json:"score"`
}

// AwardRecipient names one real Entry or an explicit display-name recipient.
type AwardRecipient struct {
	EntryID     int    `json:"entry_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Award is one named recognition independent of Placement and Score.
type Award struct {
	Key          string           `json:"key"`
	Name         string           `json:"name"`
	Recipients   []AwardRecipient `json:"recipients"`
	Promoted     bool             `json:"promoted,omitempty"`
	DisplayOrder int              `json:"display_order"`
}

// AwardReleasePathKind identifies one Event Award review and release path.
type AwardReleasePathKind string

const (
	// StandaloneRelease publishes Event Awards outside a Prizegiving.
	StandaloneRelease AwardReleasePathKind = "Standalone"
	// PrizegivingRelease publishes Event Awards in one Ceremony Session.
	PrizegivingRelease AwardReleasePathKind = "Prizegiving"
)

// AwardReleasePath identifies one independently reviewed Event Award source.
type AwardReleasePath struct {
	Kind                 AwardReleasePathKind `json:"kind"`
	PrizegivingSessionID int                  `json:"prizegiving_session_id,omitempty"`
}

// EventAward assigns one Award to exactly one release path.
type EventAward struct {
	Award
	ReleasePath AwardReleasePath `json:"release_path"`
}

// Draft is one complete immutable Competition Results proposal.
type Draft struct {
	ID                  int
	EventID             int
	SessionID           int
	Revision            int
	Disposition         Disposition
	NoPublicReason      string
	VotingTallyID       int
	TallyOverrideReason string
	VotingTally         *VotingTally
	PublicExplanation   string
	Score               ScorePolicy
	Standings           []Standing
	Awards              []Award
	Ready               bool
	ReadyByAccountID    int
	ReadyAt             time.Time
	CreatedByAccountID  int
	CreatedAt           time.Time
}

// VotingTallyEntry is one aggregate Entry score without Account-level Votes.
type VotingTallyEntry struct {
	EntryID int
	Total   int
	Count   int
}

// VotingTally is immutable aggregate evidence from the closed Voting Window.
type VotingTally struct {
	ID, Participating int
	Method            string
	SelfVotePolicy    string
	Entries           []VotingTallyEntry
	CreatedAt         time.Time
}

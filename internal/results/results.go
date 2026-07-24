// Package results owns crew-only Results Draft review.
package results

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrIncomplete means a Results Draft omits or duplicates an eligible Entry.
	ErrIncomplete = errors.New("results draft is incomplete")
	// ErrCompetitionRanking means Placements do not follow competition ranking.
	ErrCompetitionRanking = errors.New("placements do not follow competition ranking")
	// ErrUnplacedOrder means Unplaced Entries do not retain Locked Entry Order.
	ErrUnplacedOrder = errors.New("unplaced entries do not retain locked entry order")
	// ErrCrewReasonRequired means deliberate non-publication lacks a Crew Reason.
	ErrCrewReasonRequired = errors.New("results crew reason is required")
	// ErrDisposition means the current disposition cannot become Ready.
	ErrDisposition = errors.New("results disposition cannot become ready")
	// ErrScoreRequired means an eligible Entry lacks a required Score.
	ErrScoreRequired = errors.New("required result score is missing")
	// ErrInvalidScore means a Score does not match the configured exact representation.
	ErrInvalidScore = errors.New("result score is invalid")
	// ErrInvalidAward means an Award name, key, recipient, path, or order is invalid.
	ErrInvalidAward = errors.New("result award is invalid")
)

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
	ID                 int
	EventID            int
	SessionID          int
	Revision           int
	Disposition        Disposition
	NoPublicReason     string
	PublicExplanation  string
	Score              ScorePolicy
	Standings          []Standing
	Awards             []Award
	Ready              bool
	ReadyByAccountID   int
	ReadyAt            time.Time
	CreatedByAccountID int
	CreatedAt          time.Time
}

// ValidateAwards validates ordered Award content without resolving Entry ownership.
func ValidateAwards(awards []Award) error {
	if len(awards) > 1000 {
		return ErrInvalidAward
	}
	ordered := slices.Clone(awards)
	sort.Slice(ordered, func(first, second int) bool {
		return ordered[first].DisplayOrder < ordered[second].DisplayOrder
	})
	keys := make(map[string]struct{}, len(ordered))
	totalRecipients := 0
	for index, award := range ordered {
		if !validAwardKey(award.Key) ||
			!boundedAwardText(award.Name, 200) ||
			award.DisplayOrder != index+1 ||
			len(award.Recipients) == 0 ||
			len(award.Recipients) > 1000 {
			return ErrInvalidAward
		}
		totalRecipients += len(award.Recipients)
		if totalRecipients > 10000 {
			return ErrInvalidAward
		}
		if _, duplicate := keys[award.Key]; duplicate {
			return ErrInvalidAward
		}
		keys[award.Key] = struct{}{}
		recipients := make(map[AwardRecipient]struct{}, len(award.Recipients))
		for _, recipient := range award.Recipients {
			entryRecipient := recipient.EntryID > 0
			displayRecipient := strings.TrimSpace(recipient.DisplayName) != ""
			if entryRecipient == displayRecipient ||
				(displayRecipient && !boundedAwardText(recipient.DisplayName, 200)) {
				return ErrInvalidAward
			}
			if _, duplicate := recipients[recipient]; duplicate {
				return ErrInvalidAward
			}
			recipients[recipient] = struct{}{}
		}
	}
	return nil
}

// ValidateEventAwards validates Award content and path-local display order.
func ValidateEventAwards(awards []EventAward) error {
	if len(awards) > 1000 {
		return ErrInvalidAward
	}
	byPath := make(map[AwardReleasePath][]Award)
	keys := make(map[string]struct{}, len(awards))
	totalRecipients := 0
	for _, award := range awards {
		if !validAwardPath(award.ReleasePath) || award.Promoted {
			return ErrInvalidAward
		}
		if _, duplicate := keys[award.Key]; duplicate {
			return ErrInvalidAward
		}
		keys[award.Key] = struct{}{}
		totalRecipients += len(award.Recipients)
		if totalRecipients > 10000 {
			return ErrInvalidAward
		}
		byPath[award.ReleasePath] = append(byPath[award.ReleasePath], award.Award)
	}
	for _, pathAwards := range byPath {
		if err := ValidateAwards(pathAwards); err != nil {
			return err
		}
	}
	return nil
}

func validAwardPath(path AwardReleasePath) bool {
	return path.Kind == StandaloneRelease && path.PrizegivingSessionID == 0 ||
		path.Kind == PrizegivingRelease && path.PrizegivingSessionID > 0
}

func validAwardKey(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' && index > 0 ||
			(character == '-' || character == '_') && index > 0 {
			continue
		}
		return false
	}
	return true
}

func boundedAwardText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" &&
		utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum &&
		!strings.ContainsRune(value, '\x00')
}

// EligibleEntry is one Included Entry that requires an explicit Standing.
type EligibleEntry struct {
	ID          int
	LockedOrder int
}

// ValidateDraft validates one editable Results Draft without requiring complete
// eligible Entry coverage.
func ValidateDraft(draft Draft) error {
	if err := ValidateAwards(draft.Awards); err != nil {
		return err
	}
	if err := validateScorePolicy(draft.Score); err != nil {
		return err
	}
	switch draft.Disposition {
	case NoPublicResults:
		if strings.TrimSpace(draft.NoPublicReason) == "" {
			return ErrCrewReasonRequired
		}
		if len(draft.Standings) != 0 {
			return ErrDisposition
		}
		return nil
	case Publish, Pending:
	default:
		return ErrDisposition
	}
	ordered := slices.Clone(draft.Standings)
	sort.Slice(ordered, func(first, second int) bool {
		return ordered[first].DisplayOrder < ordered[second].DisplayOrder
	})
	seen := make(map[int]struct{}, len(ordered))
	previousPlacement := 0
	unplaced := false
	for index, standing := range ordered {
		if standing.DisplayOrder != index+1 || standing.EntryID <= 0 {
			return ErrIncomplete
		}
		if _, duplicate := seen[standing.EntryID]; duplicate {
			return ErrIncomplete
		}
		seen[standing.EntryID] = struct{}{}
		switch standing.Standing {
		case Placed:
			if unplaced || standing.Placement <= 0 {
				return ErrCompetitionRanking
			}
			if standing.Placement != previousPlacement &&
				standing.Placement != index+1 {
				return ErrCompetitionRanking
			}
			previousPlacement = standing.Placement
		case Unplaced:
			if standing.Placement != 0 {
				return ErrCompetitionRanking
			}
			unplaced = true
		default:
			return ErrIncomplete
		}
		if err := validateScore(draft.Score, standing.Score); err != nil {
			return err
		}
	}
	return nil
}

// Review validates whether one exact Publish Draft can be marked Ready.
func Review(draft Draft, entries []EligibleEntry) error {
	if err := ValidateDraft(draft); err != nil {
		return err
	}
	if draft.Disposition != Publish {
		return ErrDisposition
	}
	if len(draft.Standings) != len(entries) {
		return ErrIncomplete
	}
	eligible := make(map[int]EligibleEntry, len(entries))
	for _, entry := range entries {
		eligible[entry.ID] = entry
	}
	ordered := slices.Clone(draft.Standings)
	sort.Slice(ordered, func(first, second int) bool {
		return ordered[first].DisplayOrder < ordered[second].DisplayOrder
	})
	seen := make(map[int]struct{}, len(ordered))
	unplacedIDs := make([]int, 0, len(ordered))
	for _, standing := range ordered {
		if _, ok := eligible[standing.EntryID]; !ok {
			return ErrIncomplete
		}
		if _, duplicate := seen[standing.EntryID]; duplicate {
			return ErrIncomplete
		}
		seen[standing.EntryID] = struct{}{}
		if standing.Standing == Unplaced {
			unplacedIDs = append(unplacedIDs, standing.EntryID)
		}
	}
	lockedUnplaced := make([]EligibleEntry, 0, len(unplacedIDs))
	for _, entryID := range unplacedIDs {
		lockedUnplaced = append(lockedUnplaced, eligible[entryID])
	}
	sort.Slice(lockedUnplaced, func(first, second int) bool {
		return lockedUnplaced[first].LockedOrder < lockedUnplaced[second].LockedOrder
	})
	for index, entry := range lockedUnplaced {
		if entry.ID != unplacedIDs[index] {
			return ErrUnplacedOrder
		}
	}
	return nil
}

func validateScorePolicy(policy ScorePolicy) error {
	validVisibility := policy.Visibility == "" ||
		policy.Visibility == ScorePublic || policy.Visibility == ScoreCrewOnly
	validRequirement := policy.Requirement == "" ||
		policy.Requirement == ScoreOptional || policy.Requirement == ScoreRequired
	validInterpretation := policy.Interpretation == "" ||
		policy.Interpretation == HigherWins ||
		policy.Interpretation == LowerWins ||
		policy.Interpretation == Informational
	switch policy.Type {
	case None:
		if policy.Unit != "" || policy.Precision != 0 ||
			!validVisibility || !validRequirement || !validInterpretation {
			return ErrInvalidScore
		}
		return nil
	case Decimal, Duration:
		if strings.TrimSpace(policy.Unit) == "" ||
			!utf8.ValidString(policy.Unit) ||
			utf8.RuneCountInString(policy.Unit) > 100 ||
			strings.ContainsRune(policy.Unit, '\x00') ||
			policy.Precision < 0 || policy.Precision > 9 ||
			(policy.Visibility != ScorePublic && policy.Visibility != ScoreCrewOnly) ||
			(policy.Requirement != ScoreOptional && policy.Requirement != ScoreRequired) ||
			(policy.Interpretation != HigherWins &&
				policy.Interpretation != LowerWins &&
				policy.Interpretation != Informational) {
			return ErrInvalidScore
		}
		return nil
	default:
		return ErrInvalidScore
	}
}

func validateScore(policy ScorePolicy, score ScoreValue) error {
	present := score.Decimal != nil || score.Duration != nil
	if !present {
		if policy.Requirement == ScoreRequired {
			return ErrScoreRequired
		}
		return nil
	}
	switch policy.Type {
	case Decimal:
		if score.Decimal == nil || score.Duration != nil ||
			!validDecimal(*score.Decimal, policy.Precision) {
			return ErrInvalidScore
		}
	case Duration:
		if score.Duration == nil || score.Decimal != nil || *score.Duration < 0 {
			return ErrInvalidScore
		}
	default:
		return ErrInvalidScore
	}
	return nil
}

func validDecimal(value string, precision int) bool {
	if value == "" || len(value) > 200 || strings.TrimSpace(value) != value {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole == "" || (len(whole) > 1 && whole[0] == '0') ||
		(hasFraction && (fraction == "" || len(fraction) > precision)) ||
		strings.Contains(fraction, ".") {
		return false
	}
	for _, part := range []string{whole, fraction} {
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

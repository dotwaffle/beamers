// Package awardvalue defines dependency-free JSON values used by Ent snapshots.
package awardvalue

import "time"

// Recipient persists one Entry or explicit display-name recipient.
type Recipient struct {
	EntryID     int    `json:"entry_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Competition persists one Competition Award in an immutable Results snapshot.
type Competition struct {
	Key          string      `json:"key"`
	Name         string      `json:"name"`
	Recipients   []Recipient `json:"recipients"`
	Promoted     bool        `json:"promoted,omitempty"`
	DisplayOrder int         `json:"display_order"`
}

// Promotion identifies one Award's independent Result Item state.
type Promotion struct {
	Key      string
	Promoted bool
}

// Promotions returns the promotion projection of Competition Awards.
func Promotions(values []Competition) []Promotion {
	result := make([]Promotion, 0, len(values))
	for _, value := range values {
		result = append(result, Promotion{Key: value.Key, Promoted: value.Promoted})
	}
	return result
}

// PromotionChanged reports whether any Award entered or left promoted state.
func PromotionChanged(current, next []Promotion) bool {
	promoted := make(map[string]bool, len(current))
	for _, award := range current {
		promoted[award.Key] = award.Promoted
	}
	for _, award := range next {
		if award.Promoted != promoted[award.Key] {
			return true
		}
		delete(promoted, award.Key)
	}
	for _, value := range promoted {
		if value {
			return true
		}
	}
	return false
}

// ReleasePath persists one Event Award release path.
type ReleasePath struct {
	Kind                 string `json:"kind"`
	PrizegivingSessionID int    `json:"prizegiving_session_id,omitempty"`
}

// Event persists one Event Award in an immutable Event snapshot.
type Event struct {
	Key          string      `json:"key"`
	Name         string      `json:"name"`
	Recipients   []Recipient `json:"recipients"`
	DisplayOrder int         `json:"display_order"`
	ReleasePath  ReleasePath `json:"release_path"`
}

// PathState persists independent review state for one release path.
type PathState struct {
	ReleasePath      ReleasePath `json:"release_path"`
	Revision         int         `json:"revision"`
	ReadyByAccountID int         `json:"ready_by_account_id,omitempty"`
	ReadyAt          *time.Time  `json:"ready_at,omitempty"`
}

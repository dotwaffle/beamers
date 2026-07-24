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

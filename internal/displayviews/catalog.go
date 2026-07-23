// Package displayviews identifies the built-in normal Views assignable to Displays.
package displayviews

const (
	// EventOverview identifies the built-in multi-Lane Event information View.
	EventOverview = "event-overview"
	// LocationSignage identifies the built-in Location Now/Next View.
	LocationSignage = "location-signage"
	// StageTimer identifies the built-in crew countdown View.
	StageTimer = "stage-timer"
	// CompetitionOutput identifies the built-in public Competition View.
	CompetitionOutput = "competition-output"
)

// IsNormal reports whether key identifies a built-in assignable normal View.
func IsNormal(key string) bool {
	switch key {
	case EventOverview, LocationSignage, StageTimer, CompetitionOutput:
		return true
	default:
		return false
	}
}

// Package displayviews identifies the built-in normal Views assignable to Displays.
package displayviews

import "slices"

const (
	// EventOverview identifies the built-in multi-Lane Event information View.
	EventOverview = "event-overview"
	// LocationSignage identifies the built-in Location Now/Next View.
	LocationSignage = "location-signage"
	// StageTimer identifies the built-in crew countdown View.
	StageTimer = "stage-timer"
	// CompetitionOutput identifies the built-in public Competition View.
	CompetitionOutput = "competition-output"
	// Timeline identifies the built-in proportional Event day View.
	Timeline = "timeline"
	// CrewOverview identifies the built-in crew timing and progress View.
	CrewOverview = "crew-overview"
)

// normal is the closed built-in catalog in the order Views are offered. ADR 0058
// keeps it closed: these are fixed Layouts and Regions, not a step toward the
// deferred visual Layout and slide-template editor.
var normal = []string{
	EventOverview,
	LocationSignage,
	StageTimer,
	CompetitionOutput,
	Timeline,
	CrewOverview,
}

// Normal returns the built-in assignable normal Views in catalog order. It backs
// both assignment validation and the assignment control, so a View cannot become
// assignable without also being offered.
func Normal() []string {
	return slices.Clone(normal)
}

// IsNormal reports whether key identifies a built-in assignable normal View.
func IsNormal(key string) bool {
	return slices.Contains(normal, key)
}

package authz

import "slices"

// Scope is the Event scope dimension a row is judged against.
type Scope uint8

const (
	// ScopeNone is installation authority, or an action whose rule is
	// ownership rather than an Event Capability. No Event facts are needed.
	ScopeNone Scope = iota
	// ScopeEvent requires the Event Grant to name the Event. No Lane or
	// Display Group facts are needed.
	ScopeEvent
	// ScopeLanes requires every Lane the target occupies to be granted. A
	// target with no Lanes refuses: a Session with no Lane placement cannot
	// be judged, so it is refused rather than allowed.
	ScopeLanes
	// ScopeDisplayGroups requires every Display Group key the Override target
	// resolves to to be granted, or, for a Lane-typed target, the Lane.
	ScopeDisplayGroups
)

// String names the dimension for diagnostics and test output.
func (scope Scope) String() string {
	switch scope {
	case ScopeNone:
		return "none"
	case ScopeEvent:
		return "Event-wide"
	case ScopeLanes:
		return "Lanes-of-target"
	case ScopeDisplayGroups:
		return "DisplayGroups-of-target"
	default:
		return "unknown"
	}
}

// Facts are the concrete scope facts one command's plan phase loads inside the
// command transaction, before apply, for its row to be judged against.
//
// Facts carry the dimension they were built for, so a row cannot evaluate
// against facts of the wrong dimension: the evaluator refuses with
// ErrScopeFactsMismatch rather than allowing. The zero Facts value carries no
// dimension at all and is refused by every row, so a command that supplies
// nothing fails closed instead of silently passing.
type Facts struct {
	scope     Scope
	eventID   int
	laneIDs   []int
	groupKeys []string
	demanded  []Capability
	supplied  bool
	replayed  bool
}

// Replayed states that this command persists an outcome whose authorization was
// already decided, so the table is not applied to it a second time. It is the
// same rule the Command Receipt lookup follows: a recorded outcome is durable
// truth and is never re-judged, because re-judging it would let a later change
// of authority rewrite what already happened.
//
// It exists for the degraded Emergency Alert path, which decides authority in
// process memory while durable evidence is unavailable and persists the
// decision — acceptance or refusal — on recovery. A walking test holds its call
// sites to a declared list, so this cannot become a general way around the
// table.
func Replayed() Facts {
	return Facts{supplied: true, replayed: true}
}

// Installation returns the facts an installation-authority row is judged
// against. There are none beyond the declaration itself.
func Installation() Facts {
	return Facts{scope: ScopeNone, supplied: true}
}

// Event returns the facts an Event-wide row is judged against.
func Event(eventID int) Facts {
	return Facts{scope: ScopeEvent, eventID: eventID, supplied: true}
}

// Lanes returns the facts a Lanes-of-target row is judged against: every Lane
// the target Session occupies.
func Lanes(eventID int, laneIDs []int) Facts {
	return Facts{
		scope:    ScopeLanes,
		eventID:  eventID,
		laneIDs:  slices.Clone(laneIDs),
		supplied: true,
	}
}

// DisplayGroups returns the facts a DisplayGroups-of-target row is judged
// against: the Display Group keys the Override target resolves to.
func DisplayGroups(eventID int, groupKeys []string) Facts {
	return Facts{
		scope:     ScopeDisplayGroups,
		eventID:   eventID,
		groupKeys: slices.Clone(groupKeys),
		supplied:  true,
	}
}

// DisplayGroupsOfLane returns the facts a DisplayGroups-of-target row is judged
// against when the Override target is a Lane, which the current rule judges by
// Lane grant rather than by Display Group key.
func DisplayGroupsOfLane(eventID, laneID int) Facts {
	return Facts{
		scope:    ScopeDisplayGroups,
		eventID:  eventID,
		laneIDs:  []int{laneID},
		supplied: true,
	}
}

// Demanding adds Capabilities the loaded target requires beyond the row's
// unconditional ones, for actions whose authority depends on what they found:
// clearing an Emergency Alert requires the EmergencyAlert Capability where
// clearing any other Override does not.
//
// A plan may demand only Capabilities its row lists in TargetCapabilities; the
// evaluator refuses with ErrUndeclaredDemand otherwise, so the table stays the
// closed statement of what an action can require.
func (facts Facts) Demanding(capabilities ...Capability) Facts {
	facts.demanded = append(slices.Clone(facts.demanded), capabilities...)
	return facts
}

// Scope returns the dimension these facts were built for.
func (facts Facts) Scope() Scope {
	return facts.scope
}

// EventID returns the Event these facts belong to, or zero for installation
// authority.
func (facts Facts) EventID() int {
	return facts.eventID
}

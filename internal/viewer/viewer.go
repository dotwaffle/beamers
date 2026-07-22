// Package viewer carries authenticated authorization facts through Ent privacy.
package viewer

import "context"

// Role is one Account's authority for an Event.
type Role string

const (
	// Producer can configure and operate an Event.
	Producer Role = "Producer"
	// Operator can use scoped live controls.
	Operator Role = "Operator"
	// Observer can read Event crew data.
	Observer Role = "Observer"
)

// Capability is separately granted high-impact Event authority.
type Capability string

const (
	// EmergencyAlert permits emergency broadcast commands.
	EmergencyAlert Capability = "EmergencyAlert"
	// ViewResults permits reading Event results.
	ViewResults Capability = "ViewResults"
	// ManageResults permits mutating Event results.
	ManageResults Capability = "ManageResults"
)

// EventScope contains the explicit restrictions attached to one Event Grant.
type EventScope struct {
	LaneIDs          map[int]struct{}
	DisplayGroupKeys map[string]struct{}
	Capabilities     map[Capability]struct{}
}

// Identity contains authorization facts established during authentication.
type Identity struct {
	AccountID     int
	Administrator bool
	EventRoles    map[int]Role
	EventScopes   map[int]EventScope
}

type contextKey struct{}

// NewContext adds an authenticated identity without changing cancellation.
func NewContext(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

// FromContext returns the authenticated identity, when present.
func FromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}

// CanReadEvent reports whether the identity has an explicit Event Grant.
func (identity Identity) CanReadEvent(eventID int) bool {
	_, ok := identity.EventRoles[eventID]
	return ok
}

// CanProduceEvent reports whether the identity has Producer authority.
func (identity Identity) CanProduceEvent(eventID int) bool {
	return identity.EventRoles[eventID] == Producer
}

// CanOperateLane reports live-control authority for one Event Lane.
func (identity Identity) CanOperateLane(eventID, laneID int) bool {
	if eventID <= 0 || laneID <= 0 {
		return false
	}
	role := identity.EventRoles[eventID]
	if role == Producer {
		return true
	}
	if role != Operator {
		return false
	}
	_, ok := identity.EventScopes[eventID].LaneIDs[laneID]
	return ok
}

// CanOperateDisplayGroup reports live-control authority for one opaque group key.
func (identity Identity) CanOperateDisplayGroup(eventID int, key string) bool {
	if eventID <= 0 || key == "" {
		return false
	}
	role := identity.EventRoles[eventID]
	if role == Producer {
		return true
	}
	if role != Operator {
		return false
	}
	_, ok := identity.EventScopes[eventID].DisplayGroupKeys[key]
	return ok
}

// HasCapability reports role-default or explicitly granted high-impact authority.
func (identity Identity) HasCapability(eventID int, capability Capability) bool {
	switch capability {
	case EmergencyAlert, ViewResults, ManageResults:
	default:
		return false
	}
	switch identity.EventRoles[eventID] {
	case Producer:
		return true
	case Operator:
	case Observer:
		if capability != ViewResults {
			return false
		}
	default:
		return false
	}
	_, ok := identity.EventScopes[eventID].Capabilities[capability]
	return ok
}

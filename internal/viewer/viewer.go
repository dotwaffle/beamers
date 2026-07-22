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

// Identity contains authorization facts established during authentication.
type Identity struct {
	AccountID     int
	Administrator bool
	EventRoles    map[int]Role
	System        bool
}

type contextKey struct{}

// NewContext adds an authenticated identity without changing cancellation.
func NewContext(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

// SystemContext marks a narrow store-internal operation that cannot be set by transport input.
func SystemContext(ctx context.Context) context.Context {
	return NewContext(ctx, Identity{System: true})
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

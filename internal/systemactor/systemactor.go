// Package systemactor names the viewer-less callers that reach the store.
//
// Work that runs without a signed-in Account still acts on somebody's behalf:
// an enrolled Display, a public visitor, a Backup, a migration, replication,
// a command replay, or host maintenance. Each caller establishes its System
// Actor once where it enters the system, and the authorization tripwire in
// ent/schema accepts a viewer identity or a named System Actor and nothing
// else, so an unnamed allow decision fails closed.
package systemactor

import "context"

// Actor is one named viewer-less caller class. The set is closed: values
// outside the constants below are never established in a context.
type Actor uint8

const (
	// unknown is the zero value, and names no caller.
	unknown Actor = iota
	// Display is an enrolled Display reading its own snapshot or Program
	// Output after its credential check.
	Display
	// PublicVisitor is an unauthenticated reader of public surfaces: Event
	// pages, the Schedule, Results Publications, and the sign-in path that
	// has not yet established an Account.
	PublicVisitor
	// Backup is Backup or Restore work, whose access breadth is unusual and
	// therefore declared rather than smuggled.
	Backup
	// Migration is the migration runner applying committed schema changes.
	Migration
	// Replication is the replication adapter holding the database file.
	Replication
	// CommandReplay is the command lifecycle reading and writing Command
	// Receipts and their Audit Entries.
	CommandReplay
	// HostMaintenance is host-authority operational work: startup readiness,
	// diagnostics, capacity sampling, and the host command line.
	HostMaintenance
)

// String returns the actor's name, or "unknown" for an unnamed value.
func (actor Actor) String() string {
	switch actor {
	case Display:
		return "display"
	case PublicVisitor:
		return "public visitor"
	case Backup:
		return "backup"
	case Migration:
		return "migration"
	case Replication:
		return "replication"
	case CommandReplay:
		return "command replay"
	case HostMaintenance:
		return "host maintenance"
	case unknown:
		return "unknown"
	}
	return "unknown"
}

// Named reports whether the actor belongs to the closed set.
func (actor Actor) Named() bool {
	return actor != unknown && actor <= HostMaintenance
}

// All returns every named System Actor, so tests and walks cover the set
// without restating it.
func All() []Actor {
	return []Actor{
		Display,
		PublicVisitor,
		Backup,
		Migration,
		Replication,
		CommandReplay,
		HostMaintenance,
	}
}

type contextKey struct{}

// NewContext names the System Actor acting from here on without changing
// cancellation. An actor outside the closed set is not established, so work
// carrying it fails closed at the tripwire rather than acting unnamed.
func NewContext(ctx context.Context, actor Actor) context.Context {
	if !actor.Named() {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, actor)
}

// FromContext returns the named System Actor, when one is established.
func FromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(contextKey{}).(Actor)
	if !ok || !actor.Named() {
		return unknown, false
	}
	return actor, true
}

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/viewer"
)

// entrypointActor names one actor class an exported store entrypoint accepts.
// The set closes over the authenticated viewer identity, the account
// holder — an authenticated visitor acting on their own submissions or
// profile rather than as crew — and every named System Actor from
// internal/systemactor. actorNone declares that an entrypoint accepts no
// external caller at all.
type entrypointActor uint8

const (
	// actorViewer is an authenticated Crew Member identity: an Account
	// holding an Event Grant, judged by the viewer package's predicates.
	actorViewer entrypointActor = iota
	// actorAccountHolder is an authenticated visitor acting on their own
	// submissions or profile on a public surface, not as crew. The store
	// cannot tell an account holder from a Crew Member by actor class alone
	// — both carry a viewer.Identity — so this class admits the same
	// context as actorViewer, and the ownership check that distinguishes
	// them stays where it already is, inside the entrypoint.
	actorAccountHolder
	// actorDisplay is an enrolled Display reading its own snapshot or
	// Program Output after its credential check.
	actorDisplay
	// actorPublicVisitor is an unauthenticated reader of public surfaces.
	actorPublicVisitor
	// actorBackup is Backup or Restore work.
	actorBackup
	// actorMigration is the migration runner.
	actorMigration
	// actorReplication is the replication adapter.
	actorReplication
	// actorCommandReplay is the command lifecycle reading and writing
	// Command Receipts and their Audit Entries.
	actorCommandReplay
	// actorHostMaintenance is host-authority operational work.
	actorHostMaintenance
	// actorNone declares that an entrypoint is reachable only from inside
	// internal/store: an honest declaration rather than an omission. Such
	// entrypoints carry no runtime guard, because whatever context reaches
	// them was already established, and refused if wrong, at the external
	// boundary that called into the store in the first place.
	actorNone
)

// ErrActorNotPermitted means the calling context's actor is not among the
// classes this store entrypoint declares in entrypointDeclarations.
var ErrActorNotPermitted = errors.New("actor not permitted at this store entrypoint")

// systemActorClass maps each named System Actor to the entrypoint actor
// class it satisfies.
var systemActorClass = map[systemactor.Actor]entrypointActor{
	systemactor.Display:         actorDisplay,
	systemactor.PublicVisitor:   actorPublicVisitor,
	systemactor.Backup:          actorBackup,
	systemactor.Migration:       actorMigration,
	systemactor.Replication:     actorReplication,
	systemactor.CommandReplay:   actorCommandReplay,
	systemactor.HostMaintenance: actorHostMaintenance,
}

// requireActor refuses ctx's actor unless the entrypoint named by key
// declares it as accepted. key is "ReceiverType.MethodName", matching
// entrypointDeclarations; a key absent from the registry fails closed like
// any actor the entrypoint does not name, which the completeness walking
// test exists to make unreachable.
func requireActor(ctx context.Context, key string) error {
	allowed := entrypointDeclarations[key]
	if _, ok := viewer.FromContext(ctx); ok {
		if containsActor(allowed, actorViewer) || containsActor(allowed, actorAccountHolder) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrActorNotPermitted, key)
	}
	if actor, ok := systemactor.FromContext(ctx); ok {
		if class, known := systemActorClass[actor]; known && containsActor(allowed, class) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrActorNotPermitted, key)
	}
	return fmt.Errorf("%w: %s (no actor named)", ErrActorNotPermitted, key)
}

func containsActor(allowed []entrypointActor, actor entrypointActor) bool {
	return slices.Contains(allowed, actor)
}

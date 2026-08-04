package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql/schema"
	"modernc.org/sqlite"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/enttest"
	"github.com/dotwaffle/beamers/ent/migrate"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

// hostMaintenanceContext names a System Actor for store tests that exercise an
// entrypoint directly, standing in for the caller boundary that names the actor
// in production.
func hostMaintenanceContext(ctx context.Context) context.Context {
	return systemactor.NewContext(ctx, systemactor.HostMaintenance)
}

var (
	registerEntSQLite sync.Once
	entDatabaseID     atomic.Int64
)

// createEntTestSchema builds the Ent schema on one test database from a private
// copy of the generated table descriptors. Atlas rewrites those descriptors in
// place while it plans a migration, renaming indexes and linking columns back
// to them, and the generated ones are package-level values shared by every
// client in the process, so a client that migrates straight from them races
// with every other test opening its own database. This is what enttest.Open
// does for the same reason.
func createEntTestSchema(ctx context.Context, client *ent.Client) error {
	tables, err := schema.CopyTables(migrate.Tables)
	if err != nil {
		return fmt.Errorf("copy Ent migration tables: %w", err)
	}
	if err := migrate.Create(ctx, client.Schema, tables); err != nil {
		return fmt.Errorf("create Ent test schema: %w", err)
	}
	return nil
}

// closeEntTestClient closes one test database when the test ends.
func closeEntTestClient(t *testing.T, client *ent.Client) {
	t.Helper()
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Ent test database: %v", err)
		}
	})
}

// Shape of the parallel schema guard below. Four builders running three builds
// each keeps the builders drifting apart, so one is planning a migration while
// another is still setting up its tables. That overlap is what the race
// detector needs: a run where every builder moves in lockstep orders itself
// through the locks Atlas takes and reports nothing. The whole guard costs
// about a second.
const (
	entTestSchemaBuilders    = 4
	entTestSchemaBuildsEach  = 3
	entTestSchemaBuildsTotal = entTestSchemaBuilders * entTestSchemaBuildsEach
)

// TestEntTestSchemaCreationIsParallelSafe guards the Ent test helpers against
// the data race that Atlas hits while it builds a schema: the generated table
// descriptors are package-level values that Atlas rewrites in place, renaming
// indexes and linking columns back to them, so two databases migrating at once
// read and write the same state. Every test in this package that wants its own
// database pays for that.
//
// The builds run from goroutines released by one barrier rather than from
// parallel subtests, because the testing package is free to run subtests one at
// a time and orders them with its own locks, which would let the guard pass on
// a racy helper. They go through the same setup that every counting test takes,
// so putting an unsafe migration back into that setup fails here.
func TestEntTestSchemaCreationIsParallelSafe(t *testing.T) {
	t.Parallel()
	ctx := hostMaintenanceContext(t.Context())
	clients := make([]*ent.Client, entTestSchemaBuildsTotal)
	failures := make([]error, len(clients))
	start := make(chan struct{})
	var builders sync.WaitGroup
	for builder := range entTestSchemaBuilders {
		builders.Go(func() {
			<-start
			first := builder * entTestSchemaBuildsEach
			for index := first; index < first+entTestSchemaBuildsEach; index++ {
				clients[index], _, failures[index] = buildCountingEntTestClient(ctx)
			}
		})
	}
	close(start)
	builders.Wait()
	for _, client := range clients {
		if client != nil {
			closeEntTestClient(t, client)
		}
	}
	for index, err := range failures {
		if err != nil {
			t.Fatalf("build counting Ent test client %d in parallel: %v", index, err)
		}
	}
}

// TestCountingEntTestClientCountsItsQueries pins the other half of the counting
// helper's contract: the driver it hands back has to see the statements that
// the query-count guards measure, or those guards would compare zero to zero.
func TestCountingEntTestClientCountsItsQueries(t *testing.T) {
	t.Parallel()
	ctx := hostMaintenanceContext(t.Context())
	client, driver := openCountingEntTestClient(t)
	before := driver.statements.Load()
	if _, err := client.Display.Query().Count(ctx); err != nil {
		t.Fatalf("count Displays through the counting driver: %v", err)
	}
	if counted := driver.statements.Load() - before; counted != 1 {
		t.Fatalf("counting driver saw %d statements for one query, want 1", counted)
	}
}

func openEntTestClient(t *testing.T) *ent.Client {
	t.Helper()
	registerEntSQLite.Do(func() {
		sql.Register("sqlite3", &sqlite.Driver{})
	})
	dsn := fmt.Sprintf(
		"file:beamers_enttest_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)",
		entDatabaseID.Add(1),
	)
	client := enttest.Open(t, dialect.SQLite, dsn)
	closeEntTestClient(t, client)
	return client
}

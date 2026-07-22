package store

import (
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	"modernc.org/sqlite"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/enttest"
)

var (
	registerEntSQLite sync.Once
	entDatabaseID     atomic.Int64
)

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
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Ent test database: %v", err)
		}
	})
	return client
}

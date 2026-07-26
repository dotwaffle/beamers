package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCommittedMigrationsReplayIntoCleanSQLite(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize clean database: %v", err)
	}

	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close migrated database: %v", closeErr)
		}
	}()
	if err := installation.StartupError(); err != nil {
		t.Fatalf("validate migrated database: %v", err)
	}
	if err := installation.Ready(t.Context()); err != nil {
		t.Fatalf("query migrated database through Ent: %v", err)
	}
}

func TestTelemetryOpenTracesOperationsWithoutSQLOrDSN(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	installation, err := OpenWithTelemetry(
		t.Context(),
		dataDir,
		tracerProvider,
		metricnoop.NewMeterProvider(),
	)
	if err != nil {
		t.Fatalf("open instrumented database: %v", err)
	}
	if err = installation.Ready(t.Context()); err != nil {
		t.Fatalf("query instrumented database: %v", err)
	}
	if err = installation.Close(); err != nil {
		t.Fatalf("close instrumented database: %v", err)
	}
	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("instrumented database emitted no spans")
	}
	for _, span := range spans {
		switch span.Name() {
		case "sql.conn.reset_session", "sql.conn.prepare", "sql.rows", "sql.connector.connect":
			t.Errorf("low-value span emitted: %s", span.Name())
		}
		for _, attribute := range span.Attributes() {
			if attribute.Key == "db.query.text" ||
				strings.Contains(attribute.Value.String(), dataDir) ||
				strings.Contains(attribute.Value.String(), "PRAGMA") {
				t.Errorf("unsafe span attribute %s=%s", attribute.Key, attribute.Value.String())
			}
		}
	}
}

func TestAttestedMigrationPlanUpgradesStagedSQLite(t *testing.T) {
	ctx := t.Context()
	databasePath := initializeSchemaFixture(t, 47)

	plan, err := PlanMigrations(ctx, databasePath)
	if err != nil {
		t.Fatalf("plan migrations: %v", err)
	}
	if plan.FromVersion != 47 ||
		plan.ToVersion != 50 ||
		plan.Safety != MigrationNonDestructive ||
		plan.MinimumReaderSchemaVersion != 50 ||
		plan.MinimumWriterSchemaVersion != 50 ||
		len(plan.Migrations) != 3 ||
		plan.Migrations[0].Version != 48 ||
		plan.Migrations[1].Version != 49 ||
		plan.Migrations[2].Version != 50 {
		t.Fatalf("migration plan = %+v", plan)
	}

	if err = MigrateSnapshot(ctx, databasePath, plan); err != nil {
		t.Fatalf("migrate staged database: %v", err)
	}
	if err = ValidateSnapshot(ctx, databasePath); err != nil {
		t.Fatalf("validate migrated database: %v", err)
	}

	database, err := openValidationDatabase(ctx, databasePath)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close migrated database: %v", closeErr)
		}
	}()
	var safety string
	var minimumReader, minimumWriter int
	if err = database.QueryRowContext(
		ctx,
		"SELECT safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version FROM beamers_schema_migrations "+
			"WHERE version = 50",
	).Scan(&safety, &minimumReader, &minimumWriter); err != nil {
		t.Fatalf("read migration contract: %v", err)
	}
	if safety != string(MigrationNonDestructive) ||
		minimumReader != 50 ||
		minimumWriter != 50 {
		t.Fatalf(
			"migration contract = %q/%d/%d",
			safety,
			minimumReader,
			minimumWriter,
		)
	}
}

func TestMigrationPlanRejectsUnknownCommittedPrefix(t *testing.T) {
	databasePath := initializeSchemaFixture(t, 47)
	database, err := openDatabase(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err = database.ExecContext(
		t.Context(),
		"UPDATE beamers_schema_migrations SET checksum = printf('%064d', 0) "+
			"WHERE version = 47",
	); err != nil {
		_ = database.Close()
		t.Fatalf("replace migration checksum: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	if _, err = PlanMigrations(t.Context(), databasePath); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unknown prefix error = %v, want %v", err, ErrUnsupportedSchema)
	}
}

func TestDeclaredForwardWriterRangeAllowsNewerSchema(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	database, err := openDatabase(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err = database.ExecContext(
		t.Context(),
		"INSERT INTO beamers_schema_migrations "+
			"(version, name, checksum, safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version, applied_at) "+
			"VALUES (51, 'future_addition', printf('%064d', 1), "+
			"'NonDestructive', 50, 50, CURRENT_TIMESTAMP)",
	); err != nil {
		_ = database.Close()
		t.Fatalf("record future migration: %v", err)
	}
	if _, err = database.ExecContext(t.Context(), "PRAGMA user_version = 51"); err != nil {
		_ = database.Close()
		t.Fatalf("set future schema version: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close future fixture: %v", err)
	}

	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open forward-compatible installation: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close installation: %v", closeErr)
		}
	}()
	if err = installation.StartupError(); err != nil {
		t.Fatalf("forward-compatible startup: %v", err)
	}
	if installation.SchemaVersion() != 51 {
		t.Fatalf("opened schema version = %d, want 51", installation.SchemaVersion())
	}
}

func TestUnclassifiedMigrationRecordsClosedCompatibilityRange(t *testing.T) {
	databasePath := initializeSchemaFixture(t, 49)
	database, err := openDatabase(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		_ = database.Close()
		t.Fatalf("begin fixture transaction: %v", err)
	}
	future := migration{
		version:  50,
		name:     "unclassified_fixture",
		checksum: strings.Repeat("1", 64),
	}
	if err = recordMigration(t.Context(), transaction, future); err != nil {
		_ = transaction.Rollback()
		_ = database.Close()
		t.Fatalf("record unclassified migration: %v", err)
	}
	if err = transaction.Commit(); err != nil {
		_ = database.Close()
		t.Fatalf("commit fixture transaction: %v", err)
	}
	var safety string
	var minimumReader, minimumWriter int
	if err = database.QueryRowContext(
		t.Context(),
		"SELECT safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version FROM beamers_schema_migrations "+
			"WHERE version = 50",
	).Scan(&safety, &minimumReader, &minimumWriter); err != nil {
		_ = database.Close()
		t.Fatalf("read unclassified migration: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	if safety != string(MigrationUnclassified) ||
		minimumReader != 50 ||
		minimumWriter != 50 {
		t.Fatalf(
			"unclassified migration contract = %q/%d/%d",
			safety,
			minimumReader,
			minimumWriter,
		)
	}
}

func initializeSchemaFixture(t *testing.T, version int) string {
	t.Helper()
	ctx := t.Context()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if version <= 0 || version > len(migrations) {
		t.Fatalf("fixture schema version %d outside 1..%d", version, len(migrations))
	}
	databasePath := filepath.Join(t.TempDir(), databaseFilename)
	file, err := os.Create(databasePath)
	if err != nil {
		t.Fatalf("create fixture database: %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("close empty fixture database: %v", err)
	}
	database, err := sql.Open("sqlite", sqliteDataSource(databasePath, false))
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if err = initializeSchema(ctx, database, migrations[:version]); err != nil {
		_ = database.Close()
		t.Fatalf("initialize schema %d: %v", version, err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	return databasePath
}

func TestAuthenticationCredentialsExpire(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize authentication database: %v", err)
	}
	installation, err := Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open authentication database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close authentication database: %v", closeErr)
		}
	}()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	expiredBootstrapHash := strings.Repeat("a", 64)
	issueErr := installation.IssueBootstrap(
		t.Context(),
		expiredBootstrapHash,
		now,
		now.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("issue expiring bootstrap credential: %v", issueErr)
	}
	_, err = installation.BootstrapAdministrator(
		t.Context(),
		BootstrapAdministratorParams{
			BootstrapHash:  expiredBootstrapHash,
			Name:           "Ada Admin",
			NormalizedName: "ada admin",
			PasswordHash:   "password hash",
			SessionHash:    strings.Repeat("b", 64),
			Now:            now.Add(2 * time.Minute),
			SessionExpiry:  now.Add(time.Hour),
		},
	)
	if !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("expired bootstrap error = %v, want %v", err, ErrInvalidBootstrap)
	}

	validBootstrapHash := strings.Repeat("c", 64)
	bootstrapTime := now.Add(2 * time.Minute)
	issueErr = installation.IssueBootstrap(
		t.Context(),
		validBootstrapHash,
		bootstrapTime,
		bootstrapTime.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("replace expired bootstrap credential: %v", issueErr)
	}
	sessionHash := strings.Repeat("d", 64)
	created, err := installation.BootstrapAdministrator(
		t.Context(),
		BootstrapAdministratorParams{
			BootstrapHash:  validBootstrapHash,
			Name:           "Ada Admin",
			NormalizedName: "ada admin",
			PasswordHash:   "password hash",
			SessionHash:    sessionHash,
			Now:            bootstrapTime,
			SessionExpiry:  bootstrapTime.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	if created.Name != "Ada Admin" || !created.Administrator {
		t.Errorf("created Account = %+v, want Ada Admin Administrator", created)
	}
	_, err = installation.FindAccountSession(
		t.Context(),
		sessionHash,
		bootstrapTime.Add(2*time.Minute),
	)
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("expired session error = %v, want %v", err, ErrInvalidSession)
	}
}

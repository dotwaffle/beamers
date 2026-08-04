package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestBaselineMigrationInitializesCompleteSQLite(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize clean database: %v", err)
	}

	installation, err := Open(hostMaintenanceContext(t.Context()), dataDir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close migrated database: %v", closeErr)
		}
	}()
	if err = installation.StartupError(); err != nil {
		t.Fatalf("validate migrated database: %v", err)
	}
	if err = installation.Ready(hostMaintenanceContext(t.Context())); err != nil {
		t.Fatalf("query migrated database through Ent: %v", err)
	}
	current, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatalf("read current schema version: %v", err)
	}
	if installation.SchemaVersion() != 1 || current != 1 {
		t.Fatalf(
			"schema versions = installed %d/current %d, want 1/1",
			installation.SchemaVersion(),
			current,
		)
	}
	database, err := openValidationDatabase(
		hostMaintenanceContext(t.Context()),
		filepath.Join(dataDir, databaseFilename),
	)
	if err != nil {
		t.Fatalf("open migration metadata: %v", err)
	}
	defer func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("close migration metadata: %v", closeErr)
		}
	}()
	var name, safety string
	var minimumReader, minimumWriter int
	if err = database.QueryRowContext(
		hostMaintenanceContext(t.Context()),
		"SELECT name, safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version FROM beamers_schema_migrations "+
			"WHERE version = 1",
	).Scan(&name, &safety, &minimumReader, &minimumWriter); err != nil {
		t.Fatalf("read baseline metadata: %v", err)
	}
	if name != "baseline" ||
		safety != "Baseline" ||
		minimumReader != 1 ||
		minimumWriter != 1 {
		t.Fatalf(
			"baseline metadata = %q/%q/%d/%d",
			name,
			safety,
			minimumReader,
			minimumWriter,
		)
	}
	var triggers string
	if err = database.QueryRowContext(
		hostMaintenanceContext(t.Context()),
		"SELECT group_concat(name, ',') FROM "+
			"(SELECT name FROM sqlite_schema WHERE type = 'trigger' ORDER BY name)",
	).Scan(&triggers); err != nil {
		t.Fatalf("read baseline triggers: %v", err)
	}
	const expectedTriggers = "events_active_theme_revision_owner_insert," +
		"events_active_theme_revision_owner_update,lane_drafts_same_event_insert," +
		"lane_drafts_same_event_update,lane_published_versions_same_event_insert," +
		"session_draft_lanes_same_event_insert,session_draft_locations_same_event_insert," +
		"session_draft_tracks_same_event_insert,session_published_lanes_same_event_insert," +
		"session_published_locations_same_event_insert," +
		"session_published_tracks_same_event_insert"
	if triggers != expectedTriggers {
		t.Fatalf("baseline triggers = %q, want %q", triggers, expectedTriggers)
	}
	var registrationPolicies int
	if err = database.QueryRowContext(
		hostMaintenanceContext(t.Context()),
		"SELECT COUNT(*) FROM registration_policies",
	).Scan(&registrationPolicies); err != nil {
		t.Fatalf("read baseline Registration Policy: %v", err)
	}
	if registrationPolicies != 1 {
		t.Fatalf("baseline Registration Policies = %d, want 1", registrationPolicies)
	}
}

func TestTelemetryOpenTracesOperationsWithoutSQLOrDSN(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	installation, err := OpenWithTelemetry(
		hostMaintenanceContext(t.Context()),
		dataDir,
		tracerProvider,
		metricnoop.NewMeterProvider(),
	)
	if err != nil {
		t.Fatalf("open instrumented database: %v", err)
	}
	if err = installation.Ready(hostMaintenanceContext(t.Context())); err != nil {
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

func TestMigrationPlanRejectsUnknownCommittedPrefix(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize fixture database: %v", err)
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	database, err := openDatabase(hostMaintenanceContext(t.Context()), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err = database.ExecContext(
		hostMaintenanceContext(t.Context()),
		"UPDATE beamers_schema_migrations SET checksum = printf('%064d', 0) "+
			"WHERE version = 1",
	); err != nil {
		_ = database.Close()
		t.Fatalf("replace migration checksum: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}

	if _, err = PlanMigrations(hostMaintenanceContext(t.Context()), databasePath); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unknown prefix error = %v, want %v", err, ErrUnsupportedSchema)
	}
}

func TestDeclaredForwardWriterRangeAllowsNewerSchema(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	database, err := openDatabase(hostMaintenanceContext(t.Context()), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err = database.ExecContext(
		hostMaintenanceContext(t.Context()),
		"INSERT INTO beamers_schema_migrations "+
			"(version, name, checksum, safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version, applied_at) "+
			"VALUES (2, 'future_addition', printf('%064d', 1), "+
			"'NonDestructive', 1, 1, CURRENT_TIMESTAMP)",
	); err != nil {
		_ = database.Close()
		t.Fatalf("record future migration: %v", err)
	}
	if _, err = database.ExecContext(hostMaintenanceContext(t.Context()), "PRAGMA user_version = 2"); err != nil {
		_ = database.Close()
		t.Fatalf("set future schema version: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close future fixture: %v", err)
	}

	installation, err := Open(hostMaintenanceContext(t.Context()), dataDir)
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
	if installation.SchemaVersion() != 2 {
		t.Fatalf("opened schema version = %d, want 2", installation.SchemaVersion())
	}
}

func TestUnclassifiedMigrationRecordsClosedCompatibilityRange(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize fixture database: %v", err)
	}
	databasePath := filepath.Join(dataDir, databaseFilename)
	database, err := openDatabase(hostMaintenanceContext(t.Context()), databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	transaction, err := database.BeginTx(hostMaintenanceContext(t.Context()), nil)
	if err != nil {
		_ = database.Close()
		t.Fatalf("begin fixture transaction: %v", err)
	}
	future := migration{
		version:  2,
		name:     "unclassified_fixture",
		checksum: strings.Repeat("1", 64),
	}
	if err = recordMigration(hostMaintenanceContext(t.Context()), transaction, future); err != nil {
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
		hostMaintenanceContext(t.Context()),
		"SELECT safety, minimum_reader_schema_version, "+
			"minimum_writer_schema_version FROM beamers_schema_migrations "+
			"WHERE version = 2",
	).Scan(&safety, &minimumReader, &minimumWriter); err != nil {
		_ = database.Close()
		t.Fatalf("read unclassified migration: %v", err)
	}
	if err = database.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	if safety != string(MigrationUnclassified) ||
		minimumReader != 2 ||
		minimumWriter != 2 {
		t.Fatalf(
			"unclassified migration contract = %q/%d/%d",
			safety,
			minimumReader,
			minimumWriter,
		)
	}
}

func TestAuthenticationCredentialsExpire(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(hostMaintenanceContext(t.Context()), dataDir); err != nil {
		t.Fatalf("initialize authentication database: %v", err)
	}
	installation, err := Open(hostMaintenanceContext(t.Context()), dataDir)
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
		hostMaintenanceContext(t.Context()),
		expiredBootstrapHash,
		now,
		now.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("issue expiring bootstrap credential: %v", issueErr)
	}
	_, err = installation.BootstrapAdministrator(
		hostMaintenanceContext(t.Context()),
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
		hostMaintenanceContext(t.Context()),
		validBootstrapHash,
		bootstrapTime,
		bootstrapTime.Add(time.Minute),
	)
	if issueErr != nil {
		t.Fatalf("replace expired bootstrap credential: %v", issueErr)
	}
	sessionHash := strings.Repeat("d", 64)
	created, err := installation.BootstrapAdministrator(
		hostMaintenanceContext(t.Context()),
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
		hostMaintenanceContext(t.Context()),
		sessionHash,
		bootstrapTime.Add(2*time.Minute),
	)
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("expired session error = %v, want %v", err, ErrInvalidSession)
	}
}

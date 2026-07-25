// Package replication isolates optional Litestream disaster-recovery replication.
package replication

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/file"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

var errInvalidDestination = errors.New("replica destination must be an absolute file URL without credentials, query, or fragment")
var errMetricsUnavailable = errors.New("replication metrics unavailable")

// Config identifies the authoritative database and full-fidelity destination.
type Config struct {
	DatabasePath  string
	Destination   string
	Logger        *slog.Logger
	MeterProvider metric.MeterProvider
}

// Status is bounded operator-visible replication state.
type Status struct {
	Status             string     `json:"status"`
	Position           string     `json:"position,omitempty"`
	LagTransactions    uint64     `json:"lag_transactions"`
	LastSuccessfulSync *time.Time `json:"last_successful_sync,omitempty"`
	Synchronizing      bool       `json:"synchronizing"`
	ErrorClass         string     `json:"error_class,omitempty"`
	Errors             uint64     `json:"errors"`
}

// Adapter owns one optional Litestream store without owning process lifecycle.
type Adapter struct {
	config Config

	lifecycle     chan struct{}
	mu            sync.Mutex
	store         *litestream.Store
	database      *litestream.DB
	replica       *litestream.Replica
	status        string
	synchronizing bool
	errorClass    string
	errorAt       time.Time
	errors        uint64
	lastSync      time.Time
	setupErr      error

	errorCounter metric.Int64Counter
	metrics      metric.Registration
}

// New constructs a disabled-until-started adapter.
func New(config Config) *Adapter {
	if config.Logger == nil {
		config.Logger = slog.New(slog.DiscardHandler)
	}
	found := &Adapter{
		config:    config,
		lifecycle: make(chan struct{}, 1),
		status:    "configured",
	}
	found.lifecycle <- struct{}{}
	if err := found.registerMetrics(); err != nil {
		found.status = "degraded"
		found.errorClass = "telemetry"
		found.errors = 1
		found.setupErr = errors.Join(errMetricsUnavailable, err)
	}
	return found
}

// Start begins asynchronous replication. Callers may report errors but must not
// make authoritative service availability depend on the result.
func (adapter *Adapter) Start(ctx context.Context) error {
	if err := adapter.startupError(); err != nil {
		return err
	}
	if err := adapter.acquire(ctx); err != nil {
		adapter.recordError(ctx, err)
		return err
	}
	defer adapter.release()
	return adapter.start(ctx)
}

// StartAsync claims lifecycle ordering synchronously, then starts within the
// coordinator-provided budget.
func (adapter *Adapter) StartAsync(
	ctx context.Context,
	budget time.Duration,
) error {
	startContext, cancel := context.WithTimeout(ctx, budget)
	if err := adapter.startupError(); err != nil {
		cancel()
		return err
	}
	if err := adapter.acquire(startContext); err != nil {
		cancel()
		adapter.recordError(ctx, err)
		return err
	}
	go func() {
		defer adapter.release()
		defer cancel()
		_ = adapter.start(startContext)
	}()
	return nil
}

func (adapter *Adapter) startupError() error {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	setupErr := adapter.setupErr
	return setupErr
}

func (adapter *Adapter) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		err = context.Cause(ctx)
		adapter.recordError(ctx, err)
		return err
	}

	adapter.mu.Lock()
	if adapter.store != nil {
		adapter.mu.Unlock()
		return nil
	}
	adapter.status = "starting"
	adapter.mu.Unlock()

	destination, err := replicaPath(adapter.config.Destination)
	if err != nil {
		adapter.recordError(ctx, err)
		return err
	}
	if _, err = os.Stat(adapter.config.DatabasePath); err != nil {
		adapter.recordError(ctx, err)
		return err
	}
	database := litestream.NewDB(adapter.config.DatabasePath)
	client := file.NewReplicaClient(destination)
	replica := litestream.NewReplicaWithClient(database, client)
	database.Replica = replica
	client.Replica = replica

	libraryLogger := slog.New(libraryLogHandler{adapter: adapter})
	store := litestream.NewStore(
		[]*litestream.DB{database},
		litestream.DefaultCompactionLevels,
	)
	store.Logger = libraryLogger
	store.SetRetentionEnabled(false)
	store.SetShutdownSyncTimeout(0)
	database.SetLogger(libraryLogger)
	if err = store.Open(ctx); err != nil {
		adapter.recordError(ctx, err)
		return err
	}

	adapter.mu.Lock()
	adapter.store = store
	adapter.database = database
	adapter.replica = replica
	adapter.status = "replicating"
	adapter.errorClass = ""
	adapter.mu.Unlock()
	adapter.config.Logger.WarnContext(
		ctx,
		"replica contains full-fidelity secrets; protect its storage externally",
		"component", "replication",
		"status", "enabled",
	)
	return nil
}

// Finalize stops background work and attempts one final sync within ctx.
func (adapter *Adapter) Finalize(ctx context.Context) error {
	if err := adapter.acquire(ctx); err != nil {
		adapter.recordError(ctx, err)
		return err
	}
	defer adapter.release()

	adapter.mu.Lock()
	store := adapter.store
	database := adapter.database
	if store == nil {
		adapter.mu.Unlock()
		return nil
	}
	adapter.store = nil
	adapter.database = nil
	adapter.replica = nil
	adapter.status = "stopping"
	adapter.synchronizing = true
	adapter.mu.Unlock()

	err := store.Close(ctx)
	if err == nil && ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	lastSuccessfulSync := database.LastSuccessfulSyncAt()
	adapter.mu.Lock()
	adapter.synchronizing = false
	if !lastSuccessfulSync.IsZero() {
		adapter.lastSync = lastSuccessfulSync.UTC()
		adapter.errorAt = time.Time{}
		adapter.errorClass = ""
	}
	adapter.status = "stopped"
	adapter.mu.Unlock()
	if err != nil {
		adapter.recordError(ctx, err)
	}
	return err
}

func (adapter *Adapter) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-adapter.lifecycle:
		return nil
	}
}

func (adapter *Adapter) release() {
	adapter.lifecycle <- struct{}{}
}

// Status returns bounded state without exposing the destination or raw errors.
func (adapter *Adapter) Status() Status {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()

	found := Status{
		Status:        adapter.status,
		Synchronizing: adapter.synchronizing,
		ErrorClass:    adapter.errorClass,
		Errors:        adapter.errors,
	}
	if !adapter.lastSync.IsZero() {
		last := adapter.lastSync
		found.LastSuccessfulSync = &last
	}
	if adapter.database == nil || adapter.replica == nil {
		return found
	}
	local, err := adapter.database.Pos()
	if err != nil {
		found.Status = "degraded"
		found.ErrorClass = classifyError(err)
		return found
	}
	replicated := adapter.replica.Pos()
	if !replicated.IsZero() {
		found.Position = replicated.TXID.String()
	}
	if local.TXID > replicated.TXID {
		found.LagTransactions = uint64(local.TXID - replicated.TXID)
	}
	if last := adapter.database.LastSuccessfulSyncAt(); !last.IsZero() {
		last = last.UTC()
		adapter.lastSync = last
		found.LastSuccessfulSync = &last
		if last.After(adapter.errorAt) {
			adapter.status = "replicating"
			adapter.errorClass = ""
			adapter.errorAt = time.Time{}
			found.Status = "replicating"
			found.ErrorClass = ""
		}
	}
	return found
}

func (adapter *Adapter) recordError(ctx context.Context, err error) {
	class := classifyError(err)
	adapter.mu.Lock()
	changed := adapter.errorClass != class
	adapter.status = "degraded"
	adapter.errorClass = class
	adapter.errorAt = time.Now()
	adapter.errors++
	adapter.mu.Unlock()
	if adapter.errorCounter != nil {
		adapter.errorCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("class", class)))
	}
	if changed {
		adapter.config.Logger.WarnContext(
			ctx,
			"replication degraded",
			"component", "replication",
			"status", class,
		)
	}
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, errInvalidDestination):
		return "configuration"
	case errors.Is(err, errMetricsUnavailable):
		return "telemetry"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, fs.ErrNotExist):
		return "source_unavailable"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	default:
		return "destination"
	}
}

func replicaPath(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil ||
		parsed.Scheme != "file" ||
		parsed.Host != "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		!filepath.IsAbs(parsed.Path) {
		return "", errInvalidDestination
	}
	return filepath.Clean(parsed.Path), nil
}

func (adapter *Adapter) registerMetrics() error {
	provider := adapter.config.MeterProvider
	if provider == nil {
		provider = metricnoop.NewMeterProvider()
	}
	meter := provider.Meter("github.com/dotwaffle/beamers/internal/replication")
	position, err := meter.Int64ObservableGauge("beamers.replication.position")
	if err != nil {
		return fmt.Errorf("create replication position gauge: %w", err)
	}
	lag, err := meter.Int64ObservableGauge(
		"beamers.replication.lag",
		metric.WithUnit("{transaction}"),
	)
	if err != nil {
		return fmt.Errorf("create replication lag gauge: %w", err)
	}
	lastSync, err := meter.Int64ObservableGauge(
		"beamers.replication.last_success",
		metric.WithUnit("s"),
	)
	if err != nil {
		return fmt.Errorf("create replication last-success gauge: %w", err)
	}
	synchronizing, err := meter.Int64ObservableGauge(
		"beamers.replication.synchronizing",
	)
	if err != nil {
		return fmt.Errorf("create replication synchronization gauge: %w", err)
	}
	errorCounter, err := meter.Int64Counter("beamers.replication.errors")
	if err != nil {
		return fmt.Errorf("create replication error counter: %w", err)
	}
	registration, err := meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			status := adapter.Status()
			observer.ObserveInt64(
				position,
				txidMetric(status.Position),
				metric.WithAttributes(attribute.String("role", "replica")),
			)
			observer.ObserveInt64(lag, saturatingInt64(status.LagTransactions))
			if status.LastSuccessfulSync != nil {
				observer.ObserveInt64(lastSync, status.LastSuccessfulSync.Unix())
			}
			if status.Synchronizing {
				observer.ObserveInt64(synchronizing, 1)
			} else {
				observer.ObserveInt64(synchronizing, 0)
			}
			return nil
		},
		position,
		lag,
		lastSync,
		synchronizing,
	)
	if err != nil {
		return fmt.Errorf("register replication metrics: %w", err)
	}
	adapter.errorCounter = errorCounter
	adapter.metrics = registration
	return nil
}

func saturatingInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func txidMetric(value string) int64 {
	found, _ := strconv.ParseUint(value, 16, 64)
	return saturatingInt64(found)
}

type libraryLogHandler struct {
	adapter *Adapter
}

func (handler libraryLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (handler libraryLogHandler) Handle(ctx context.Context, record slog.Record) error {
	var found error
	record.Attrs(func(attribute slog.Attr) bool {
		if attribute.Key == "error" {
			found, _ = attribute.Value.Any().(error)
		}
		return true
	})
	if found == nil {
		found = errors.New("replication library failure")
	}
	handler.adapter.recordError(ctx, found)
	return nil
}

func (handler libraryLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler libraryLogHandler) WithGroup(string) slog.Handler {
	return handler
}

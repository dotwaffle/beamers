package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/replication"
	"github.com/dotwaffle/beamers/internal/telemetry"
)

// One pending refetch invalidation is sufficient. A second publication while
// it remains queued proves the subscriber is not draining and disconnects it.
const displaySubscriberQueueCapacity = 1

// Config contains the immutable service configuration.
type Config struct {
	DataDir         string
	AttachmentsDir  string
	ListenAddress   string
	BuildVersion    string
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
	TracerProvider  trace.TracerProvider
	MeterProvider   metric.MeterProvider
	Propagator      propagation.TextMapPropagator
	Telemetry       *telemetry.Runtime
	ReplicaURL      string
	ReplicationSync func(context.Context) error
}

// Run serves health endpoints until the context is canceled.
func Run(ctx context.Context, config Config) error {
	if config.BuildVersion == "" {
		return errors.New("server build version is required")
	}
	if config.ShutdownTimeout <= 0 {
		return errors.New("server shutdown timeout must be positive")
	}
	attachmentsDir := config.AttachmentsDir
	if attachmentsDir == "" {
		attachmentsDir = filepath.Join(config.DataDir, "attachments")
	}
	openConfig := operations.OpenConfig{
		DataDir: config.DataDir, AttachmentsDir: attachmentsDir,
	}
	if config.Telemetry != nil && config.Telemetry.Enabled() {
		openConfig.TracerProvider = config.TracerProvider
		openConfig.MeterProvider = config.MeterProvider
	}
	var err error
	upgrade, upgradeErr := operations.PrepareUpgrade(ctx, openConfig)
	var installation *operations.Installation
	if upgradeErr != nil {
		if errors.Is(upgradeErr, operations.ErrInstallationInUse) {
			return upgradeErr
		}
		installation, err = operations.OpenInstallationWithConfig(ctx, openConfig)
		if err != nil {
			return err
		}
	}
	var startupErr error
	if installation != nil {
		startupErr = installation.StartupError()
	}
	listenAddress := config.ListenAddress
	if startupErr != nil {
		listenAddress, err = localRecoveryAddress(listenAddress)
		if err != nil {
			return errors.Join(err, installation.Close())
		}
		config.Logger.Error(
			"storage unavailable; entering local recovery mode",
			"component", "storage",
			"error", startupErr,
		)
	}

	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", listenAddress)
	if err != nil {
		return errors.Join(err, installation.Close(), upgrade.Close())
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return errors.Join(err, listener.Close(), installation.Close(), upgrade.Close())
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return errors.Join(err, listener.Close(), installation.Close(), upgrade.Close())
	}
	var replica *replication.Adapter
	replicationSync := config.ReplicationSync
	replicationContext, cancelReplication := context.WithCancel(ctx)
	defer cancelReplication()
	if config.ReplicaURL != "" {
		replica = replication.New(replication.Config{
			DatabasePath:  filepath.Join(config.DataDir, "beamers.db"),
			Destination:   config.ReplicaURL,
			Logger:        config.Logger,
			MeterProvider: config.MeterProvider,
		})
		replicationSync = replica.Finalize
	}

	appConfig := applicationConfig{
		Config:          config,
		ReplicationDone: replicationContext.Done(),
		Installation:    installation,
		ListenerAddress: listener.Addr(),
		DisplayStream:   displayStream,
		ProgramStream:   programStream,
		Replication:     replica,
	}
	var application *application
	if upgrade != nil {
		application = newUpgradeApplication(appConfig, upgrade)
	} else {
		application, err = newApplication(appConfig)
		if err != nil {
			return errors.Join(err, listener.Close(), installation.Close())
		}
	}
	httpServer := &http.Server{
		Handler:           application,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	mode := "normal"
	if startupErr != nil {
		mode = "recovery"
	} else if upgrade != nil {
		mode = "upgrade"
	}
	config.Logger.Info("server listening", "address", listener.Addr().String(), "mode", mode)

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.Serve(listener)
	}()
	if startupErr == nil && upgrade == nil {
		application.startReplication(application.replicationContext()) //nolint:contextcheck // Replication follows the explicit server lifetime.
	}
	if upgrade != nil && !upgrade.Plan().RequiresApproval {
		result, upgradeErr := application.applyPreparedUpgrade(
			ctx,
			operations.UpgradeApproval{},
		)
		if upgradeErr != nil && !errors.Is(upgradeErr, context.Canceled) {
			config.Logger.Error(
				"automatic storage upgrade failed",
				"component", "storage",
				"rollback_backup", result.BackupPath,
				"error", upgradeErr,
			)
		}
	}

	select {
	case err := <-serveResult:
		cancelReplication()
		closeContext, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			config.ShutdownTimeout,
		)
		defer cancel()
		var replicationErr error
		if replicationSync != nil {
			replicationErr = replicationSync(closeContext)
		}
		return errors.Join(
			normalizeServeError(err),
			replicationErr,
			application.Close(),
		)
	case <-ctx.Done():
		shutdownStarted := time.Now()
		shutdownDeadline := shutdownStarted.Add(config.ShutdownTimeout)
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.ShutdownTimeout)
		defer cancel()

		activeRequests, drained := application.withdrawReadiness()
		logShutdown(
			config.Logger,
			shutdownStarted,
			shutdownDeadline,
			config.ShutdownTimeout,
			"readiness",
			"complete",
			"",
		)
		drainStatus := waitForTrafficDrain(
			shutdownContext,
			config.ShutdownTimeout,
			activeRequests,
			drained,
		)
		logShutdown(
			config.Logger,
			shutdownStarted,
			shutdownDeadline,
			config.ShutdownTimeout,
			"traffic_drain",
			drainStatus,
			"",
		)
		inFlightStatus := "skipped"
		if drainStatus == "timeout" && config.ShutdownTimeout >= 30*time.Second {
			activeRequests, drained = application.beginInFlightDrain()
			application.config.DisplayStream.Notify()
			application.config.ProgramStream.Notify()
			inFlightStatus = waitForInFlightDrain(
				shutdownContext,
				shutdownDeadline.Add(-10*time.Second),
				activeRequests,
				drained,
			)
		}
		logShutdown(
			config.Logger,
			shutdownStarted,
			shutdownDeadline,
			config.ShutdownTimeout,
			"in_flight",
			inFlightStatus,
			"",
		)
		application.beginShutdown()

		_, shutdownErr := finalizeShutdown(
			shutdownContext,
			shutdownFinalizers{
				http:        httpServer.Shutdown,
				forceHTTP:   httpServer.Close,
				replication: replicationSync,
				telemetry:   telemetryFinalizer(config.Telemetry),
				storage:     func(context.Context) error { return application.Close() },
			},
			func(result finalizerResult) {
				logShutdown(
					config.Logger,
					shutdownStarted,
					shutdownDeadline,
					config.ShutdownTimeout,
					"finalize",
					result.status(),
					result.name,
				)
			},
		)
		serveErr := normalizeServeError(<-serveResult)
		finalErr := errors.Join(shutdownErr, serveErr)
		logShutdown(
			config.Logger,
			shutdownStarted,
			shutdownDeadline,
			config.ShutdownTimeout,
			"shutdown",
			finalizerStatus(errors.Join(finalErr, shutdownContext.Err())),
			"",
		)
		return finalErr
	}
}

func waitForTrafficDrain(
	ctx context.Context,
	budget time.Duration,
	activeRequests int,
	drained <-chan struct{},
) string {
	if budget < 30*time.Second || activeRequests == 0 {
		return "skipped"
	}
	drainContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-drained:
		return "complete"
	case <-drainContext.Done():
		return finalizerStatus(context.Cause(drainContext))
	}
}

func waitForInFlightDrain(
	ctx context.Context,
	reserveStarts time.Time,
	activeRequests int,
	drained <-chan struct{},
) string {
	if activeRequests == 0 {
		return "complete"
	}
	drainContext, cancel := context.WithDeadline(ctx, reserveStarts)
	defer cancel()
	select {
	case <-drained:
		return "complete"
	case <-drainContext.Done():
		return finalizerStatus(context.Cause(drainContext))
	}
}

type finalizerResult struct {
	name           string
	err            error
	statusOverride string
}

func (result finalizerResult) status() string {
	if result.statusOverride != "" {
		return result.statusOverride
	}
	return finalizerStatus(result.err)
}

type shutdownFinalizer struct {
	name string
	run  func(context.Context) error
}

type shutdownFinalizers struct {
	http        func(context.Context) error
	forceHTTP   func() error
	replication func(context.Context) error
	telemetry   func(context.Context) error
	storage     func(context.Context) error
}

func finalizeShutdown(
	ctx context.Context,
	finalizers shutdownFinalizers,
	observe func(finalizerResult),
) (bool, error) {
	httpContext, cancelHTTP := shutdownPhaseContext(ctx, 1, 4)
	httpTimedOut, httpErr := runFinalizers(
		httpContext,
		[]shutdownFinalizer{{name: "http", run: finalizers.http}},
		observe,
	)
	cancelHTTP()
	if httpTimedOut && finalizers.forceHTTP != nil {
		httpErr = errors.Join(httpErr, finalizers.forceHTTP())
	}

	synchronization := make([]shutdownFinalizer, 0, 2)
	if finalizers.replication == nil {
		observe(finalizerResult{name: "replication", statusOverride: "skipped"})
	} else {
		synchronization = append(synchronization, shutdownFinalizer{
			name: "replication",
			run:  finalizers.replication,
		})
	}
	if finalizers.telemetry == nil {
		observe(finalizerResult{name: "telemetry", statusOverride: "skipped"})
	} else {
		synchronization = append(synchronization, shutdownFinalizer{
			name: "telemetry",
			run:  finalizers.telemetry,
		})
	}
	var synchronizationWait sync.WaitGroup
	for index := range synchronization {
		run := synchronization[index].run
		synchronizationWait.Add(1)
		synchronization[index].run = func(ctx context.Context) error {
			defer synchronizationWait.Done()
			return run(ctx)
		}
	}
	syncContext, cancelSync := shutdownPhaseContext(ctx, 3, 4)
	syncTimedOut, syncErr := runFinalizers(syncContext, synchronization, observe)
	cancelSync()
	if syncTimedOut {
		synchronizationStopped := make(chan struct{})
		go func() {
			synchronizationWait.Wait()
			close(synchronizationStopped)
		}()
		select {
		case <-synchronizationStopped:
		case <-ctx.Done():
			observe(finalizerResult{name: "storage", statusOverride: "skipped"})
			return true, errors.Join(httpErr, syncErr)
		}
	}

	storageTimedOut, storageErr := runFinalizers(
		ctx,
		[]shutdownFinalizer{{name: "storage", run: finalizers.storage}},
		observe,
	)
	return httpTimedOut || storageTimedOut, errors.Join(httpErr, syncErr, storageErr)
}

func shutdownPhaseContext(
	ctx context.Context,
	numerator, denominator int,
) (context.Context, context.CancelFunc) {
	deadline, found := ctx.Deadline()
	if !found {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	return context.WithDeadline(
		ctx,
		time.Now().Add(remaining*time.Duration(numerator)/time.Duration(denominator)),
	)
}

func runFinalizers(
	ctx context.Context,
	finalizers []shutdownFinalizer,
	observe func(finalizerResult),
) (bool, error) {
	results := make(chan finalizerResult, len(finalizers))
	pending := make(map[string]shutdownFinalizer, len(finalizers))
	for _, finalizer := range finalizers {
		pending[finalizer.name] = finalizer
		go func() {
			results <- finalizerResult{
				name: finalizer.name,
				err:  finalizer.run(ctx),
			}
		}()
	}
	var foundErr error
	for len(pending) > 0 {
		select {
		case result := <-results:
			_, expected := pending[result.name]
			if !expected {
				continue
			}
			delete(pending, result.name)
			foundErr = errors.Join(foundErr, result.err)
			observe(result)
		case <-ctx.Done():
			for name := range pending {
				result := finalizerResult{name: name, err: context.Cause(ctx)}
				observe(result)
				foundErr = errors.Join(foundErr, result.err)
			}
			return true, foundErr
		}
	}
	return false, foundErr
}

func telemetryFinalizer(runtime *telemetry.Runtime) func(context.Context) error {
	if runtime == nil || !runtime.Enabled() {
		return nil
	}
	return runtime.Shutdown
}

func logShutdown(
	logger *slog.Logger,
	started time.Time,
	deadline time.Time,
	budget time.Duration,
	phase string,
	status string,
	finalizer string,
) {
	elapsed := time.Since(started)
	remaining := max(time.Until(deadline), 0)
	attributes := []any{
		"component", "shutdown",
		"phase", phase,
		"status", status,
		"budget_ms", budget.Milliseconds(),
		"elapsed_ms", elapsed.Milliseconds(),
		"remaining_ms", remaining.Milliseconds(),
	}
	if finalizer != "" {
		attributes = append(attributes, "finalizer", finalizer)
	}
	logger.Info("shutdown progress", attributes...)
}

func finalizerStatus(err error) string {
	switch {
	case err == nil:
		return "complete"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "error"
	}
}

func localRecoveryAddress(address string) (string, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.Join(errors.New("parse recovery listen address"), err)
	}
	return net.JoinHostPort("127.0.0.1", port), nil
}

func liveness(response http.ResponseWriter, request *http.Request) {
	if !probeMethodAllowed(response, request) {
		return
	}
	setProbeHeaders(response)
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("live\n"))
}

func probeMethodAllowed(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	response.Header().Set("Allow", "GET, HEAD")
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func setProbeHeaders(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

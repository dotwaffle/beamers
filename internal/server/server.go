package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
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

	appConfig := applicationConfig{
		Config:          config,
		Installation:    installation,
		ListenerAddress: listener.Addr(),
		DisplayStream:   displayStream,
		ProgramStream:   programStream,
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
		return errors.Join(normalizeServeError(err), application.Close())
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

		// Start HTTP and storage closure together so final synchronization uses
		// the entire remaining platform budget.
		shutdownResults := make(chan finalizerResult, 2)
		go func() {
			shutdownResults <- finalizerResult{
				name: "http", err: httpServer.Shutdown(shutdownContext),
			}
		}()
		go func() {
			shutdownResults <- finalizerResult{name: "storage", err: application.Close()}
		}()
		finalizerTimedOut, shutdownErr := collectFinalizers(
			shutdownContext,
			[]string{"http", "storage"},
			shutdownResults,
			func(result finalizerResult) {
				logShutdown(
					config.Logger,
					shutdownStarted,
					shutdownDeadline,
					config.ShutdownTimeout,
					"finalize",
					finalizerStatus(result.err),
					result.name,
				)
			},
		)
		if finalizerTimedOut {
			shutdownErr = errors.Join(shutdownErr, httpServer.Close())
		}
		if config.Telemetry == nil || !config.Telemetry.Enabled() {
			logShutdown(
				config.Logger,
				shutdownStarted,
				shutdownDeadline,
				config.ShutdownTimeout,
				"finalize",
				"skipped",
				"telemetry",
			)
		} else {
			telemetryErr := config.Telemetry.Shutdown(shutdownContext)
			logShutdown(
				config.Logger,
				shutdownStarted,
				shutdownDeadline,
				config.ShutdownTimeout,
				"finalize",
				finalizerStatus(telemetryErr),
				"telemetry",
			)
		}
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
	name string
	err  error
}

func collectFinalizers(
	ctx context.Context,
	names []string,
	results <-chan finalizerResult,
	observe func(finalizerResult),
) (bool, error) {
	pending := make(map[string]struct{}, len(names))
	for _, name := range names {
		pending[name] = struct{}{}
	}
	var finalizerErr error
	for len(pending) != 0 {
		select {
		case result := <-results:
			if _, expected := pending[result.name]; !expected {
				continue
			}
			delete(pending, result.name)
			finalizerErr = errors.Join(finalizerErr, result.err)
			observe(result)
		case <-ctx.Done():
			for name := range pending {
				observe(finalizerResult{name: name, err: context.Cause(ctx)})
			}
			return true, errors.Join(finalizerErr, context.Cause(ctx))
		}
	}
	return false, finalizerErr
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

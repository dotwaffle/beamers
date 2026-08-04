package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/federation"
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
	TLSCertificate  string
	TLSPrivateKey   string
	TrustedProxies  []netip.Prefix
	PublicListen    string
	InsecureCrew    bool
	InsecureDisplay bool
	Demo            bool
	SceneID         *federation.Config
}

// Run serves health endpoints until the context is canceled.
func Run(ctx context.Context, config Config) error {
	if config.BuildVersion == "" {
		return errors.New("server build version is required")
	}
	if config.ShutdownTimeout <= 0 {
		return errors.New("server shutdown timeout must be positive")
	}
	if (config.TLSCertificate == "") != (config.TLSPrivateKey == "") {
		return errors.New("TLS certificate and private key must be configured together")
	}
	var (
		err     error
		sceneID *federation.SceneID
	)
	if config.SceneID != nil {
		transport := otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithTracerProvider(config.TracerProvider),
			otelhttp.WithMeterProvider(config.MeterProvider),
			otelhttp.WithPropagators(config.Propagator),
		)
		sceneID, err = federation.NewSceneID(
			*config.SceneID,
			&http.Client{Transport: transport, Timeout: 5 * time.Second},
		)
		if err != nil {
			return err
		}
	}
	attachmentsDir := config.AttachmentsDir
	if attachmentsDir == "" {
		attachmentsDir = filepath.Join(config.DataDir, "attachments")
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return err
	}
	scheduleStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return err
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return err
	}
	votingStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return err
	}
	openConfig := operations.OpenConfig{
		DataDir: config.DataDir, AttachmentsDir: attachmentsDir,
		NotifyDisplays: displayStream.Notify,
		NotifySchedule: scheduleStream.Notify,
		NotifyProgram:  programStream.Notify,
		NotifyVoting:   votingStream.Notify,
	}
	if config.Telemetry != nil && config.Telemetry.Enabled() {
		openConfig.TracerProvider = config.TracerProvider
		openConfig.MeterProvider = config.MeterProvider
	}
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
	var publicListener net.Listener
	if config.PublicListen != "" && startupErr == nil {
		publicListener, err = listenConfig.Listen(ctx, "tcp", config.PublicListen)
		if err != nil {
			return errors.Join(err, listener.Close(), installation.Close(), upgrade.Close())
		}
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
		ScheduleStream:  scheduleStream,
		VotingStream:    votingStream,
		Replication:     replica,
		SceneID:         sceneID,
	}
	var application *application
	if upgrade != nil {
		application = newUpgradeApplication(appConfig, upgrade)
	} else {
		application, err = newApplication(ctx, appConfig)
		if err != nil {
			return errors.Join(err, listener.Close(), closeListener(publicListener), installation.Close())
		}
	}
	if startupErr == nil {
		recordedConfig := config
		recordedConfig.AttachmentsDir = attachmentsDir
		recordedConfig.ListenAddress = listenAddress
		if err = backup.RecordConfiguration(backupConfiguration(recordedConfig)); err != nil {
			return errors.Join(
				err,
				listener.Close(),
				closeListener(publicListener),
				application.Close(),
			)
		}
	}
	privateHandler := instrumentInboundHTTP(
		config,
		"beamers.private",
		protectInterfaces(application, interfacePolicy{
			logger:          config.Logger,
			listenerAddress: listener.Addr(), trustedProxies: config.TrustedProxies,
			allowInsecureCrew: config.InsecureCrew, allowInsecureDisplay: config.InsecureDisplay,
			demo: config.Demo,
		}),
	)
	httpServer := &http.Server{
		Handler:           privateHandler,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var publicServer *http.Server
	if publicListener != nil {
		publicServer = &http.Server{
			Handler: instrumentInboundHTTP(
				config,
				"beamers.public",
				protectInterfaces(application, interfacePolicy{
					logger:          config.Logger,
					listenerAddress: publicListener.Addr(),
					trustedProxies:  config.TrustedProxies,
					publicOnly:      true,
				}),
			),
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
	}
	mode := "normal"
	if startupErr != nil {
		mode = "recovery"
	} else if upgrade != nil {
		mode = "upgrade"
	}
	if config.InsecureCrew {
		config.Logger.Warn(
			"insecure Crew mode enabled; credentials and sessions may be observed",
			"component", "interfaces",
		)
	}
	if config.InsecureDisplay {
		config.Logger.Warn(
			"insecure Display mode enabled; credentials and content may be observed",
			"component", "interfaces",
		)
	}
	scheme := "http"
	if config.TLSCertificate != "" {
		scheme = "https"
	}
	config.Logger.Info(
		"server listening",
		"address", listener.Addr().String(),
		"mode", mode,
		"scheme", scheme,
	)

	serveResult := make(chan error, 2)
	serving := 1
	go func() {
		if config.TLSCertificate != "" {
			serveResult <- httpServer.ServeTLS(
				listener,
				config.TLSCertificate,
				config.TLSPrivateKey,
			)
			return
		}
		serveResult <- httpServer.Serve(listener)
	}()
	if publicServer != nil {
		config.Logger.Info(
			"public server listening",
			"address", publicListener.Addr().String(),
			"mode", mode,
			"scheme", "http",
		)
		serving++
		go func() {
			serveResult <- publicServer.Serve(publicListener)
		}()
	}
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
		closeErr := errors.Join(httpServer.Close(), closeHTTPServer(publicServer))
		return errors.Join(
			normalizeServeError(err),
			closeErr,
			drainServeResults(closeContext, drainInput{
				logger:  config.Logger,
				results: serveResult,
				count:   serving - 1,
			}),
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
			application.config.ScheduleStream.Notify()
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
				http: func(ctx context.Context) error {
					return errors.Join(
						httpServer.Shutdown(ctx),
						shutdownHTTPServer(ctx, publicServer),
					)
				},
				forceHTTP: func() error {
					return errors.Join(httpServer.Close(), closeHTTPServer(publicServer))
				},
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
		serveErr := drainServeResults(shutdownContext, drainInput{
			logger:  config.Logger,
			results: serveResult,
			count:   serving,
		})
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

// instrumentInboundHTTP wraps a listener's handler with otelhttp so inbound
// requests get request metrics and a server span, giving downstream otelsql
// database spans a parent. operation names the span/metric per listener
// (private vs. public) so the two are distinguishable in exported data.
func instrumentInboundHTTP(config Config, operation string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(
		next,
		operation,
		otelhttp.WithTracerProvider(config.TracerProvider),
		otelhttp.WithMeterProvider(config.MeterProvider),
		otelhttp.WithPropagators(config.Propagator),
	)
}

func closeListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func shutdownHTTPServer(ctx context.Context, server *http.Server) error {
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func closeHTTPServer(server *http.Server) error {
	if server == nil {
		return nil
	}
	return server.Close()
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

type drainInput struct {
	logger  *slog.Logger
	results <-chan error
	count   int
}

// drainServeResults collects the result of every serve goroutine. A listener
// that stopped for its own reason, rather than because shutdown closed it, is
// reported instead of being left unread behind the first result.
func drainServeResults(ctx context.Context, input drainInput) error {
	var found error
	for range input.count {
		select {
		case result := <-input.results:
			err := normalizeServeError(result)
			if err == nil {
				continue
			}
			input.logger.ErrorContext(
				ctx,
				"server stopped serving",
				slog.String("component", "server"),
				slog.String("error", err.Error()),
			)
			found = errors.Join(found, err)
		case <-ctx.Done():
			input.logger.Warn(
				"server shutdown ended before every listener stopped",
				slog.String("component", "server"),
			)
			return found
		}
	}
	return found
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

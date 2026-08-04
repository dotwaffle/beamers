package server

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/federation"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/replication"
)

type applicationConfig struct {
	Config
	ReplicationDone <-chan struct{}
	Installation    *operations.Installation
	ListenerAddress net.Addr
	DisplayStream   *displaystream.Hub
	ProgramStream   *displaystream.Hub
	ScheduleStream  *displaystream.Hub
	VotingStream    *displaystream.Hub
	Replication     *replication.Adapter
	SceneID         *federation.SceneID
}

var errRestoreInProgress = errors.New("restore already in progress")

// application keeps probes stable while normal routes are rebuilt after Restore.
type application struct {
	config applicationConfig

	mu              sync.Mutex
	installation    *operations.Installation
	handler         *routeMux
	accepting       bool
	maintenance     bool
	maintenanceKind string
	restoring       bool
	upgrade         *operations.Upgrade
	upgradeApplying bool
	rejectMutations bool
	rejectStreams   bool
	active          int
	nextRequest     uint64
	cancels         map[uint64]context.CancelCauseFunc
	persistent      map[uint64]bool
	drained         chan struct{}
	scheduleMetrics *scheduleStreamMetrics
}

func newUpgradeApplication(
	config applicationConfig,
	upgrade *operations.Upgrade,
) *application {
	app := &application{
		config:          config,
		maintenance:     true,
		maintenanceKind: "upgrade",
		upgrade:         upgrade,
		cancels:         make(map[uint64]context.CancelCauseFunc),
		persistent:      make(map[uint64]bool),
		drained:         closedChannel(),
		scheduleMetrics: &scheduleStreamMetrics{},
	}
	app.handler = app.probeRoutes()
	app.handler.HandleFunc(
		"/admin/upgrade",
		crewRoute(),
		app.upgradePreview,
	)
	app.handler.HandleFunc(
		"/admin/upgrade/apply",
		routeContract{kind: crewInterface, recoveryLimit: 5},
		app.upgradeApply,
	)
	return app
}

type applicationRequestContextKey struct{}

type applicationRequest struct {
	id       uint64
	detached bool
}

func newApplication(ctx context.Context, config applicationConfig) (*application, error) {
	if config.ScheduleStream == nil {
		var err error
		config.ScheduleStream, err = displaystream.NewProcess(displaySubscriberQueueCapacity)
		if err != nil {
			return nil, err
		}
	}
	if config.VotingStream == nil {
		var err error
		config.VotingStream, err = displaystream.NewProcess(displaySubscriberQueueCapacity)
		if err != nil {
			return nil, err
		}
	}
	found := &application{
		config:          config,
		installation:    config.Installation,
		accepting:       config.Installation.StartupError() == nil,
		cancels:         make(map[uint64]context.CancelCauseFunc),
		persistent:      make(map[uint64]bool),
		drained:         closedChannel(),
		scheduleMetrics: &scheduleStreamMetrics{},
	}
	handler, err := found.buildHandler(ctx, config.Installation)
	if err != nil {
		return nil, err
	}
	found.handler = handler
	return found, nil
}

func (application *application) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	application.mu.Lock()
	handler := application.handler
	contract, found := handler.contract(request)
	if !found {
		application.mu.Unlock()
		http.NotFound(response, request)
		return
	}
	if contract.kind == probeInterface {
		application.mu.Unlock()
		handler.ServeHTTP(response, request)
		return
	}
	if application.maintenance {
		kind := application.maintenanceKind
		application.mu.Unlock()
		if kind == "upgrade" {
			handler.ServeHTTP(response, request)
			return
		}
		serveMaintenance(response, request, kind)
		return
	}
	persistent := contract.persistent
	if (application.rejectMutations && requestMutates(request, contract)) ||
		(application.rejectStreams && persistent) {
		application.mu.Unlock()
		serveMaintenance(response, request, "shutdown")
		return
	}
	if application.active == 0 {
		application.drained = make(chan struct{})
	}
	application.active++
	application.nextRequest++
	tracked := &applicationRequest{id: application.nextRequest}
	requestContext, cancel := context.WithCancelCause(request.Context())
	application.cancels[tracked.id] = cancel
	if persistent {
		application.persistent[tracked.id] = true
	}
	request = request.WithContext(context.WithValue(
		requestContext,
		applicationRequestContextKey{},
		tracked,
	))
	application.mu.Unlock()
	defer func() {
		cancel(nil)
		application.mu.Lock()
		if !tracked.detached {
			delete(application.cancels, tracked.id)
			delete(application.persistent, tracked.id)
			application.finishRequestLocked()
		}
		application.mu.Unlock()
	}()
	handler.ServeHTTP(response, request)
}

func (application *application) readiness(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !probeMethodAllowed(response, request) {
		return
	}
	setProbeHeaders(response)
	application.mu.Lock()
	accepting := application.accepting
	installation := application.installation
	application.mu.Unlock()
	if !accepting || installation == nil {
		http.Error(response, "not ready", http.StatusServiceUnavailable)
		return
	}
	probeContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := installation.Ready(probeContext); err != nil {
		application.config.Logger.LogAttrs(
			request.Context(),
			slog.LevelError,
			"readiness storage probe failed",
			slog.String("component", "storage"),
			slog.Any("error", err),
		)
		http.Error(response, "not ready", http.StatusServiceUnavailable)
		return
	}
	recovered, err := installation.Recover(probeContext)
	if err != nil {
		application.config.Logger.LogAttrs(
			request.Context(),
			slog.LevelError,
			"storage recovery flush failed",
			slog.String("component", "storage"),
			slog.Any("error", err),
		)
		http.Error(response, "not ready", http.StatusServiceUnavailable)
		return
	}
	if recovered {
		application.config.Logger.InfoContext(
			request.Context(),
			"persisted degraded Emergency Alert evidence",
			"component", "storage",
		)
		application.config.DisplayStream.Notify()
	}
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ready\n"))
}

func (application *application) contract(
	request *http.Request,
) (routeContract, bool) {
	application.mu.Lock()
	handler := application.handler
	application.mu.Unlock()
	return handler.contract(request)
}

func (application *application) cancelPreparedRestore(
	ctx context.Context,
	journalPath string,
) error {
	return application.runRestoreMaintenance(ctx, func() error {
		return backup.CancelPreparedRestore(ctx, journalPath)
	})
}

func (application *application) runRestoreMaintenance(
	ctx context.Context,
	operation func() error,
) error {
	installation, err := application.beginRestore(ctx)
	if err != nil {
		return err
	}
	application.stopReplication(ctx)
	if err = installation.Close(); err != nil {
		application.setUnavailable(installation)
		return err
	}
	operationErr := operation()
	reopened, openErr := operations.OpenInstallationWithConfig(ctx, application.openConfig())
	if openErr != nil {
		application.setUnavailable(nil)
		return errors.Join(operationErr, openErr)
	}
	handler, buildErr := application.buildHandler(ctx, reopened)
	if buildErr != nil {
		application.setUnavailable(reopened)
		return errors.Join(operationErr, buildErr)
	}

	application.mu.Lock()
	application.installation = reopened
	application.handler = handler
	application.maintenance = false
	application.maintenanceKind = ""
	application.restoring = false
	application.accepting = reopened.StartupError() == nil
	application.rejectMutations = false
	application.rejectStreams = false
	application.config.DisplayStream.Notify()
	application.config.ScheduleStream.Notify()
	application.mu.Unlock()
	application.startReplication(application.replicationContext()) //nolint:contextcheck // Replication follows the server, not the completed Restore request.
	return operationErr
}

func (application *application) beginRestore(
	ctx context.Context,
) (*operations.Installation, error) {
	application.mu.Lock()
	if application.restoring {
		application.mu.Unlock()
		return nil, errRestoreInProgress
	}
	application.restoring = true
	application.maintenance = true
	application.maintenanceKind = "restore"
	application.accepting = false
	if current, ok := ctx.Value(applicationRequestContextKey{}).(*applicationRequest); ok {
		if _, tracked := application.cancels[current.id]; tracked {
			current.detached = true
			delete(application.cancels, current.id)
			delete(application.persistent, current.id)
			application.finishRequestLocked()
		}
	}
	for _, cancel := range application.cancels {
		cancel(errors.New("installation entering Restore maintenance"))
	}
	drained := application.drained
	installation := application.installation
	application.mu.Unlock()
	select {
	case <-drained:
		return installation, nil
	case <-ctx.Done():
		application.mu.Lock()
		application.restoring = false
		application.maintenance = false
		application.maintenanceKind = ""
		application.accepting = installation != nil && installation.StartupError() == nil
		application.rejectMutations = false
		application.rejectStreams = false
		application.mu.Unlock()
		return nil, context.Cause(ctx)
	}
}

func (application *application) finishRequestLocked() {
	application.active--
	if application.active == 0 {
		close(application.drained)
	}
}

func (application *application) setUnavailable(installation *operations.Installation) {
	application.mu.Lock()
	application.installation = installation
	application.accepting = false
	application.maintenance = true
	application.maintenanceKind = "restore"
	application.restoring = false
	application.rejectMutations = true
	application.rejectStreams = true
	application.mu.Unlock()
}

func (application *application) applyPreparedUpgrade(
	ctx context.Context,
	approval operations.UpgradeApproval,
) (operations.UpgradeResult, error) {
	application.mu.Lock()
	if application.upgradeApplying || application.upgrade == nil {
		application.mu.Unlock()
		return operations.UpgradeResult{}, errors.New("upgrade is not available")
	}
	upgrade := application.upgrade
	application.upgrade = nil
	application.upgradeApplying = true
	application.mu.Unlock()

	result, err := upgrade.Apply(ctx, approval)
	if err != nil {
		application.mu.Lock()
		application.upgradeApplying = false
		application.mu.Unlock()
		return result, err
	}
	reopened, err := operations.OpenInstallationWithConfig(ctx, application.openConfig())
	if err != nil {
		application.mu.Lock()
		application.upgradeApplying = false
		application.mu.Unlock()
		return result, err
	}
	handler, err := application.buildHandler(ctx, reopened)
	if err != nil {
		err = errors.Join(err, reopened.Close())
		application.mu.Lock()
		application.upgradeApplying = false
		application.mu.Unlock()
		return result, err
	}
	application.mu.Lock()
	application.installation = reopened
	application.handler = handler
	application.accepting = true
	application.maintenance = false
	application.maintenanceKind = ""
	application.upgradeApplying = false
	application.rejectMutations = false
	application.rejectStreams = false
	application.mu.Unlock()
	application.startReplication(application.replicationContext()) //nolint:contextcheck // Replication follows the server, not the completed upgrade request.
	return result, nil
}

func (application *application) Close() error {
	application.beginShutdown()
	application.mu.Lock()
	installation := application.installation
	application.installation = nil
	upgrade := application.upgrade
	application.upgrade = nil
	application.mu.Unlock()
	var installationErr error
	if installation != nil {
		installationErr = installation.Close()
	}
	return errors.Join(installationErr, upgrade.Close())
}

func (application *application) startReplication(ctx context.Context) {
	if application.config.Replication == nil {
		return
	}
	if err := application.config.Replication.StartAsync(
		ctx,
		application.config.ShutdownTimeout,
	); err != nil {
		application.config.Logger.WarnContext(
			ctx,
			"replication unavailable; authoritative service remains active",
			"component", "replication",
			"status", application.config.Replication.Status().ErrorClass,
		)
	}
}

func (application *application) stopReplication(ctx context.Context) {
	if application.config.Replication == nil {
		return
	}
	stopContext, cancel := context.WithTimeout(ctx, application.config.ShutdownTimeout)
	defer cancel()
	if err := application.config.Replication.Finalize(stopContext); err != nil {
		application.config.Logger.WarnContext(
			stopContext,
			"replication final synchronization failed",
			"component", "replication",
			"status", application.config.Replication.Status().ErrorClass,
		)
	}
}

func (application *application) replicationContext() context.Context {
	return replicationLifetimeContext{done: application.config.ReplicationDone}
}

type replicationLifetimeContext struct {
	done <-chan struct{}
}

func (ctx replicationLifetimeContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx replicationLifetimeContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx replicationLifetimeContext) Err() error {
	select {
	case <-ctx.done:
		return context.Canceled
	default:
		return nil
	}
}

func (replicationLifetimeContext) Value(any) any {
	return nil
}

func (application *application) beginShutdown() {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.accepting = false
	application.maintenance = true
	application.maintenanceKind = "shutdown"
	application.rejectMutations = true
	application.rejectStreams = true
	for _, cancel := range application.cancels {
		cancel(errors.New("application closing"))
	}
}

func (application *application) beginInFlightDrain() (int, <-chan struct{}) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.rejectMutations = true
	application.rejectStreams = true
	for id := range application.persistent {
		if cancel := application.cancels[id]; cancel != nil {
			cancel(errors.New("persistent stream closing"))
		}
	}
	return application.active, application.drained
}

func (application *application) withdrawReadiness() (int, <-chan struct{}) {
	application.mu.Lock()
	defer application.mu.Unlock()
	application.accepting = false
	return application.active, application.drained
}

func requestMutates(request *http.Request, contract routeContract) bool {
	if request.Method == http.MethodGet {
		return contract.mutatesOnRead
	}
	return request.Method != http.MethodHead
}

func (application *application) openConfig() operations.OpenConfig {
	config := operations.OpenConfig{
		DataDir: application.config.DataDir, AttachmentsDir: application.config.AttachmentsDir,
		NotifyDisplays: application.config.DisplayStream.Notify,
		NotifySchedule: application.config.ScheduleStream.Notify,
		NotifyProgram:  application.config.ProgramStream.Notify,
		NotifyVoting:   application.config.VotingStream.Notify,
	}
	if application.config.Telemetry != nil && application.config.Telemetry.Enabled() {
		config.TracerProvider = application.config.TracerProvider
		config.MeterProvider = application.config.MeterProvider
	}
	return config
}

func serveMaintenance(
	response http.ResponseWriter,
	request *http.Request,
	kind string,
) {
	if kind == "" {
		kind = "restore"
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Retry-After", "1")
	response.Header().Set("X-Beamers-Maintenance", kind)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(response, "maintenance in progress", http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusServiceUnavailable)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write([]byte(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<meta http-equiv="refresh" content="1">
<title>Beamers maintenance</title></head>
<body><main><h1>Installation maintenance</h1>
<p>Maintenance state: <strong>` + html.EscapeString(kind) + `</strong></p>
<p role="status">Beamers will return to this page when storage is ready.</p></main>
</body></html>`))
}

func closedChannel() chan struct{} {
	found := make(chan struct{})
	close(found)
	return found
}

func (application *application) buildHandler(
	ctx context.Context,
	installation *operations.Installation,
) (*routeMux, error) {
	mux := application.probeRoutes()
	if startupErr := installation.StartupError(); startupErr != nil {
		mux.HandleFunc("/diagnostics", crewRoute(), func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			if !probeMethodAllowed(response, request) {
				return
			}
			detail := strings.ToValidUTF8(startupErr.Error(), "")
			if len(detail) > 512 {
				detail = strings.ToValidUTF8(detail[:512], "")
			}
			storage := "unavailable"
			switch {
			case errors.Is(startupErr, operations.ErrUninitialized):
				storage = "uninitialized"
			case errors.Is(startupErr, operations.ErrUnsupportedSchema):
				storage = "unsupported_schema"
			}
			response.Header().Set("Cache-Control", "no-store")
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]string{
				"mode": "recovery", "storage": storage, "detail": detail,
			})
		})
		// Recovery mode deliberately exposes only local diagnostics and stable probes.
		return mux, nil //nolint:nilerr // StartupError selects the restricted handler.
	}
	if err := installation.RecordInterfaceModes(
		ctx,
		application.config.InsecureCrew,
		application.config.InsecureDisplay,
	); err != nil {
		return nil, err
	}
	diagnostics := registerDiagnosticsRoutes(
		mux,
		installation.Authentication(),
		installation,
		installation.Displays(),
		application.config.DisplayStream,
		application.config.ProgramStream,
		application.config.ScheduleStream,
		application.config.VotingStream,
		application.scheduleMetrics,
		application.config.Telemetry,
		application.config.Replication,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerPprofRoutes(
		mux,
		installation.Authentication(),
		application.config.Logger,
		application.config.ListenerAddress,
	)
	authenticationLimiter := newAuthFailureLimiter(time.Now, application.config.Logger)
	registerAuthenticationRoutes(
		mux,
		installation.Authentication(),
		application.config.Logger,
		application.config.ListenerAddress,
		authenticationLimiter,
	)
	registerFederationRoutes(
		mux,
		installation.Authentication(),
		application.config.SceneID,
		application.config.Logger,
	)
	var sceneID *federation.PublicProvider
	if application.config.SceneID != nil {
		public := application.config.SceneID.Public()
		sceneID = &public
	}
	if err := registerFrontendRoutes(
		mux,
		installation.Authentication(),
		authenticationLimiter,
		installation.RundownQueries(),
		installation.Events(),
		application.config.Logger,
		sceneID,
	); err != nil {
		return nil, err
	}
	registerThemeRoutes(
		mux,
		installation.Authentication(),
		installation.Themes(),
		installation.EventThemes(),
		application.config.Logger,
	)
	registerPlanningRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.Attachments(),
		installation.RundownCommands(),
		installation.RundownQueries(),
		application.config.Logger,
	)
	registerAdministrationRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.Activation(),
		application.config.Logger,
	)
	registerVotingRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.Voting(),
		authenticationLimiter,
		application.config.VotingStream,
		application.config.Logger,
	)
	registerOperationRoutes(
		mux,
		installation.Authentication(),
		installation.Activation(),
		installation.Events(),
		installation.RundownQueries(),
		installation.SessionControl(),
		installation.Competition(),
		installation.Displays(),
		application.config.DisplayStream,
		application.config.Logger,
	)
	registerControlRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.RundownQueries(),
		installation.Overrides(),
		application.config.DisplayStream,
		application.config.BuildVersion,
		application.config.Logger,
	)
	registerEntryRoutes(
		mux,
		installation.Authentication(),
		installation.Competition(),
		installation.Presentation(),
		installation.Voting(),
		installation.Attachments(),
		installation.ProgramControl(),
		installation.Events(),
		installation.RundownQueries(),
		application.config.Logger,
		authenticationLimiter,
	)
	registerResultsFrontendRoutes(
		mux,
		installation.Authentication(),
		installation.Results(),
		application.config.Logger,
	)
	archives := registerBackupRoutes(
		mux,
		installation,
		application.config.DataDir,
		application.config.AttachmentsDir,
		backupConfiguration(application.config.Config),
		application.cancelPreparedRestore,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerInstallationFrontendRoutes(mux, diagnostics, archives)
	registerFinalFilesRoutes(
		mux,
		installation,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerEventRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerAttachmentRoutes(
		mux,
		installation.Authentication(),
		installation.Attachments(),
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerOverrideRoutes(
		mux,
		installation.Authentication(),
		installation.Overrides(),
		application.config.BuildVersion,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerScheduleRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.Schedule(),
		installation.Competition(),
		installation.Presentation(),
		installation.Voting(),
		application.config.ScheduleStream,
		application.scheduleMetrics,
		application.config.Logger,
	)
	registerDisplayRoutes(
		mux,
		installation.Authentication(),
		installation.Displays(),
		application.config.DisplayStream,
		application.config.BuildVersion,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	if err := registerDisplayConnectRoutes(
		mux,
		installation.Displays(),
		application.config.DisplayStream,
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	if err := registerRundownRoutes(
		mux,
		installation.Authentication(),
		installation.RundownCommands(),
		installation.RundownQueries(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	if err := registerCompetitionRoutes(
		mux,
		installation.Authentication(),
		installation.Competition(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	if err := registerResultsRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		installation.Results(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
		application.config.Logger,
	); err != nil {
		return nil, err
	}
	if err := registerProgramControlRoutes(
		mux,
		installation.Authentication(),
		installation.ProgramControl(),
		installation.Displays(),
		application.config.DisplayStream,
		application.config.ProgramStream,
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
		application.config.BuildVersion,
		application.config.Logger,
	); err != nil {
		return nil, err
	}
	if err := registerActivationRoutes(
		mux,
		installation.Authentication(),
		installation.Activation(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	if err := registerScheduleBaselineRoutes(
		mux,
		installation.Authentication(),
		installation.ScheduleBaselineCommands(),
		installation.ScheduleBaselineQueries(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	if err := registerSessionControlRoutes(
		mux,
		installation.Authentication(),
		installation.SessionControl(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	mux.handler = requireCompatibleClientBuild(application.config.BuildVersion, mux.handler)
	return mux, nil
}

func (application *application) probeRoutes() *routeMux {
	mux := newRouteMux()
	mux.HandleFunc("/livez", probeRoute(), liveness)
	mux.HandleFunc("/readyz", probeRoute(), application.readiness)
	return mux
}

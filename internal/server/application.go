package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
)

type applicationConfig struct {
	Config
	Installation    *operations.Installation
	ListenerAddress net.Addr
	DisplayStream   *displaystream.Hub
	ProgramStream   *displaystream.Hub
}

var errRestoreInProgress = errors.New("restore already in progress")

// application keeps probes stable while normal routes are rebuilt after Restore.
type application struct {
	config applicationConfig

	mu              sync.Mutex
	installation    *operations.Installation
	handler         http.Handler
	accepting       bool
	maintenance     bool
	maintenanceKind string
	restoring       bool
	upgrade         *operations.Upgrade
	upgradeApplying bool
	active          int
	nextRequest     uint64
	cancels         map[uint64]context.CancelCauseFunc
	drained         chan struct{}
}

func newUpgradeApplication(
	config applicationConfig,
	upgrade *operations.Upgrade,
) *application {
	return &application{
		config:          config,
		handler:         http.NotFoundHandler(),
		maintenance:     true,
		maintenanceKind: "upgrade",
		upgrade:         upgrade,
		cancels:         make(map[uint64]context.CancelCauseFunc),
		drained:         closedChannel(),
	}
}

type applicationRequestContextKey struct{}

type applicationRequest struct {
	id       uint64
	detached bool
}

func newApplication(config applicationConfig) (*application, error) {
	found := &application{
		config:       config,
		installation: config.Installation,
		accepting:    config.Installation.StartupError() == nil,
		cancels:      make(map[uint64]context.CancelCauseFunc),
		drained:      closedChannel(),
	}
	handler, err := found.buildHandler(config.Installation)
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
	switch request.URL.Path {
	case "/livez":
		liveness(response, request)
		return
	case "/readyz":
		application.readiness(response, request)
		return
	}

	application.mu.Lock()
	if application.maintenance {
		kind := application.maintenanceKind
		upgradeRoute := kind == "upgrade" &&
			(request.URL.Path == "/admin/upgrade" ||
				request.URL.Path == "/admin/upgrade/apply")
		application.mu.Unlock()
		if upgradeRoute {
			application.serveUpgrade(response, request)
			return
		}
		serveMaintenance(response, request, kind)
		return
	}
	handler := application.handler
	if application.active == 0 {
		application.drained = make(chan struct{})
	}
	application.active++
	application.nextRequest++
	tracked := &applicationRequest{id: application.nextRequest}
	requestContext, cancel := context.WithCancelCause(request.Context())
	application.cancels[tracked.id] = cancel
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

func (application *application) restore(ctx context.Context, journalPath string) error {
	installation, err := application.beginRestore(ctx)
	if err != nil {
		return err
	}
	if err = installation.Close(); err != nil {
		application.setUnavailable(installation)
		return err
	}
	_, restoreErr := operations.ApplyRestore(ctx, journalPath)
	reopened, openErr := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir:        application.config.DataDir,
		AttachmentsDir: application.config.AttachmentsDir,
	})
	if openErr != nil {
		application.setUnavailable(nil)
		return errors.Join(restoreErr, openErr)
	}
	handler, buildErr := application.buildHandler(reopened)
	if buildErr != nil {
		application.setUnavailable(reopened)
		return errors.Join(restoreErr, buildErr)
	}

	application.mu.Lock()
	application.installation = reopened
	application.handler = handler
	application.maintenance = false
	application.maintenanceKind = ""
	application.restoring = false
	application.accepting = reopened.StartupError() == nil
	application.mu.Unlock()
	return restoreErr
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
	reopened, err := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir:        application.config.DataDir,
		AttachmentsDir: application.config.AttachmentsDir,
	})
	if err != nil {
		application.mu.Lock()
		application.upgradeApplying = false
		application.mu.Unlock()
		return result, err
	}
	handler, err := application.buildHandler(reopened)
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
	application.mu.Unlock()
	return result, nil
}

func (application *application) Close() error {
	application.mu.Lock()
	application.accepting = false
	application.maintenance = true
	application.maintenanceKind = "shutdown"
	for _, cancel := range application.cancels {
		cancel(errors.New("application closing"))
	}
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
<body><main><h1>Maintenance in progress</h1>
<p role="status">Beamers will return to this page when storage is ready.</p></main>
</body></html>`))
}

func closedChannel() chan struct{} {
	found := make(chan struct{})
	close(found)
	return found
}

func (application *application) buildHandler(
	installation *operations.Installation,
) (http.Handler, error) {
	mux := http.NewServeMux()
	if startupErr := installation.StartupError(); startupErr != nil {
		mux.HandleFunc("GET /diagnostics", func(response http.ResponseWriter, _ *http.Request) {
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
	registerAuthenticationRoutes(
		mux,
		installation.Authentication(),
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerBackupRoutes(
		mux,
		installation,
		application.config.DataDir,
		application.config.AttachmentsDir,
		application.restore,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerEventRoutes(
		mux,
		installation.Authentication(),
		installation.Events(),
		application.config.DisplayStream.Notify,
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
		application.config.DisplayStream.Notify,
		application.config.Logger,
		application.config.ListenerAddress,
	)
	registerScheduleRoutes(mux, installation.Schedule(), application.config.Logger)
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
		application.config.DisplayStream.Notify,
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
		installation.Results(),
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
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
		application.config.DisplayStream.Notify,
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
		application.config.DisplayStream.Notify,
		application.config.ListenerAddress,
		application.config.TracerProvider,
		application.config.MeterProvider,
		application.config.Propagator,
	); err != nil {
		return nil, err
	}
	return requireCompatibleClientBuild(application.config.BuildVersion, mux), nil
}

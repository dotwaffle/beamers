package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
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

// application keeps probes stable while normal routes are rebuilt after Restore.
type application struct {
	config applicationConfig

	mu           sync.Mutex
	installation *operations.Installation
	handler      http.Handler
	accepting    bool
	maintenance  bool
	active       int
	nextRequest  uint64
	cancels      map[uint64]context.CancelCauseFunc
	drained      chan struct{}
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
		application.mu.Unlock()
		response.Header().Set("Retry-After", "1")
		response.Header().Set("X-Beamers-Maintenance", "restore")
		http.Error(response, "maintenance in progress", http.StatusServiceUnavailable)
		return
	}
	handler := application.handler
	tracked := request.URL.Path != "/admin/restores/apply"
	var cancel context.CancelCauseFunc
	var requestID uint64
	if tracked {
		if application.active == 0 {
			application.drained = make(chan struct{})
		}
		application.active++
		application.nextRequest++
		requestID = application.nextRequest
		requestContext, requestCancel := context.WithCancelCause(request.Context())
		cancel = requestCancel
		application.cancels[requestID] = cancel
		request = request.WithContext(requestContext)
	}
	application.mu.Unlock()
	if tracked {
		defer func() {
			cancel(nil)
			application.mu.Lock()
			delete(application.cancels, requestID)
			application.active--
			if application.active == 0 {
				close(application.drained)
			}
			application.mu.Unlock()
		}()
	}
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
	application.accepting = reopened.StartupError() == nil
	application.mu.Unlock()
	return restoreErr
}

func (application *application) beginRestore(
	ctx context.Context,
) (*operations.Installation, error) {
	application.mu.Lock()
	application.maintenance = true
	application.accepting = false
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
		application.maintenance = false
		application.accepting = installation != nil && installation.StartupError() == nil
		application.mu.Unlock()
		return nil, context.Cause(ctx)
	}
}

func (application *application) setUnavailable(installation *operations.Installation) {
	application.mu.Lock()
	application.installation = installation
	application.accepting = false
	application.maintenance = true
	application.mu.Unlock()
}

func (application *application) Close() error {
	application.mu.Lock()
	application.accepting = false
	application.maintenance = true
	for _, cancel := range application.cancels {
		cancel(errors.New("application closing"))
	}
	installation := application.installation
	application.installation = nil
	application.mu.Unlock()
	if installation == nil {
		return nil
	}
	return installation.Close()
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
	if installation.StartupError() != nil {
		// Recovery mode deliberately exposes only the stable outer probes.
		handler := requireCompatibleClientBuild(
			application.config.BuildVersion,
			mux,
		)
		return handler, nil //nolint:nilerr // StartupError selects the restricted handler.
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

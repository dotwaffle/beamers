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
}

// Run serves health endpoints until the context is canceled.
func Run(ctx context.Context, config Config) error {
	if config.BuildVersion == "" {
		return errors.New("server build version is required")
	}
	attachmentsDir := config.AttachmentsDir
	if attachmentsDir == "" {
		attachmentsDir = filepath.Join(config.DataDir, "attachments")
	}
	installation, err := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir: config.DataDir, AttachmentsDir: attachmentsDir,
	})
	if err != nil {
		return err
	}
	startupErr := installation.StartupError()
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
		return errors.Join(err, installation.Close())
	}
	displayStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return errors.Join(err, listener.Close(), installation.Close())
	}
	programStream, err := displaystream.NewProcess(displaySubscriberQueueCapacity)
	if err != nil {
		return errors.Join(err, listener.Close(), installation.Close())
	}

	application, err := newApplication(applicationConfig{
		Config:          config,
		Installation:    installation,
		ListenerAddress: listener.Addr(),
		DisplayStream:   displayStream,
		ProgramStream:   programStream,
	})
	if err != nil {
		return errors.Join(err, listener.Close(), installation.Close())
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
	}
	config.Logger.Info("server listening", "address", listener.Addr().String(), "mode", mode)

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveResult:
		return errors.Join(normalizeServeError(err), application.Close())
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.ShutdownTimeout)
		defer cancel()

		// The ten-second platform profile has no drain phase. Start HTTP and
		// storage closure together so final storage safety keeps the full budget.
		shutdownResults := make(chan error, 2)
		go func() {
			shutdownResults <- httpServer.Shutdown(shutdownContext)
		}()
		go func() {
			shutdownResults <- application.Close()
		}()
		shutdownErr := errors.Join(<-shutdownResults, <-shutdownResults)
		serveErr := normalizeServeError(<-serveResult)
		return errors.Join(shutdownErr, serveErr)
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

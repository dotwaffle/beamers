package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/internal/operations"
)

// Config contains the immutable service configuration.
type Config struct {
	DataDir         string
	ListenAddress   string
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
	TracerProvider  trace.TracerProvider
	MeterProvider   metric.MeterProvider
	Propagator      propagation.TextMapPropagator
}

// Run serves health endpoints until the context is canceled.
func Run(ctx context.Context, config Config) error {
	installation, err := operations.OpenInstallation(ctx, config.DataDir)
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

	var accepting atomic.Bool
	accepting.Store(startupErr == nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", liveness)
	mux.HandleFunc("/readyz", readiness(&accepting, installation, config.Logger))
	if startupErr == nil {
		registerAuthenticationRoutes(
			mux,
			installation.Authentication(),
			config.Logger,
			listener.Addr(),
		)
		registerEventRoutes(
			mux,
			installation.Authentication(),
			installation.Events(),
			config.Logger,
			listener.Addr(),
		)
		registerScheduleRoutes(mux, installation.Schedule(), config.Logger)
		registerDisplayRoutes(
			mux, installation.Authentication(), installation.Displays(), config.Logger, listener.Addr(),
		)
		if err := registerRundownRoutes(
			mux,
			installation.Authentication(),
			installation.RundownCommands(),
			installation.RundownQueries(),
			listener.Addr(),
			config.TracerProvider,
			config.MeterProvider,
			config.Propagator,
		); err != nil {
			return errors.Join(err, listener.Close(), installation.Close())
		}
		if err := registerActivationRoutes(
			mux,
			installation.Authentication(),
			installation.Activation(),
			listener.Addr(),
			config.TracerProvider,
			config.MeterProvider,
			config.Propagator,
		); err != nil {
			return errors.Join(err, listener.Close(), installation.Close())
		}
		if err := registerSessionControlRoutes(
			mux,
			installation.Authentication(),
			installation.SessionControl(),
			listener.Addr(),
			config.TracerProvider,
			config.MeterProvider,
			config.Propagator,
		); err != nil {
			return errors.Join(err, listener.Close(), installation.Close())
		}
	}

	httpServer := &http.Server{
		Handler:           mux,
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
		return errors.Join(normalizeServeError(err), installation.Close())
	case <-ctx.Done():
		accepting.Store(false)
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.ShutdownTimeout)
		defer cancel()

		// The ten-second platform profile has no drain phase. Start HTTP and
		// storage closure together so final storage safety keeps the full budget.
		shutdownResults := make(chan error, 2)
		go func() {
			shutdownResults <- httpServer.Shutdown(shutdownContext)
		}()
		go func() {
			shutdownResults <- installation.Close()
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

func readiness(
	accepting *atomic.Bool,
	installation *operations.Installation,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if !probeMethodAllowed(response, request) {
			return
		}
		setProbeHeaders(response)
		if !accepting.Load() {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		probeContext, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := installation.Ready(probeContext); err != nil {
			logger.LogAttrs(
				request.Context(),
				slog.LevelError,
				"readiness storage probe failed",
				slog.String("component", "storage"),
				slog.Any("error", err),
			)
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	}
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

package server

import (
	"errors"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/rundownconnect"
)

const maxRundownRPCBodyBytes = 1 << 20

func registerRundownRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	commands *rundown.Commands,
	queries *rundown.Queries,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	if tracerProvider == nil || meterProvider == nil || propagator == nil {
		return errors.New("rundown telemetry providers and propagator are required")
	}
	adapter, err := rundownconnect.NewHandler(commands, queries)
	if err != nil {
		return err
	}
	authenticationInterceptor, err := rundownconnect.AuthenticationInterceptor(authentication)
	if err != nil {
		return err
	}
	telemetryInterceptor, err := otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(tracerProvider),
		otelconnect.WithMeterProvider(meterProvider),
		otelconnect.WithPropagator(propagator),
		otelconnect.WithoutServerPeerAttributes(),
		otelconnect.WithoutTraceEvents(),
	)
	if err != nil {
		return err
	}
	path, handler := rundownv1connect.NewRundownServiceHandler(
		adapter,
		connect.WithInterceptors(telemetryInterceptor, authenticationInterceptor),
	)
	allowPlaintextCrew := listenerIsLoopback(listenerAddress)
	mux.Handle(path, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(response, request, http.MethodPost, allowPlaintextCrew) {
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, maxRundownRPCBodyBytes)
		handler.ServeHTTP(response, request)
	}))
	return nil
}

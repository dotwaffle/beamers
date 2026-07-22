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

	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/sessionconnect"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
)

const maxSessionControlRPCBodyBytes = 64 << 10

func registerSessionControlRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *sessioncontrol.Service,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	if tracerProvider == nil || meterProvider == nil || propagator == nil {
		return errors.New("session control telemetry providers and propagator are required")
	}
	adapter, err := sessionconnect.NewHandler(service)
	if err != nil {
		return err
	}
	authenticationInterceptor, err := connectapi.AuthenticationInterceptor(authentication)
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
	path, handler := sessionv1connect.NewSessionControlServiceHandler(
		adapter,
		connect.WithInterceptors(
			telemetryInterceptor,
			connectapi.RequestIDInterceptor(),
			sessionconnect.ErrorInterceptor(),
			authenticationInterceptor,
			sessionconnect.ValidationInterceptor(),
		),
	)
	allowPlaintextCrew := listenerIsLoopback(listenerAddress)
	mux.Handle(path, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(response, request, http.MethodPost, allowPlaintextCrew) {
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, maxSessionControlRPCBodyBytes)
		handler.ServeHTTP(response, request)
	}))
	return nil
}

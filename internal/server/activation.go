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

	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/activationconnect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

const maxActivationRPCBodyBytes = 64 << 10

func registerActivationRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *activation.Service,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	if tracerProvider == nil || meterProvider == nil || propagator == nil {
		return errors.New("activation telemetry providers and propagator are required")
	}
	adapter, err := activationconnect.NewHandler(service)
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
	path, handler := activationv1connect.NewActivationServiceHandler(
		adapter,
		connect.WithInterceptors(
			telemetryInterceptor,
			connectapi.RequestIDInterceptor(),
			activationconnect.ErrorInterceptor(),
			authenticationInterceptor,
			activationconnect.ValidationInterceptor(),
		),
	)
	allowPlaintextCrew := listenerIsLoopback(listenerAddress)
	mux.Handle(path, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(response, request, http.MethodPost, allowPlaintextCrew) {
			return
		}
		request.Body = http.MaxBytesReader(response, request.Body, maxActivationRPCBodyBytes)
		handler.ServeHTTP(response, request)
	}))
	return nil
}

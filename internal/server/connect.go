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

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

type connectRouteConfig struct {
	name                  string
	authentication        *auth.Service
	authenticationLayer   connect.Interceptor
	listenerAddress       net.Addr
	tracerProvider        trace.TracerProvider
	meterProvider         metric.MeterProvider
	propagator            propagation.TextMapPropagator
	errorInterceptor      connect.Interceptor
	validationInterceptor connect.Interceptor
	maxBodyBytes          int64
	contract              routeContract
	build                 func(...connect.HandlerOption) (string, http.Handler)
}

func registerConnectRoute(mux *routeMux, config connectRouteConfig) error {
	if mux == nil || config.build == nil {
		return errors.New(config.name + " Connect registration is incomplete")
	}
	if config.tracerProvider == nil || config.meterProvider == nil || config.propagator == nil {
		return errors.New(config.name + " telemetry providers and propagator are required")
	}
	authenticationInterceptor := config.authenticationLayer
	if authenticationInterceptor == nil {
		var err error
		authenticationInterceptor, err = connectapi.AuthenticationInterceptor(config.authentication)
		if err != nil {
			return err
		}
	}
	telemetryInterceptor, err := otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(config.tracerProvider),
		otelconnect.WithMeterProvider(config.meterProvider),
		otelconnect.WithPropagator(config.propagator),
		otelconnect.WithoutServerPeerAttributes(),
		otelconnect.WithoutTraceEvents(),
	)
	if err != nil {
		return err
	}
	path, handler := config.build(connect.WithInterceptors(
		telemetryInterceptor,
		connectapi.RequestIDInterceptor(),
		config.errorInterceptor,
		authenticationInterceptor,
		config.validationInterceptor,
	))
	allowPlaintextCrew := listenerIsLoopback(config.listenerAddress)
	contract := config.contract
	contract.maxBodyBytes = config.maxBodyBytes
	mux.Handle(path, contract, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(response, request, http.MethodPost, allowPlaintextCrew) {
			return
		}
		handler.ServeHTTP(response, request)
	}))
	return nil
}

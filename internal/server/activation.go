package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/activationconnect"
	"github.com/dotwaffle/beamers/internal/auth"
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
	adapter, err := activationconnect.NewHandler(service)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "activation", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor:      activationconnect.ErrorInterceptor(),
		validationInterceptor: activationconnect.ValidationInterceptor(),
		maxBodyBytes:          maxActivationRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return activationv1connect.NewActivationServiceHandler(adapter, options...)
		},
	})
}

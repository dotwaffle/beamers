package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
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
	adapter, err := sessionconnect.NewHandler(service)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "session control", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor:      sessionconnect.ErrorInterceptor(),
		validationInterceptor: sessionconnect.ValidationInterceptor(),
		maxBodyBytes:          maxSessionControlRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return sessionv1connect.NewSessionControlServiceHandler(adapter, options...)
		},
	})
}

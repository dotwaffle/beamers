package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/display/v1/displayv1connect"
	"github.com/dotwaffle/beamers/internal/displayconnect"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

const maxDisplayRPCBodyBytes = 64 << 10

func registerDisplayConnectRoutes(
	mux *http.ServeMux,
	service *displays.Service,
	stream *displaystream.Hub,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	authentication, err := displayconnect.AuthenticationInterceptor(service, stream, displayCookieName)
	if err != nil {
		return err
	}
	adapter, err := displayconnect.NewHandler(service, stream)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "display", authenticationLayer: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		maxBodyBytes: maxDisplayRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return displayv1connect.NewDisplayServiceHandler(adapter, options...)
		},
	})
}

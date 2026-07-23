package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
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
	notifyDisplays func(),
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	adapter, err := rundownconnect.NewHandler(commands, queries, notifyDisplays)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "rundown", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor:      rundownconnect.ErrorInterceptor(),
		validationInterceptor: rundownconnect.ValidationInterceptor(),
		maxBodyBytes:          maxRundownRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return rundownv1connect.NewRundownServiceHandler(adapter, options...)
		},
	})
}

package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/competitionconnect"
)

const maxCompetitionRPCBodyBytes = 128 << 10

func registerCompetitionRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *competition.Service,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	adapter, err := competitionconnect.NewHandler(service)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "competition", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor: competitionconnect.ErrorInterceptor(),
		maxBodyBytes:     maxCompetitionRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return competitionv1connect.NewCompetitionServiceHandler(adapter, options...)
		},
	})
}

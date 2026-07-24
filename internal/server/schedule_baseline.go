package server

import (
	"net"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/schedulebaseline/v1/schedulebaselinev1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
	"github.com/dotwaffle/beamers/internal/schedulebaselineconnect"
)

const maxScheduleBaselineRPCBodyBytes = 256 << 10

func registerScheduleBaselineRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	commands *schedulebaseline.Commands,
	queries *schedulebaseline.Queries,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
) error {
	adapter, err := schedulebaselineconnect.NewHandler(commands, queries)
	if err != nil {
		return err
	}
	return registerConnectRoute(mux, connectRouteConfig{
		name: "Public Schedule Baseline", authentication: authentication,
		listenerAddress: listenerAddress, tracerProvider: tracerProvider,
		meterProvider: meterProvider, propagator: propagator,
		errorInterceptor:      schedulebaselineconnect.ErrorInterceptor(),
		validationInterceptor: schedulebaselineconnect.ValidationInterceptor(),
		maxBodyBytes:          maxScheduleBaselineRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return schedulebaselinev1connect.NewScheduleBaselineServiceHandler(adapter, options...)
		},
	})
}

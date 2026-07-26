package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/resultsconnect"
)

const maxResultsRPCBodyBytes = 128 << 10

func registerResultsRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *results.Service,
	listenerAddress net.Addr,
	tracerProvider trace.TracerProvider,
	meterProvider metric.MeterProvider,
	propagator propagation.TextMapPropagator,
	logger *slog.Logger,
) error {
	adapter, err := resultsconnect.NewHandler(service)
	if err != nil {
		return err
	}
	if err := registerConnectRoute(mux, connectRouteConfig{
		name: "results", authentication: authentication, listenerAddress: listenerAddress,
		tracerProvider: tracerProvider, meterProvider: meterProvider, propagator: propagator,
		errorInterceptor: resultsconnect.ErrorInterceptor(),
		maxBodyBytes:     maxResultsRPCBodyBytes,
		build: func(options ...connect.HandlerOption) (string, http.Handler) {
			return resultsv1connect.NewResultsServiceHandler(adapter, options...)
		},
	}); err != nil {
		return err
	}
	registerPublicResultsRoutes(mux, service, logger)
	return nil
}

type publicResultsHandlers struct {
	service publicResultsReader
	logger  *slog.Logger
}

type publicResultsReader interface {
	PublicArtifact(
		context.Context,
		int,
		results.PublicationScope,
		int,
		int,
	) (results.PublicArtifact, bool, error)
}

func registerPublicResultsRoutes(
	mux *http.ServeMux,
	service publicResultsReader,
	logger *slog.Logger,
) {
	handlers := publicResultsHandlers{service: service, logger: logger}
	mux.HandleFunc(
		"/results/events/{eventID}/{scope}/{sessionID}",
		handlers.latestHTML,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/{scope}/{sessionID}/results.txt",
		handlers.latestText,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/{scope}/{sessionID}/revisions/{revision}/results.json",
		handlers.versionedJSON,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/event-awards",
		handlers.latestEventAwardsHTML,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/event-awards/results.txt",
		handlers.latestEventAwardsText,
	)
	mux.HandleFunc(
		"/results/events/{eventID}/event-awards/revisions/{revision}/results.json",
		handlers.versionedEventAwardsJSON,
	)
}

func (handlers publicResultsHandlers) latestHTML(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serve(response, request, 0, "text/html; charset=utf-8")
}

func (handlers publicResultsHandlers) latestText(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serve(response, request, 0, "text/plain; charset=utf-8")
}

func (handlers publicResultsHandlers) versionedJSON(
	response http.ResponseWriter,
	request *http.Request,
) {
	revision, err := strconv.Atoi(request.PathValue("revision"))
	if err != nil || revision <= 0 {
		http.NotFound(response, request)
		return
	}
	handlers.serve(response, request, revision, "application/json")
}

func (handlers publicResultsHandlers) latestEventAwardsHTML(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serveEventAwards(response, request, 0, "text/html; charset=utf-8")
}

func (handlers publicResultsHandlers) latestEventAwardsText(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.serveEventAwards(response, request, 0, "text/plain; charset=utf-8")
}

func (handlers publicResultsHandlers) versionedEventAwardsJSON(
	response http.ResponseWriter,
	request *http.Request,
) {
	revision, err := strconv.Atoi(request.PathValue("revision"))
	if err != nil || revision <= 0 {
		http.NotFound(response, request)
		return
	}
	handlers.serveEventAwards(response, request, revision, "application/json")
}

func (handlers publicResultsHandlers) serveEventAwards(
	response http.ResponseWriter,
	request *http.Request,
	revision int,
	contentType string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	handlers.serveArtifact(
		response,
		request,
		eventID,
		results.PublicationScopeEventAwards,
		eventID,
		revision,
		contentType,
	)
}

func (handlers publicResultsHandlers) serve(
	response http.ResponseWriter,
	request *http.Request,
	revision int,
	contentType string,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	sessionID, sessionErr := positivePathID(request, "sessionID")
	scope := results.PublicationScope(strings.ToLower(request.PathValue("scope")))
	scope = map[results.PublicationScope]results.PublicationScope{
		"prizegiving": results.PublicationScopePrizegiving,
		"standalone":  results.PublicationScopeStandalone,
	}[scope]
	if eventErr != nil || sessionErr != nil || scope == "" {
		http.NotFound(response, request)
		return
	}
	handlers.serveArtifact(
		response,
		request,
		eventID,
		scope,
		sessionID,
		revision,
		contentType,
	)
}

func (handlers publicResultsHandlers) serveArtifact(
	response http.ResponseWriter,
	request *http.Request,
	eventID int,
	scope results.PublicationScope,
	sessionID int,
	revision int,
	contentType string,
) {
	artifact, found, err := handlers.service.PublicArtifact(
		request.Context(),
		eventID,
		scope,
		sessionID,
		revision,
	)
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Results", "error", err)
		http.Error(response, "Results unavailable", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(response, request)
		return
	}
	etag := fmt.Sprintf(`"results-%d-%s-%d-%d"`, eventID, scope, sessionID, artifact.Revision)
	response.Header().Set("Cache-Control", "public, max-age=15, must-revalidate")
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("ETag", etag)
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	if request.Method == http.MethodHead {
		return
	}
	var content string
	switch contentType {
	case "text/html; charset=utf-8":
		content = artifact.HTML
	case "text/plain; charset=utf-8":
		content = artifact.Text
	default:
		content = artifact.JSON
	}
	if _, err = response.Write([]byte(content)); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write public Results", "error", err)
	}
}

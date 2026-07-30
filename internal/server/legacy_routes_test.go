package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyBrowserRoutesAreNotRegistered(t *testing.T) {
	routes := newRouteMux()
	logger := slog.New(slog.DiscardHandler)
	registerScheduleRoutes(
		routes,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		logger,
	)
	registerEntryRoutes(
		routes,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		logger,
		nil,
	)
	registerPublicResultsRoutes(routes, &publicResultsReaderStub{}, logger)

	for _, path := range []string{
		"/schedule",
		"/schedule/sessions/7",
		"/submissions",
		"/results/events/7",
		"/results/events/7/standalone/3",
		"/results/events/7/event-awards",
	} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			path,
			http.NoBody,
		)
		if contract, ok := routes.contract(request); ok {
			t.Errorf("%s retained browser contract %+v", path, contract)
		}
	}
}

func TestCanonicalBrowserAndMachineRoutesRemainRegistered(t *testing.T) {
	routes := newRouteMux()
	logger := slog.New(slog.DiscardHandler)
	registerScheduleRoutes(
		routes,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		logger,
	)
	registerEntryRoutes(
		routes,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		logger,
		nil,
	)
	registerPublicResultsRoutes(routes, &publicResultsReaderStub{}, logger)

	for path, want := range map[string]interfaceKind{
		"/events/revision/schedule":                               publicInterface,
		"/events/revision/schedule/sessions/7":                    publicInterface,
		"/my-schedule":                                            publicInterface,
		"/my-participation":                                       publicInterface,
		"/schedule/events":                                        publicInterface,
		"/results/events/7/results.txt":                           publicInterface,
		"/results/events/7/revisions/3/results.json":              publicInterface,
		"/results/events/7/standalone/3/results.txt":              publicInterface,
		"/results/events/7/standalone/3/revisions/2/results.json": publicInterface,
		"/results/events/7/event-awards/results.txt":              publicInterface,
		"/results/events/7/event-awards/revisions/2/results.json": publicInterface,
	} {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			path,
			http.NoBody,
		)
		contract, ok := routes.contract(request)
		if !ok || contract.kind != want {
			t.Errorf("%s contract = %+v, %t; want kind %d", path, contract, ok, want)
		}
	}
}

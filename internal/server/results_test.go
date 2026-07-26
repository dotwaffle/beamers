package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dotwaffle/beamers/internal/results"
)

type publicResultsRead struct {
	eventID, scopeSessionID, revision int
	scope                             results.PublicationScope
}

type publicResultsReaderStub struct {
	reads []publicResultsRead
}

func (stub *publicResultsReaderStub) PublicArtifact(
	_ context.Context,
	eventID int,
	scope results.PublicationScope,
	scopeSessionID int,
	revision int,
) (results.PublicArtifact, bool, error) {
	stub.reads = append(stub.reads, publicResultsRead{
		eventID: eventID, scope: scope, scopeSessionID: scopeSessionID,
		revision: revision,
	})
	return results.PublicArtifact{
		Revision: 7, HTML: "<p>Awards</p>", Text: "Awards", JSON: `{"awards":true}`,
	}, true, nil
}

func TestPublicEventAwardsRoutesServeAllFormats(t *testing.T) {
	stub := &publicResultsReaderStub{}
	mux := newRouteMux()
	registerPublicResultsRoutes(
		mux,
		stub,
		slog.New(slog.DiscardHandler),
	)
	tests := []struct {
		path, contentType, body string
		revision                int
	}{
		{
			path:        "/results/events/41/event-awards",
			contentType: "text/html; charset=utf-8", body: "<p>Awards</p>",
		},
		{
			path:        "/results/events/41/event-awards/results.txt",
			contentType: "text/plain; charset=utf-8", body: "Awards",
		},
		{
			path:        "/results/events/41/event-awards/revisions/7/results.json",
			contentType: "application/json", body: `{"awards":true}`, revision: 7,
		},
	}
	for _, test := range tests {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			test.path,
			http.NoBody,
		)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK ||
			response.Header().Get("Content-Type") != test.contentType ||
			response.Body.String() != test.body {
			t.Fatalf(
				"GET %s = %d, %q, %q",
				test.path,
				response.Code,
				response.Header().Get("Content-Type"),
				response.Body.String(),
			)
		}
		read := stub.reads[len(stub.reads)-1]
		if read.eventID != 41 ||
			read.scope != results.PublicationScopeEventAwards ||
			read.scopeSessionID != 41 ||
			read.revision != test.revision {
			t.Fatalf("GET %s read = %+v", test.path, read)
		}
	}
}

func TestPublicEventResultsRoutesServeAllFormats(t *testing.T) {
	stub := &publicResultsReaderStub{}
	mux := newRouteMux()
	registerPublicResultsRoutes(mux, stub, slog.New(slog.DiscardHandler))
	tests := []struct {
		path, contentType, body string
		revision                int
	}{
		{
			path:        "/results/events/41",
			contentType: "text/html; charset=utf-8", body: "<p>Awards</p>",
		},
		{
			path:        "/results/events/41/results.txt",
			contentType: "text/plain; charset=utf-8", body: "Awards",
		},
		{
			path:        "/results/events/41/revisions/7/results.json",
			contentType: "application/json", body: `{"awards":true}`, revision: 7,
		},
	}
	for _, test := range tests {
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			test.path,
			http.NoBody,
		)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusOK ||
			response.Header().Get("Content-Type") != test.contentType ||
			response.Body.String() != test.body {
			t.Fatalf(
				"GET %s = %d, %q, %q",
				test.path,
				response.Code,
				response.Header().Get("Content-Type"),
				response.Body.String(),
			)
		}
		read := stub.reads[len(stub.reads)-1]
		if read.eventID != 41 ||
			read.scope != results.PublicationScopeEvent ||
			read.scopeSessionID != 41 ||
			read.revision != test.revision {
			t.Fatalf("GET %s read = %+v", test.path, read)
		}
	}
}

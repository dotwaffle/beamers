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
		Revision: 7,
		HTML:     "<html><head></head><body><main><p>Awards</p></main></body></html>",
		Text:     "Awards",
		JSON:     `{"awards":true}`,
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
			contentType: "text/html; charset=utf-8",
			body: `<html><head><link rel="stylesheet" href="/assets/events/41/theme.css"></head>` +
				`<body data-reduced-effects="false"><main><p>Awards</p>` +
				`<p><a href="/effects?return_to=%2Fresults%2Fevents%2F41%2Fevent-awards">` +
				`Pause effects</a></p></main></body></html>`,
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
		if test.contentType == "text/html; charset=utf-8" &&
			response.Header().Get("Vary") != "Cookie" {
			t.Errorf("GET %s Vary = %q, want Cookie", test.path, response.Header().Get("Vary"))
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
			contentType: "text/html; charset=utf-8",
			body: `<html><head><link rel="stylesheet" href="/assets/events/41/theme.css"></head>` +
				`<body data-reduced-effects="false"><main><p>Awards</p>` +
				`<p><a href="/effects?return_to=%2Fresults%2Fevents%2F41">` +
				`Pause effects</a></p></main></body></html>`,
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
		if test.contentType == "text/html; charset=utf-8" &&
			response.Header().Get("Vary") != "Cookie" {
			t.Errorf("GET %s Vary = %q, want Cookie", test.path, response.Header().Get("Vary"))
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

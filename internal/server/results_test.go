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
	reads        []publicResultsRead
	artifactHTML string
}

func TestPublicResultsHTMLServesStoredArtifactForEveryVisitor(t *testing.T) {
	t.Parallel()

	const stored = `<!doctype html><html><body data-reduced-effects="false">` +
		`<main id="main-content" tabindex="-1"><h1>Results</h1></main></body></html>`
	for name, cookie := range map[string]*http.Cookie{
		"anonymous": nil,
		"signed in": {
			Name: sessionCookieName, Value: "signed-in-session",
		},
		"reduced effects cookie": {
			Name: reducedEffectsCookieName, Value: "true",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"/events/revision/results",
				http.NoBody,
			)
			if cookie != nil {
				request.AddCookie(cookie)
			}
			response := httptest.NewRecorder()
			handlers := publicResultsHandlers{
				service: &publicResultsReaderStub{artifactHTML: stored},
				logger:  slog.New(slog.DiscardHandler),
			}
			handlers.serveArtifact(
				response,
				request,
				41,
				results.PublicationScopeEvent,
				41,
				0,
				"text/html; charset=utf-8",
			)
			if response.Code != http.StatusOK || response.Body.String() != stored {
				t.Fatalf(
					"HTML = %d %q, want stored %q",
					response.Code,
					response.Body.String(),
					stored,
				)
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=15, must-revalidate" {
				t.Errorf("Cache-Control = %q", got)
			}
			if got := response.Header().Get("ETag"); got != `"results-41-Event-41-7"` {
				t.Errorf("ETag = %q", got)
			}
			if got := response.Header().Get("Vary"); got != "" {
				t.Errorf("Vary = %q", got)
			}
		})
	}
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
	artifactHTML := stub.artifactHTML
	if artifactHTML == "" {
		artifactHTML = "<html><head></head><body><main><p>Awards</p></main></body></html>"
	}
	return results.PublicArtifact{
		Revision: 7,
		HTML:     artifactHTML,
		Text:     "Awards",
		JSON:     `{"awards":true}`,
	}, true, nil
}

func TestPublicEventAwardsRoutesServeMachineFormats(t *testing.T) {
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

func TestPublicEventResultsRoutesServeMachineFormats(t *testing.T) {
	stub := &publicResultsReaderStub{}
	mux := newRouteMux()
	registerPublicResultsRoutes(mux, stub, slog.New(slog.DiscardHandler))
	tests := []struct {
		path, contentType, body string
		revision                int
	}{
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

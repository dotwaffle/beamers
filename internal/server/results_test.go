package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/results"
)

type publicResultsRead struct {
	eventID, scopeSessionID, revision int
	scope                             results.PublicationScope
}

type publicResultsReaderStub struct {
	reads []publicResultsRead
}

func TestWrappedPublicResultsKeepsSharedSkipTarget(t *testing.T) {
	t.Parallel()

	for name, main := range map[string]string{
		"current": `<main id="main-content" tabindex="-1">`,
		"legacy":  "<main>",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := `<!doctype html><html><body>` + main +
				`<h1>Results</h1></main></body></html>`
			wrapped, err := wrapPublicResultsHTML(t.Context(), document, frontend.Shell{
				Event: frontend.EventContext{Name: "Revision", Slug: "revision"},
			})
			if err != nil {
				t.Fatalf("wrap public Results: %v", err)
			}
			for _, want := range []string{
				`class="skip-link" href="#main-content"`,
				`<main id="main-content" tabindex="-1">`,
			} {
				if !strings.Contains(wrapped, want) {
					t.Errorf("wrapped public Results missing %q: %s", want, wrapped)
				}
			}
			if strings.Contains(wrapped, "<main>") {
				t.Errorf("wrapped public Results retained legacy main: %s", wrapped)
			}
		})
	}
}

func TestPublicResultsETagIncludesEventShell(t *testing.T) {
	first := publicResultsETag(
		41,
		results.PublicationScopeEvent,
		41,
		7,
		&frontend.Shell{Event: frontend.EventContext{Name: "First Event"}},
	)
	second := publicResultsETag(
		41,
		results.PublicationScopeEvent,
		41,
		7,
		&frontend.Shell{Event: frontend.EventContext{Name: "Renamed Event"}},
	)
	if first == second {
		t.Fatalf("Event rename retained public Results ETag %q", first)
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
	return results.PublicArtifact{
		Revision: 7,
		HTML:     "<html><head></head><body><main><p>Awards</p></main></body></html>",
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

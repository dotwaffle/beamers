package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrewBrowserMutationWithoutBuildRequiresReload(t *testing.T) {
	called := false
	handler := requireCompatibleClientBuild("current-build", http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			called = true
		},
	))
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"/admin/events",
		strings.NewReader("{}"),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if called {
		t.Error("obsolete crew browser mutation reached its handler")
	}
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"reload_required"`) ||
		response.Header().Get(clientBuildHeader) != "current-build" {
		t.Errorf(
			"obsolete crew browser mutation = %d %q build %q",
			response.Code,
			response.Body.String(),
			response.Header().Get(clientBuildHeader),
		)
	}
}

func TestProgrammaticAndCurrentCrewClientsRemainCompatible(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		build        string
		secFetchSite string
	}{
		{name: "programmatic client"},
		{name: "current browser", build: "current-build", secFetchSite: "same-origin"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			called := false
			handler := requireCompatibleClientBuild("current-build", http.HandlerFunc(
				func(response http.ResponseWriter, _ *http.Request) {
					called = true
					response.WriteHeader(http.StatusNoContent)
				},
			))
			request := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				"/admin/events",
				strings.NewReader("{}"),
			)
			request.Header.Set("Content-Type", "application/json")
			if testCase.build != "" {
				request.Header.Set(clientBuildHeader, testCase.build)
			}
			if testCase.secFetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", testCase.secFetchSite)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if !called || response.Code != http.StatusNoContent {
				t.Errorf("compatible crew mutation = called %t, status %d", called, response.Code)
			}
		})
	}
}

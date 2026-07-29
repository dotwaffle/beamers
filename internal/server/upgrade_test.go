package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpgradeMaintenanceRejectsUnregisteredRoutes(t *testing.T) {
	application := newUpgradeApplication(applicationConfig{}, nil)
	for _, path := range []string{
		"/admin/events?event=1",
		"/beamers.display.v1.DisplayService/GetSnapshot",
		"/unknown",
	} {
		response := httptest.NewRecorder()
		application.ServeHTTP(
			response,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody),
		)
		if response.Code != http.StatusNotFound ||
			response.Header().Get("X-Beamers-Maintenance") != "" {
			t.Errorf(
				"GET %s = %d, headers %v; want unclassified 404",
				path,
				response.Code,
				response.Header(),
			)
		}
	}
}

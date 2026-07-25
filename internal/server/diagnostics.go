package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/replication"
	"github.com/dotwaffle/beamers/internal/telemetry"
)

const (
	testedLocationsOrLanes = 64
	testedDisplays         = 250
	testedSessionsEntries  = 5_000
)

func registerDiagnosticsRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	installation *operations.Installation,
	displayService *displays.Service,
	displayStream *displaystream.Hub,
	programStream *displaystream.Hub,
	telemetryRuntime *telemetry.Runtime,
	replicationAdapter *replication.Adapter,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	serve := func(response http.ResponseWriter, request *http.Request, actor auth.Account) {
		statuses, statusErr := displayService.List(request.Context(), actor, displayStream.Cursor())
		if statusErr != nil {
			logger.ErrorContext(
				request.Context(),
				"build diagnostics",
				"component", "diagnostics",
				"error", statusErr,
			)
		}
		capacity, capacityErr := installation.Capacity(request.Context())
		if capacityErr != nil {
			logger.ErrorContext(
				request.Context(),
				"build capacity diagnostics",
				"component", "diagnostics",
				"error", capacityErr,
			)
		}
		found := normalDiagnostics(
			statuses,
			statusErr,
			capacity,
			capacityErr,
			displayStream.SubscriberCount(),
			programStream.SubscriberCount(),
			telemetryRuntime != nil && telemetryRuntime.Enabled(),
			replicationAdapter,
		)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(found); err != nil {
			logger.ErrorContext(
				request.Context(),
				"write diagnostics",
				"component", "diagnostics",
				"error", err,
			)
		}
	}
	if listenerIsLoopback(listenerAddress) {
		mux.HandleFunc("GET /diagnostics", func(response http.ResponseWriter, request *http.Request) {
			serve(response, request, auth.Account{Administrator: true})
		})
	}
	mux.HandleFunc("GET /admin/diagnostics", func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(
			response,
			request,
			http.MethodGet,
			listenerIsLoopback(listenerAddress),
		) {
			return
		}
		cookie, err := request.Cookie(sessionCookieName)
		if err != nil {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		actor, err := authentication.Authenticate(request.Context(), cookie.Value)
		if err != nil {
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		if !actor.Administrator {
			http.Error(response, "Administrator authority required", http.StatusForbidden)
			return
		}
		serve(response, request, actor)
	})
}

type componentDiagnostics struct {
	Status string `json:"status"`
}

type streamDiagnostics struct {
	Status      string `json:"status"`
	Subscribers int    `json:"subscribers"`
}

type normalDiagnosticsResponse struct {
	Mode        string               `json:"mode"`
	Storage     componentDiagnostics `json:"storage"`
	Backup      componentDiagnostics `json:"backup"`
	Replication replication.Status   `json:"replication"`
	Streams     struct {
		Display streamDiagnostics `json:"display"`
		Program streamDiagnostics `json:"program"`
	} `json:"streams"`
	Displays struct {
		Status   string         `json:"status"`
		Total    int            `json:"total"`
		Delivery map[string]int `json:"delivery"`
	} `json:"displays"`
	Capacity  capacityDiagnostics  `json:"capacity"`
	Telemetry componentDiagnostics `json:"telemetry"`
}

type capacityWarning struct {
	Code      string `json:"code"`
	Observed  int    `json:"observed"`
	TestedMax int    `json:"tested_max"`
}

type capacityDiagnostics struct {
	Status   string              `json:"status"`
	Counts   operations.Capacity `json:"counts"`
	Warnings []capacityWarning   `json:"warnings,omitempty"`
}

func normalDiagnostics(
	statuses []displays.Status,
	statusErr error,
	capacity operations.Capacity,
	capacityErr error,
	displaySubscribers int,
	programSubscribers int,
	telemetryEnabled bool,
	replicationAdapter *replication.Adapter,
) normalDiagnosticsResponse {
	found := normalDiagnosticsResponse{
		Mode:        "normal",
		Storage:     componentDiagnostics{Status: "ready"},
		Backup:      componentDiagnostics{Status: "available"},
		Replication: replication.Status{Status: "disabled"},
		Telemetry:   componentDiagnostics{Status: "disabled"},
		Capacity:    capacityDiagnostics{Status: "within_tested_envelope", Counts: capacity},
	}
	found.Streams.Display = streamDiagnostics{
		Status: "ready", Subscribers: displaySubscribers,
	}
	found.Streams.Program = streamDiagnostics{
		Status: "ready", Subscribers: programSubscribers,
	}
	found.Displays.Status = "ready"
	found.Displays.Delivery = map[string]int{
		"applied": 0, "excessively_skewed": 0, "lagging": 0,
		"offline": 0, "unstable": 0, "unknown": 0,
	}
	if telemetryEnabled {
		found.Telemetry.Status = "enabled"
	}
	if replicationAdapter != nil {
		found.Replication = replicationAdapter.Status()
	}
	if capacityErr != nil {
		found.Capacity.Status = "unavailable"
	} else {
		found.Capacity.Warnings = capacityWarnings(capacity, displaySubscribers)
		if len(found.Capacity.Warnings) > 0 {
			found.Capacity.Status = "warning"
		}
	}
	if statusErr != nil {
		found.Storage.Status = "unavailable"
		found.Backup.Status = "unavailable"
		found.Displays.Status = "unavailable"
		return found
	}
	found.Displays.Total = len(statuses)
	for _, status := range statuses {
		if _, known := found.Displays.Delivery[status.DeliveryState]; !known {
			found.Displays.Delivery["unknown"]++
			continue
		}
		found.Displays.Delivery[status.DeliveryState]++
	}
	return found
}

func capacityWarnings(capacity operations.Capacity, displaySubscribers int) []capacityWarning {
	var warnings []capacityWarning
	if observed := max(capacity.Locations, capacity.Lanes); observed > testedLocationsOrLanes {
		warnings = append(warnings, capacityWarning{
			Code: "lanes_or_locations", Observed: observed, TestedMax: testedLocationsOrLanes,
		})
	}
	if observed := capacity.Sessions + capacity.Entries; observed > testedSessionsEntries {
		warnings = append(warnings, capacityWarning{
			Code: "sessions_and_entries", Observed: observed, TestedMax: testedSessionsEntries,
		})
	}
	if observed := max(capacity.Displays, displaySubscribers); observed > testedDisplays {
		warnings = append(warnings, capacityWarning{
			Code: "displays", Observed: observed, TestedMax: testedDisplays,
		})
	}
	return warnings
}

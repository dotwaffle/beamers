package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
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

type diagnosticHandlers struct {
	authentication     *auth.Service
	installation       *operations.Installation
	displayService     *displays.Service
	displayStream      *displaystream.Hub
	programStream      *displaystream.Hub
	scheduleStream     *displaystream.Hub
	votingStream       *displaystream.Hub
	scheduleMetrics    *scheduleStreamMetrics
	telemetryRuntime   *telemetry.Runtime
	replicationAdapter *replication.Adapter
	logger             *slog.Logger
	dataDir            string
	attachmentsDir     string
}

func registerDiagnosticsRoutes(
	mux *routeMux,
	authentication *auth.Service,
	installation *operations.Installation,
	displayService *displays.Service,
	displayStream *displaystream.Hub,
	programStream *displaystream.Hub,
	scheduleStream *displaystream.Hub,
	votingStream *displaystream.Hub,
	scheduleMetrics *scheduleStreamMetrics,
	telemetryRuntime *telemetry.Runtime,
	replicationAdapter *replication.Adapter,
	logger *slog.Logger,
	listenerAddress net.Addr,
	dataDir string,
	attachmentsDir string,
	meterProvider metric.MeterProvider,
) diagnosticHandlers {
	handlers := diagnosticHandlers{
		authentication:     authentication,
		installation:       installation,
		displayService:     displayService,
		displayStream:      displayStream,
		programStream:      programStream,
		scheduleStream:     scheduleStream,
		votingStream:       votingStream,
		scheduleMetrics:    scheduleMetrics,
		telemetryRuntime:   telemetryRuntime,
		replicationAdapter: replicationAdapter,
		logger:             logger,
		dataDir:            dataDir,
		attachmentsDir:     attachmentsDir,
	}
	registerStorageGauges(registerStorageGaugesInput{
		MeterProvider:  meterProvider,
		DataDir:        dataDir,
		AttachmentsDir: attachmentsDir,
		Logger:         logger,
	})
	serve := func(response http.ResponseWriter, request *http.Request, actor auth.Account) {
		found := handlers.collect(request.Context(), actor)
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
		mux.HandleFunc("/diagnostics", crewRoute(), func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			if !probeMethodAllowed(response, request) {
				return
			}
			serve(response, request, auth.Account{Administrator: true})
		})
	}
	mux.HandleFunc("/admin/diagnostics", crewRoute(), func(
		response http.ResponseWriter,
		request *http.Request,
	) {
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
	return handlers
}

func (handlers diagnosticHandlers) collect(
	ctx context.Context,
	actor auth.Account,
) normalDiagnosticsResponse {
	readinessErr := handlers.installation.Ready(ctx)
	if readinessErr != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build readiness diagnostics",
			"component", "diagnostics",
			"error", readinessErr,
		)
	}
	statuses, statusErr := handlers.displayService.List(
		ctx,
		actor,
		handlers.displayStream.Cursor(),
	)
	if statusErr != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build diagnostics",
			"component", "diagnostics",
			"error", statusErr,
		)
	}
	capacity, capacityErr := handlers.installation.Capacity(ctx)
	if capacityErr != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build capacity diagnostics",
			"component", "diagnostics",
			"error", capacityErr,
		)
	}
	sessionCounts, sessionCountsErr := handlers.authentication.SessionCounts(ctx)
	if sessionCountsErr != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build authentication diagnostics",
			"component", "diagnostics",
			"error", sessionCountsErr,
		)
	}
	diagnostics := normalDiagnostics(
		statuses,
		statusErr,
		capacity,
		capacityErr,
		sessionCounts,
		sessionCountsErr,
		handlers.displayStream.SubscriberCount(),
		handlers.displayStream.DisplayCount(),
		handlers.programStream.SubscriberCount(),
		handlers.scheduleStream.SubscriberCount(),
		handlers.votingStream.SubscriberCount(),
		handlers.scheduleMetrics.snapshot(),
		handlers.telemetryRuntime != nil && handlers.telemetryRuntime.Enabled(),
		handlers.replicationAdapter,
	)
	if readinessErr != nil {
		diagnostics.Readiness.Status = "not_ready"
	}
	handlers.applyStorageDiagnostics(ctx, &diagnostics)
	handlers.applyBackupAgeDiagnostics(ctx, &diagnostics)
	return diagnostics
}

// diskSpaceWarningBytes is the free-space floor below which diagnostics
// reports a warning and logs it, giving an Administrator advance notice
// before the filesystem holding DataDir runs out mid-show.
const diskSpaceWarningBytes uint64 = 1 << 30 // 1 GiB

func (handlers diagnosticHandlers) applyStorageDiagnostics(
	ctx context.Context,
	diagnostics *normalDiagnosticsResponse,
) {
	stats, err := collectStorageStats(handlers.dataDir, handlers.attachmentsDir)
	if err != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build storage size diagnostics",
			"component", "diagnostics",
			"error", err,
		)
		diagnostics.DiskSpace.Status = "unavailable"
		return
	}
	diagnostics.DiskSpace.FreeBytes = stats.freeDiskBytes
	diagnostics.DiskSpace.WarningBytes = diskSpaceWarningBytes
	diagnostics.StorageSize.DatabaseBytes = stats.databaseBytes
	diagnostics.StorageSize.AttachmentsBytes = stats.attachmentsBytes
	if stats.freeDiskBytes < diskSpaceWarningBytes {
		diagnostics.DiskSpace.Status = "warning"
		handlers.logger.WarnContext(
			ctx,
			"disk free space below warning threshold",
			"component", "diagnostics",
			"status", "warning",
		)
		return
	}
	diagnostics.DiskSpace.Status = "ready"
}

func (handlers diagnosticHandlers) applyBackupAgeDiagnostics(
	ctx context.Context,
	diagnostics *normalDiagnosticsResponse,
) {
	completedAt, found, err := backup.LastCompletedAt(handlers.dataDir)
	if err != nil {
		handlers.logger.ErrorContext(
			ctx,
			"build Backup age diagnostics",
			"component", "diagnostics",
			"error", err,
		)
		return
	}
	if !found {
		return
	}
	age := time.Since(completedAt).Seconds()
	diagnostics.Backup.AgeSeconds = &age
}

type componentDiagnostics struct {
	Status string `json:"status"`
}

type streamDiagnostics struct {
	Status      string `json:"status"`
	Subscribers int    `json:"subscribers"`
}

type scheduleStreamDiagnostics struct {
	streamDiagnostics
	Connections            uint64 `json:"connections"`
	GapRecoveries          uint64 `json:"gap_recoveries"`
	IncompatibleRecoveries uint64 `json:"incompatible_recoveries"`
	Resnapshots            uint64 `json:"resnapshots"`
	SlowDrops              uint64 `json:"slow_drops"`
	Disconnects            uint64 `json:"disconnects"`
}

type authenticationDiagnostics struct {
	Status          string `json:"status"`
	Active          int    `json:"active"`
	Cached          int    `json:"cached"`
	Stored          int    `json:"stored"`
	PerAccountLimit int    `json:"per_account_limit"`
}

// backupDiagnostics reports Backup availability plus how long ago the most
// recent Backup completed. AgeSeconds is nil when no Backup has completed
// since the completion marker was introduced.
type backupDiagnostics struct {
	Status     string   `json:"status"`
	AgeSeconds *float64 `json:"age_seconds,omitempty"`
}

// diskSpaceDiagnostics reports free capacity on the filesystem holding
// DataDir, and the threshold below which Status becomes "warning".
type diskSpaceDiagnostics struct {
	Status       string `json:"status"`
	FreeBytes    uint64 `json:"free_bytes"`
	WarningBytes uint64 `json:"warning_bytes"`
}

// storageSizeDiagnostics reports the current on-disk size of the durable
// database and the Attachment Store.
type storageSizeDiagnostics struct {
	DatabaseBytes    uint64 `json:"database_bytes"`
	AttachmentsBytes uint64 `json:"attachments_bytes"`
}

type normalDiagnosticsResponse struct {
	Mode           string                    `json:"mode"`
	Readiness      componentDiagnostics      `json:"readiness"`
	Storage        componentDiagnostics      `json:"storage"`
	Backup         backupDiagnostics         `json:"backup"`
	DiskSpace      diskSpaceDiagnostics      `json:"disk_space"`
	StorageSize    storageSizeDiagnostics    `json:"storage_size"`
	Authentication authenticationDiagnostics `json:"authentication"`
	Replication    replication.Status        `json:"replication"`
	Streams        struct {
		Display  streamDiagnostics         `json:"display"`
		Program  streamDiagnostics         `json:"program"`
		Schedule scheduleStreamDiagnostics `json:"schedule"`
		Voting   streamDiagnostics         `json:"voting"`
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
	sessionCounts auth.SessionCounts,
	sessionCountsErr error,
	displaySubscribers int,
	connectedDisplays int,
	programSubscribers int,
	scheduleSubscribers int,
	votingSubscribers int,
	scheduleMetrics scheduleStreamMetricSnapshot,
	telemetryEnabled bool,
	replicationAdapter *replication.Adapter,
) normalDiagnosticsResponse {
	found := normalDiagnosticsResponse{
		Mode:        "normal",
		Readiness:   componentDiagnostics{Status: "ready"},
		Storage:     componentDiagnostics{Status: "ready"},
		Backup:      backupDiagnostics{Status: "available"},
		Replication: replication.Status{Status: "disabled"},
		Telemetry:   componentDiagnostics{Status: "disabled"},
		Capacity:    capacityDiagnostics{Status: "within_tested_envelope", Counts: capacity},
		Authentication: authenticationDiagnostics{
			Status:          "bounded",
			Active:          sessionCounts.Active,
			Cached:          sessionCounts.Cached,
			Stored:          sessionCounts.Stored,
			PerAccountLimit: sessionCounts.PerAccountLimit,
		},
	}
	found.Streams.Display = streamDiagnostics{
		Status: "ready", Subscribers: displaySubscribers,
	}
	found.Streams.Program = streamDiagnostics{
		Status: "ready", Subscribers: programSubscribers,
	}
	found.Streams.Schedule = scheduleStreamDiagnostics{
		streamDiagnostics: streamDiagnostics{
			Status: "ready", Subscribers: scheduleSubscribers,
		},
		Connections:            scheduleMetrics.Connections,
		GapRecoveries:          scheduleMetrics.GapRecoveries,
		IncompatibleRecoveries: scheduleMetrics.IncompatibleRecoveries,
		Resnapshots:            scheduleMetrics.Resnapshots,
		SlowDrops:              scheduleMetrics.SlowDrops,
		Disconnects:            scheduleMetrics.Disconnects,
	}
	found.Streams.Voting = streamDiagnostics{
		Status: "ready", Subscribers: votingSubscribers,
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
		found.Capacity.Warnings = capacityWarnings(capacity, connectedDisplays)
		if len(found.Capacity.Warnings) > 0 {
			found.Capacity.Status = "warning"
		}
	}
	if sessionCountsErr != nil {
		found.Authentication.Status = "unavailable"
	}
	if statusErr != nil {
		found.Readiness.Status = "not_ready"
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

func capacityWarnings(capacity operations.Capacity, connectedDisplays int) []capacityWarning {
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
	if connectedDisplays > testedDisplays {
		warnings = append(warnings, capacityWarning{
			Code: "displays", Observed: connectedDisplays, TestedMax: testedDisplays,
		})
	}
	return warnings
}

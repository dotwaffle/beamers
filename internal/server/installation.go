package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/frontend"
)

type installationHandlers struct {
	browser     frontendHandlers
	diagnostics diagnosticHandlers
	archives    archiveHandlers
}

func registerInstallationFrontendRoutes(
	mux *routeMux,
	diagnostics diagnosticHandlers,
	archives archiveHandlers,
) {
	handlers := installationHandlers{
		browser: frontendHandlers{
			authentication: archives.installation.Authentication(),
			logger:         archives.logger,
			random:         rand.Reader,
		},
		diagnostics: diagnostics,
		archives:    archives,
	}
	route := backstagePageRoute()
	route.timeout = restoreOperationTimeout
	route.maxBodyBytes = backup.MaxArchiveBytes + defaultRequestBytes
	mux.HandleFunc("/backstage/installation", route, handlers.installation)
}

func (handlers installationHandlers) installation(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.render(response, request, actor, csrfToken)
	case http.MethodPost:
		handlers.submit(response, request, actor)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers installationHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		http.Error(response, "invalid form submission", http.StatusBadRequest)
		return
	}
	if mediaType == "multipart/form-data" {
		if !handlers.browser.validMultipartForm(response, request) {
			return
		}
		defer func() {
			if err := request.MultipartForm.RemoveAll(); err != nil {
				handlers.browser.logger.Warn("remove Restore form staging", "error", err)
			}
		}()
		handlers.prepareRestore(response, request)
		return
	}
	if !handlers.browser.validForm(response, request) {
		return
	}
	switch request.Form.Get("action") {
	case "backup-sanitized":
		if request.Form.Get("confirm") != "true" {
			http.Error(response, "Backup confirmation required", http.StatusUnprocessableEntity)
			return
		}
		handlers.archives.downloadBackup(response, request, backup.Sanitized)
	case "backup-full-fidelity":
		if request.Form.Get("acknowledge_protection") != "true" {
			http.Error(
				response,
				"Full-Fidelity Backup protection acknowledgment required",
				http.StatusUnprocessableEntity,
			)
			return
		}
		if !handlers.reauthenticate(response, request, actor) {
			return
		}
		handlers.archives.downloadBackup(response, request, backup.FullFidelity)
	case "cancel-restore":
		if request.Form.Get("acknowledge_cancellation") != "true" {
			http.Error(
				response,
				"prepared Restore cancellation acknowledgment required",
				http.StatusUnprocessableEntity,
			)
			return
		}
		if !handlers.reauthenticate(response, request, actor) {
			return
		}
		journalPath, err := handlers.journalPath()
		if err != nil {
			handlers.archives.writeRestoreFailure(response, request, err)
			return
		}
		if err = handlers.archives.cancelRestore(request.Context(), journalPath); err != nil {
			handlers.archives.writeRestoreFailure(response, request, err)
			return
		}
		http.Redirect(
			response,
			request,
			"/backstage/installation?canceled=true",
			http.StatusSeeOther,
		)
	default:
		http.Error(response, "invalid installation action", http.StatusUnprocessableEntity)
	}
}

func (handlers installationHandlers) prepareRestore(
	response http.ResponseWriter,
	request *http.Request,
) {
	if request.FormValue("action") != "prepare-restore" {
		http.Error(response, "invalid installation action", http.StatusUnprocessableEntity)
		return
	}
	archive, _, err := request.FormFile("backup")
	if err != nil {
		http.Error(response, "Backup ZIP is required", http.StatusUnprocessableEntity)
		return
	}
	defer func() {
		if closeErr := archive.Close(); closeErr != nil {
			handlers.browser.logger.Warn("close Restore upload", "error", closeErr)
		}
	}()
	if _, err = handlers.archives.prepareRestore(request.Context(), archive); err != nil {
		handlers.archives.writeRestoreFailure(response, request, err)
		return
	}
	http.Redirect(
		response,
		request,
		"/backstage/installation?prepared=true",
		http.StatusSeeOther,
	)
}

func (handlers installationHandlers) reauthenticate(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
) bool {
	err := handlers.browser.authentication.Reauthenticate(
		request.Context(),
		actor,
		request.Form.Get("password"),
	)
	if errors.Is(err, auth.ErrAuthenticationFailed) {
		http.Error(response, "reauthentication failed", http.StatusUnauthorized)
		return false
	}
	if err != nil {
		handlers.browser.frontendError(response, request, "reauthenticate Administrator", err)
		return false
	}
	return true
}

func (handlers installationHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
) {
	diagnostics := handlers.diagnostics.collect(request.Context(), actor)
	journalPath, err := handlers.journalPath()
	if err != nil {
		handlers.browser.frontendError(response, request, "resolve Restore journal", err)
		return
	}
	plan, present, err := backup.InspectPreparedRestore(request.Context(), journalPath)
	if err != nil {
		handlers.browser.frontendError(response, request, "inspect prepared Restore", err)
		return
	}
	message := ""
	switch {
	case request.URL.Query().Get("prepared") == "true":
		message = "Restore prepared and validated."
	case request.URL.Query().Get("canceled") == "true":
		message = "Prepared Restore canceled."
	}
	statuses := []frontend.InstallationStatus{
		{Label: "Readiness", Status: diagnostics.Readiness.Status},
		{Label: "Maintenance", Status: "inactive"},
		{Label: "Storage", Status: diagnostics.Storage.Status},
		{Label: "Backup", Status: diagnostics.Backup.Status},
		{
			Label: "Authentication", Status: diagnostics.Authentication.Status,
			Detail: fmt.Sprintf(
				"%d active, %d stored sessions",
				diagnostics.Authentication.Active,
				diagnostics.Authentication.Stored,
			),
		},
		{
			Label: "Display stream", Status: diagnostics.Streams.Display.Status,
			Detail: strconv.Itoa(diagnostics.Streams.Display.Subscribers) + " subscribers",
		},
		{
			Label: "Program stream", Status: diagnostics.Streams.Program.Status,
			Detail: strconv.Itoa(diagnostics.Streams.Program.Subscribers) + " subscribers",
		},
		{
			Label: "Displays", Status: diagnostics.Displays.Status,
			Detail: strconv.Itoa(diagnostics.Displays.Total) + " registered",
		},
		{Label: "Replication", Status: diagnostics.Replication.Status},
		{Label: "Telemetry", Status: diagnostics.Telemetry.Status},
	}
	warnings := make([]string, 0, len(diagnostics.Capacity.Warnings))
	for _, warning := range diagnostics.Capacity.Warnings {
		warnings = append(warnings, fmt.Sprintf(
			"%s: %d exceeds tested maximum %d",
			warning.Code,
			warning.Observed,
			warning.TestedMax,
		))
	}
	prepared := frontend.PreparedRestore{Present: present}
	if present {
		prepared.Mode = string(plan.Manifest.Mode)
		prepared.CreatedAt = plan.Manifest.CreatedAt.Format(time.RFC3339)
		prepared.ReplacesData = plan.ReplacesData
		prepared.ReplacesAttachments = plan.ReplacesAttachments
		for _, difference := range plan.ConfigurationDifferences {
			prepared.ConfigurationDifferences = append(
				prepared.ConfigurationDifferences,
				difference.Field,
			)
		}
	}
	response.Header().Set("Cache-Control", "no-store")
	handlers.browser.render(
		response,
		request,
		http.StatusOK,
		frontend.Installation(frontend.InstallationPage{
			AccountName:    actor.Name,
			CSRFToken:      csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Navigation:     backstageNavigation(actor),
			Statuses:       statuses,
			Capacity: frontend.InstallationCapacity{
				Status:        diagnostics.Capacity.Status,
				ActiveEventID: diagnostics.Capacity.Counts.ActiveEventID,
				Locations:     diagnostics.Capacity.Counts.Locations,
				Lanes:         diagnostics.Capacity.Counts.Lanes,
				Sessions:      diagnostics.Capacity.Counts.Sessions,
				Entries:       diagnostics.Capacity.Counts.Entries,
				Displays:      diagnostics.Capacity.Counts.Displays,
				Warnings:      warnings,
			},
			Prepared: prepared,
			Message:  message,
		}),
	)
}

func (handlers installationHandlers) journalPath() (string, error) {
	dataDir, err := filepath.Abs(handlers.archives.dataDir)
	if err != nil {
		return "", err
	}
	return dataDir + ".beamers-restore.json", nil
}

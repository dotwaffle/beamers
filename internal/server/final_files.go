package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/operations"
)

type finalFilesHandlers struct {
	installation       *operations.Installation
	browser            frontendHandlers
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func registerFinalFilesRoutes(
	mux *routeMux,
	installation *operations.Installation,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := finalFilesHandlers{
		installation: installation,
		browser: frontendHandlers{
			authentication: installation.Authentication(),
			logger:         logger,
			random:         rand.Reader,
		},
		logger:             logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	backstageRoute := backstagePageRoute()
	backstageRoute.maxBodyBytes = maxAuthBodyBytes
	backstageRoute.timeout = restoreOperationTimeout
	mux.HandleFunc(
		"/backstage/events/{eventID}/final-files",
		backstageRoute,
		handlers.backstage,
	)
	mux.HandleFunc("/admin/final-files/preview", crewRoute(), handlers.preview)
	mux.HandleFunc(
		"/admin/final-files",
		routeContract{kind: crewInterface, timeout: restoreOperationTimeout},
		handlers.download,
	)
}

func (handlers finalFilesHandlers) backstage(
	response http.ResponseWriter,
	request *http.Request,
) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if _, granted := actor.EventRoles[eventID]; err != nil || !actor.Administrator || !granted {
		http.NotFound(response, request)
		return
	}
	event, err := handlers.installation.Events().CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Final Files Export Event", err)
		return
	}
	plan, err := handlers.installation.Attachments().PlanFinalFiles(
		request.Context(), eventID, "",
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "preview Final Files Export", err)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderBackstage(
			response, request, actor, event, plan, csrfToken, http.StatusOK, "",
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		digest := request.Form.Get("preview_digest")
		if digest == "" || request.Form.Get("confirmed") != "true" {
			handlers.renderBackstage(
				response,
				request,
				actor,
				event,
				plan,
				csrfToken,
				http.StatusBadRequest,
				"Review and confirm the exact preview before downloading.",
			)
			return
		}
		if digest != plan.PreviewDigest {
			handlers.renderBackstage(
				response,
				request,
				actor,
				event,
				plan,
				csrfToken,
				http.StatusConflict,
				"Preview changed. Review the current files before downloading.",
			)
			return
		}
		archive, size, archiveErr := handlers.prepareFinalFilesArchive(
			request, eventID, digest,
		)
		if errors.Is(archiveErr, attachments.ErrFinalFilesPreviewChanged) {
			plan, err = handlers.installation.Attachments().PlanFinalFiles(
				request.Context(), eventID, "",
			)
			if err != nil {
				handlers.browser.frontendError(
					response, request, "refresh Final Files Export preview", err,
				)
				return
			}
			handlers.renderBackstage(
				response,
				request,
				actor,
				event,
				plan,
				csrfToken,
				http.StatusConflict,
				"Preview changed. Review the current files before downloading.",
			)
			return
		}
		if archiveErr != nil {
			handlers.browser.frontendError(
				response, request, "prepare Final Files Export download", archiveErr,
			)
			return
		}
		handlers.serveFinalFilesArchive(response, request, archive, size)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers finalFilesHandlers) renderBackstage(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	event events.Event,
	plan attachments.FinalFilesPlan,
	csrfToken string,
	status int,
	message string,
) {
	var totalSize int64
	for _, file := range plan.Files {
		totalSize += file.SizeBytes
	}
	handlers.browser.render(response, request, status, frontend.FinalFiles(
		frontend.FinalFilesPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request), Navigation: backstageNavigation(actor),
			Event: event, Plan: plan, TotalSize: totalSize, Error: message,
		},
	))
}

func (handlers finalFilesHandlers) preview(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	if _, ok := authenticateAdministrator(
		response,
		request,
		handlers.installation.Authentication(),
		handlers.logger,
		"Final Files Export",
	); !ok {
		return
	}
	var input struct {
		EventID int `json:"event_id"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	plan, err := handlers.installation.Attachments().PlanFinalFiles(
		request.Context(), input.EventID, "",
	)
	if err != nil {
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(response).Encode(plan); err != nil {
		handlers.logger.ErrorContext(
			request.Context(), "write Final Files Export preview", "error", err,
		)
	}
}

func (handlers finalFilesHandlers) download(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	if _, ok := authenticateAdministrator(
		response,
		request,
		handlers.installation.Authentication(),
		handlers.logger,
		"Final Files Export",
	); !ok {
		return
	}
	var input struct {
		EventID       int    `json:"event_id"`
		PreviewDigest string `json:"preview_digest"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	archive, size, err := handlers.prepareFinalFilesArchive(
		request, input.EventID, input.PreviewDigest,
	)
	if err != nil {
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	handlers.serveFinalFilesArchive(response, request, archive, size)
}

func (handlers finalFilesHandlers) prepareFinalFilesArchive(
	request *http.Request,
	eventID int,
	previewDigest string,
) (*os.File, int64, error) {
	archive, err := os.CreateTemp("", ".beamers-final-files-*.zip")
	if err != nil {
		return nil, 0, err
	}
	keep := false
	defer func() {
		if !keep {
			handlers.cleanupFinalFilesArchive(archive)
		}
	}()
	if writeErr := handlers.installation.Attachments().WriteFinalFilesZIP(
		request.Context(), eventID, previewDigest, archive,
	); writeErr != nil {
		return nil, 0, writeErr
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	info, err := archive.Stat()
	if err != nil {
		return nil, 0, err
	}
	keep = true
	return archive, info.Size(), nil
}

func (handlers finalFilesHandlers) serveFinalFilesArchive(
	response http.ResponseWriter,
	request *http.Request,
	archive *os.File,
	size int64,
) {
	defer handlers.cleanupFinalFilesArchive(archive)
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set(
		"Content-Disposition",
		`attachment; filename="beamers-final-files.zip"`,
	)
	response.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if _, err := io.Copy(response, archive); err != nil {
		handlers.logger.ErrorContext(
			request.Context(), "write Final Files Export download", "error", err,
		)
	}
}

func (handlers finalFilesHandlers) cleanupFinalFilesArchive(archive *os.File) {
	if err := archive.Close(); err != nil {
		handlers.logger.Warn("close Final Files Export download", "error", err)
	}
	if err := os.Remove(archive.Name()); err != nil { //nolint:gosec // Process-owned path from os.CreateTemp.
		handlers.logger.Warn("remove Final Files Export staging", "error", err)
	}
}

func (handlers finalFilesHandlers) writeFinalFilesFailure(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	status := http.StatusInternalServerError
	if errors.Is(err, attachments.ErrInvalidInput) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, attachments.ErrFinalFilesPreviewChanged) ||
		errors.Is(err, attachments.ErrFinalFilesDestinationExists) ||
		errors.Is(err, os.ErrExist) {
		status = http.StatusConflict
	}
	handlers.logger.ErrorContext(request.Context(), "Final Files Export failed", "error", err)
	http.Error(response, "Final Files Export unavailable", status)
}

package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/operations"
)

type finalFilesHandlers struct {
	installation       *operations.Installation
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func registerFinalFilesRoutes(
	mux *http.ServeMux,
	installation *operations.Installation,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := finalFilesHandlers{
		installation:       installation,
		logger:             logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc("/admin/final-files/preview", handlers.preview)
	mux.HandleFunc("/admin/final-files", handlers.download)
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
	workDir, err := os.MkdirTemp("", ".beamers-final-files-*")
	if err != nil {
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			handlers.logger.Warn("remove Final Files Export staging", "error", removeErr)
		}
	}()
	archivePath := filepath.Join(workDir, "beamers-final-files.zip")
	archive, err := os.OpenFile( //nolint:gosec // Private process-owned download staging.
		archivePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600,
	)
	if err != nil {
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	if err = handlers.installation.Attachments().WriteFinalFilesZIP(
		request.Context(), input.EventID, input.PreviewDigest, archive,
	); err != nil {
		_ = archive.Close()
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	info, err := archive.Stat()
	if err != nil {
		_ = archive.Close()
		handlers.writeFinalFilesFailure(response, request, err)
		return
	}
	defer func() {
		if closeErr := archive.Close(); closeErr != nil {
			handlers.logger.Warn("close Final Files Export download", "error", closeErr)
		}
	}()
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set(
		"Content-Disposition",
		`attachment; filename="beamers-final-files.zip"`,
	)
	response.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if _, err = io.Copy(response, archive); err != nil {
		handlers.logger.ErrorContext(
			request.Context(), "write Final Files Export download", "error", err,
		)
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

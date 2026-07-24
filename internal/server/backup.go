package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/operations"
)

const maxRestoreUploadBytes int64 = 65 << 30

type backupHandlers struct {
	installation       *operations.Installation
	dataDir            string
	attachmentsDir     string
	restore            func(context.Context, string) error
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func registerBackupRoutes(
	mux *http.ServeMux,
	installation *operations.Installation,
	dataDir string,
	attachmentsDir string,
	restore func(context.Context, string) error,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := backupHandlers{
		installation:       installation,
		dataDir:            dataDir,
		attachmentsDir:     attachmentsDir,
		restore:            restore,
		logger:             logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc("/admin/backups", handlers.create)
	mux.HandleFunc("/admin/restores/preview", handlers.previewRestore)
	mux.HandleFunc("/admin/restores/apply", handlers.applyRestore)
}

func (handlers backupHandlers) previewRestore(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/zip" {
		http.Error(response, "Restore preview requires a ZIP Backup", http.StatusBadRequest)
		return
	}
	workDir, err := os.MkdirTemp(
		filepath.Dir(handlers.dataDir),
		".beamers-admin-restore-*",
	)
	if err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			handlers.logger.Warn("remove Restore upload staging", "error", removeErr)
		}
	}()
	archivePath := filepath.Join(workDir, "backup.zip")
	archive, err := os.OpenFile( //nolint:gosec // Private process-owned upload staging.
		archivePath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRestoreUploadBytes)
	_, copyErr := io.Copy(archive, request.Body)
	closeErr := archive.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	plan, err := operations.PrepareRestore(request.Context(), backup.RestoreInput{
		InputPath:      archivePath,
		DataDir:        handlers.dataDir,
		AttachmentsDir: handlers.attachmentsDir,
		Replace:        true,
	})
	if err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(response).Encode(plan); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Restore preview", "error", err)
	}
}

func (handlers backupHandlers) applyRestore(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	var input struct {
		Password               string `json:"password"`
		AcknowledgeReplacement bool   `json:"acknowledge_replacement"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	if !input.AcknowledgeReplacement {
		http.Error(
			response,
			"Restore replacement acknowledgment required",
			http.StatusUnprocessableEntity,
		)
		return
	}
	if err := handlers.installation.Authentication().Reauthenticate(
		request.Context(),
		actor,
		input.Password,
	); err != nil {
		if errors.Is(err, auth.ErrAuthenticationFailed) {
			http.Error(response, "reauthentication failed", http.StatusUnauthorized)
			return
		}
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	dataDir, err := filepath.Abs(handlers.dataDir)
	if err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	if err = handlers.restore(
		context.WithoutCancel(request.Context()),
		dataDir+".beamers-restore.json",
	); err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write([]byte("{\"restored\":true}\n"))
}

func (handlers backupHandlers) create(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	var input struct {
		Mode                          backup.Mode `json:"mode"`
		Password                      string      `json:"password"`
		AcknowledgeUnencryptedSecrets bool        `json:"acknowledge_unencrypted_secrets"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	if input.Mode == "" {
		input.Mode = backup.Sanitized
	}
	if input.Mode != backup.Sanitized && input.Mode != backup.FullFidelity {
		http.Error(response, "invalid Backup mode", http.StatusUnprocessableEntity)
		return
	}
	if input.Mode == backup.FullFidelity {
		if !input.AcknowledgeUnencryptedSecrets {
			http.Error(
				response,
				"Full-Fidelity Backup requires unencrypted-secret acknowledgment",
				http.StatusUnprocessableEntity,
			)
			return
		}
		if err := handlers.installation.Authentication().Reauthenticate(
			request.Context(),
			actor,
			input.Password,
		); err != nil {
			if errors.Is(err, auth.ErrAuthenticationFailed) {
				http.Error(response, "reauthentication failed", http.StatusUnauthorized)
				return
			}
			handlers.logger.ErrorContext(
				request.Context(),
				"Backup reauthentication failed",
				"error", err,
			)
			http.Error(response, "reauthentication unavailable", http.StatusInternalServerError)
			return
		}
	}

	workDir, err := os.MkdirTemp("", ".beamers-admin-backup-*")
	if err != nil {
		handlers.writeFailure(response, request, err)
		return
	}
	defer func() {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			handlers.logger.Warn("remove Backup download staging", "error", removeErr)
		}
	}()
	archivePath := filepath.Join(workDir, "beamers-backup.zip")
	manifest, err := handlers.installation.CreateBackup(
		request.Context(),
		backup.CreateInput{
			AttachmentsDir: handlers.attachmentsDir,
			OutputPath:     archivePath,
			Mode:           input.Mode,
		},
	)
	if err != nil {
		handlers.writeFailure(response, request, err)
		return
	}
	archive, err := os.Open(archivePath) //nolint:gosec // Private process-owned staging path.
	if err != nil {
		handlers.writeFailure(response, request, err)
		return
	}
	defer func() {
		if closeErr := archive.Close(); closeErr != nil {
			handlers.logger.Warn("close Backup download", "error", closeErr)
		}
	}()
	info, err := archive.Stat()
	if err != nil {
		handlers.writeFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", `attachment; filename="beamers-backup.zip"`)
	response.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	response.Header().Set("X-Beamers-Backup-Mode", string(manifest.Mode))
	if _, err = io.Copy(response, archive); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Backup download", "error", err)
	}
}

func (handlers backupHandlers) authenticate(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	actor, err := handlers.installation.Authentication().Authenticate(
		request.Context(),
		cookie.Value,
	)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Backup authentication failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return actor, true
}

func (handlers backupHandlers) writeFailure(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	handlers.logger.ErrorContext(request.Context(), "create Backup failed", "error", err)
	http.Error(response, "Backup unavailable", http.StatusInternalServerError)
}

func (handlers backupHandlers) writeRestoreFailure(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	handlers.logger.ErrorContext(request.Context(), "Restore failed", "error", err)
	http.Error(response, "Restore unavailable", http.StatusInternalServerError)
}

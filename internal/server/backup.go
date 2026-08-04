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
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/operations"
)

const restoreOperationTimeout = 30 * time.Minute

type archiveHandlers struct {
	installation       *operations.Installation
	dataDir            string
	attachmentsDir     string
	cancelRestore      func(context.Context, string) error
	configuration      backup.Configuration
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func backupConfiguration(config Config) backup.Configuration {
	trustedProxies := make([]string, len(config.TrustedProxies))
	for index, prefix := range config.TrustedProxies {
		trustedProxies[index] = prefix.String()
	}
	return backup.RedactConfiguration(backup.Configuration{
		DataDir:         config.DataDir,
		AttachmentsDir:  config.AttachmentsDir,
		ListenAddress:   config.ListenAddress,
		PublicListen:    config.PublicListen,
		TLSCertificate:  config.TLSCertificate,
		TLSPrivateKey:   config.TLSPrivateKey,
		TrustedProxies:  trustedProxies,
		ReplicaURL:      config.ReplicaURL,
		ShutdownTimeout: config.ShutdownTimeout,
		InsecureCrew:    config.InsecureCrew,
		InsecureDisplay: config.InsecureDisplay,
	})
}

func registerBackupRoutes(
	mux *routeMux,
	installation *operations.Installation,
	dataDir string,
	attachmentsDir string,
	configuration backup.Configuration,
	cancelRestore func(context.Context, string) error,
	logger *slog.Logger,
	listenerAddress net.Addr,
) archiveHandlers {
	handlers := archiveHandlers{
		installation:       installation,
		dataDir:            dataDir,
		attachmentsDir:     attachmentsDir,
		configuration:      configuration,
		cancelRestore:      cancelRestore,
		logger:             logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc("/admin/backups", crewRoute(), handlers.create)
	mux.HandleFunc(
		"/admin/restores/preview",
		routeContract{
			kind: crewInterface, timeout: restoreOperationTimeout,
			maxBodyBytes: backup.MaxArchiveBytes, recoveryLimit: 10,
		},
		handlers.previewRestore,
	)
	mux.HandleFunc(
		"/admin/restores/cancel",
		routeContract{
			kind: crewInterface, timeout: restoreOperationTimeout, recoveryLimit: 5,
		},
		handlers.cancelPreparedRestore,
	)
	return handlers
}

func (handlers archiveHandlers) previewRestore(
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
		"Restore",
	); !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/zip" {
		http.Error(response, "Restore preview requires a ZIP Backup", http.StatusBadRequest)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, backup.MaxArchiveBytes)
	plan, err := handlers.prepareRestore(request.Context(), request.Body)
	if err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(response).Encode(plan); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Restore preview", "error", err)
	}
}

func (handlers archiveHandlers) prepareRestore(
	ctx context.Context,
	source io.Reader,
) (backup.RestorePlan, error) {
	workDir, err := os.MkdirTemp(
		filepath.Dir(handlers.dataDir),
		".beamers-admin-restore-*",
	)
	if err != nil {
		return backup.RestorePlan{}, err
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
		return backup.RestorePlan{}, err
	}
	_, copyErr := io.Copy(archive, source)
	closeErr := archive.Close()
	if copyCloseErr := errors.Join(copyErr, closeErr); copyCloseErr != nil {
		return backup.RestorePlan{}, copyCloseErr
	}
	plan, err := backup.PrepareRestore(ctx, backup.RestoreInput{
		InputPath:      archivePath,
		DataDir:        handlers.dataDir,
		AttachmentsDir: handlers.attachmentsDir,
		Configuration:  handlers.configuration,
		Replace:        true,
	})
	if err != nil {
		return backup.RestorePlan{}, err
	}
	return plan, nil
}

func (handlers archiveHandlers) cancelPreparedRestore(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := authenticateAdministrator(
		response,
		request,
		handlers.installation.Authentication(),
		handlers.logger,
		"Restore",
	)
	if !ok {
		return
	}
	var input struct {
		Password                   string `json:"password"`
		AcknowledgeAbandonPrepared bool   `json:"acknowledge_abandon_prepared_restore"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	if !input.AcknowledgeAbandonPrepared {
		http.Error(
			response,
			"prepared Restore cancellation acknowledgment required",
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
	restoreContext, cancel := context.WithTimeout(
		context.WithoutCancel(request.Context()),
		restoreOperationTimeout,
	)
	defer cancel()
	if err = handlers.cancelRestore(
		restoreContext,
		dataDir+".beamers-restore.json",
	); err != nil {
		handlers.writeRestoreFailure(response, request, err)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write([]byte("{\"canceled\":true}\n"))
}

func (handlers archiveHandlers) create(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := authenticateAdministrator(
		response,
		request,
		handlers.installation.Authentication(),
		handlers.logger,
		"Backup",
	)
	if !ok {
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
	handlers.downloadBackup(response, request, input.Mode)
}

func (handlers archiveHandlers) downloadBackup(
	response http.ResponseWriter,
	request *http.Request,
	mode backup.Mode,
) {
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
			DataDir:        handlers.dataDir,
			AttachmentsDir: handlers.attachmentsDir,
			OutputPath:     archivePath,
			Mode:           mode,
			Configuration:  handlers.configuration,
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
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Beamers-Backup-Mode", string(manifest.Mode))
	if err = writeArchiveDownload(response, archive); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Backup download", "error", err)
	}
}

func (handlers archiveHandlers) writeFailure(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	handlers.logger.ErrorContext(request.Context(), "create Backup failed", "error", err)
	http.Error(response, "Backup unavailable", http.StatusInternalServerError)
}

func (handlers archiveHandlers) writeRestoreFailure(
	response http.ResponseWriter,
	request *http.Request,
	err error,
) {
	handlers.logger.ErrorContext(request.Context(), "Restore failed", "error", err)
	http.Error(response, "Restore unavailable", http.StatusInternalServerError)
}

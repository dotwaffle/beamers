package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
)

type attachmentHandlers struct {
	authentication     *auth.Service
	attachments        *attachments.Service
	logger             *slog.Logger
	allowPlaintextCrew bool
	uploadLimiter      *authFailureLimiter
}

func registerAttachmentRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *attachments.Service,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := attachmentHandlers{
		authentication: authentication, attachments: service, logger: logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
		uploadLimiter:      newAuthFailureLimiter(time.Now),
	}
	mux.HandleFunc("/crew/events/{eventID}/upload-links", handlers.issueUploadLink)
	mux.HandleFunc("/crew/events/{eventID}/upload-links/{linkID}/revoke", handlers.revokeUploadLink)
	mux.HandleFunc("/crew/events/{eventID}/reopen-windows", handlers.createReopenWindow)
	mux.HandleFunc("/crew/events/{eventID}/reopen-windows/{windowID}", handlers.updateReopenWindow)
	mux.HandleFunc("/crew/events/{eventID}/attachments", handlers.uploadForCrew)
	mux.HandleFunc("/crew/events/{eventID}/attachment-versions/{versionID}", handlers.readVersion)
	mux.HandleFunc("/upload/{token}", handlers.upload)
}

func (handlers attachmentHandlers) issueUploadLink(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "upload target not found", http.StatusNotFound)
		return
	}
	var input attachments.IssueLinkInput
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	issued, err := handlers.attachments.IssueUploadLink(request.Context(), actor, input)
	switch {
	case errors.Is(err, attachments.ErrProducerRequired):
		http.Error(response, "event access denied", http.StatusForbidden)
		return
	case errors.Is(err, attachments.ErrUploadTargetNotFound):
		http.Error(response, "upload target not found", http.StatusNotFound)
		return
	case errors.Is(err, attachments.ErrInvalidInput), errors.Is(err, command.ErrInvalidID):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, attachments.ErrCommandConflict):
		http.Error(response, "command ID conflict", http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "issue Upload Link failed", "error", err)
		http.Error(response, "Upload Link unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(response).Encode(issued); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Upload Link", "error", err)
	}
}

func (handlers attachmentHandlers) upload(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clientKey, credentialKey := uploadLimitKeys(request, request.PathValue("token"))
	if retryAfter, blocked := handlers.uploadLimiter.blocked(clientKey, credentialKey); blocked {
		writeUploadRateLimit(response, retryAfter)
		return
	}
	handlers.uploadLimiter.record(clientKey, credentialKey)
	request.Body = http.MaxBytesReader(response, request.Body, (64<<20)+(1<<20))
	name, filename, mediaType, body, ok := multipartUpload(response, request)
	if !ok {
		return
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			handlers.logger.Warn("close uploaded Attachment", "error", closeErr)
		}
	}()
	created, err := handlers.attachments.Upload(request.Context(), attachments.UploadInput{
		Token: request.PathValue("token"), CommandID: request.FormValue("command_id"), Name: name,
		OriginalFilename: filename, MediaType: mediaType, Body: body,
	})
	handlers.writeUploadResult(response, request, created, err)
}

func writeUploadRateLimit(response http.ResponseWriter, retryAfter time.Duration) {
	seconds := max(1, int(retryAfter.Round(time.Second)/time.Second))
	response.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(response, "upload rate limit exceeded", http.StatusTooManyRequests)
}

func (handlers attachmentHandlers) uploadForCrew(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "upload target not found", http.StatusNotFound)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, (64<<20)+(1<<20))
	name, filename, mediaType, body, ok := multipartUpload(response, request)
	if !ok {
		return
	}
	defer func() {
		if closeErr := body.Close(); closeErr != nil {
			handlers.logger.Warn("close crew-uploaded Attachment", "error", closeErr)
		}
	}()
	targetID, parseErr := strconv.Atoi(request.FormValue("target_id"))
	if parseErr != nil {
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
		return
	}
	created, err := handlers.attachments.UploadForCrew(request.Context(), actor, attachments.CrewUploadInput{
		EventID: eventID, TargetType: attachments.TargetKind(request.FormValue("target_type")), TargetID: targetID,
		CommandID: request.FormValue("command_id"), Name: name,
		OriginalFilename: filename, MediaType: mediaType, Body: body,
	})
	handlers.writeUploadResult(response, request, created, err)
}

func multipartUpload(
	response http.ResponseWriter,
	request *http.Request,
) (name, filename, mediaType string, body interface {
	Read([]byte) (int, error)
	Close() error
}, ok bool) {
	// MaxBytesReader is installed by both callers before multipart parsing.
	if err := request.ParseMultipartForm(64 << 20); err != nil { //nolint:gosec // Request bytes are bounded.
		http.Error(response, "invalid upload", http.StatusBadRequest)
		return "", "", "", nil, false
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		http.Error(response, "file is required", http.StatusUnprocessableEntity)
		return "", "", "", nil, false
	}
	return request.FormValue("name"), header.Filename, header.Header.Get("Content-Type"), file, true
}

func (handlers attachmentHandlers) writeUploadResult(
	response http.ResponseWriter,
	request *http.Request,
	created attachments.Version,
	err error,
) {
	switch {
	case errors.Is(err, attachments.ErrUploadLinkInvalid):
		http.Error(response, "upload link not found", http.StatusNotFound)
		return
	case errors.Is(err, attachments.ErrUploadClosed):
		http.Error(response, "uploads are closed", http.StatusGone)
		return
	case errors.Is(err, attachments.ErrProducerRequired):
		http.Error(response, "event access denied", http.StatusForbidden)
		return
	case errors.Is(err, attachments.ErrUploadTargetNotFound):
		http.Error(response, "upload target not found", http.StatusNotFound)
		return
	case errors.Is(err, attachments.ErrAttachmentTooLarge):
		http.Error(response, "attachment too large", http.StatusRequestEntityTooLarge)
		return
	case errors.Is(err, attachments.ErrInvalidInput):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, command.ErrInvalidID):
		http.Error(response, "invalid command ID", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, attachments.ErrCommandConflict):
		http.Error(response, "command ID conflict", http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "upload Attachment failed", "error", err)
		http.Error(response, "Attachment upload unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(response).Encode(created); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Attachment Version", "error", err)
	}
}

func (handlers attachmentHandlers) revokeUploadLink(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	linkID, linkErr := positivePathID(request, "linkID")
	if eventErr != nil || linkErr != nil {
		http.Error(response, "Upload Link not found", http.StatusNotFound)
		return
	}
	var input struct {
		CommandID string `json:"command_id"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	err := handlers.attachments.RevokeUploadLink(request.Context(), actor, eventID, linkID, input.CommandID)
	if errors.Is(err, attachments.ErrProducerRequired) {
		http.Error(response, "event access denied", http.StatusForbidden)
		return
	}
	if errors.Is(err, attachments.ErrUploadTargetNotFound) {
		http.Error(response, "Upload Link not found", http.StatusNotFound)
		return
	}
	if errors.Is(err, command.ErrInvalidID) {
		http.Error(response, "invalid command ID", http.StatusUnprocessableEntity)
		return
	}
	if errors.Is(err, attachments.ErrCommandConflict) {
		http.Error(response, "command ID conflict", http.StatusConflict)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "revoke Upload Link failed", "error", err)
		http.Error(response, "Upload Link unavailable", http.StatusInternalServerError)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handlers attachmentHandlers) createReopenWindow(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "upload target not found", http.StatusNotFound)
		return
	}
	var input attachments.ReopenInput
	if err = decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	created, err := handlers.attachments.CreateReopenWindow(request.Context(), actor, input)
	switch {
	case errors.Is(err, attachments.ErrProducerRequired):
		http.Error(response, "event access denied", http.StatusForbidden)
	case errors.Is(err, attachments.ErrUploadTargetNotFound):
		http.Error(response, "upload target not found", http.StatusNotFound)
	case errors.Is(err, attachments.ErrInvalidInput):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case errors.Is(err, command.ErrInvalidID):
		http.Error(response, "invalid command ID", http.StatusUnprocessableEntity)
	case errors.Is(err, attachments.ErrCommandConflict):
		http.Error(response, "command ID conflict", http.StatusConflict)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "create Reopen Window failed", "error", err)
		http.Error(response, "Reopen Window unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		if encodeErr := json.NewEncoder(response).Encode(created); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Reopen Window", "error", encodeErr)
		}
	}
}

func (handlers attachmentHandlers) updateReopenWindow(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPatch, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	windowID, windowErr := positivePathID(request, "windowID")
	if eventErr != nil || windowErr != nil {
		http.Error(response, "Reopen Window not found", http.StatusNotFound)
		return
	}
	var input attachments.UpdateReopenInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID, input.WindowID = eventID, windowID
	updated, err := handlers.attachments.UpdateReopenWindow(request.Context(), actor, input)
	switch {
	case errors.Is(err, attachments.ErrProducerRequired):
		http.Error(response, "event access denied", http.StatusForbidden)
	case errors.Is(err, attachments.ErrUploadTargetNotFound):
		http.Error(response, "Reopen Window not found", http.StatusNotFound)
	case errors.Is(err, attachments.ErrReopenWindowRevision),
		errors.Is(err, attachments.ErrCommandConflict):
		http.Error(response, "Reopen Window conflict", http.StatusConflict)
	case errors.Is(err, attachments.ErrInvalidInput),
		errors.Is(err, attachments.ErrReopenWindowExtension),
		errors.Is(err, command.ErrInvalidID):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "update Reopen Window failed", "error", err)
		http.Error(response, "Reopen Window unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(response).Encode(updated); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write updated Reopen Window", "error", encodeErr)
		}
	}
}

func (handlers attachmentHandlers) readVersion(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	versionID, versionErr := positivePathID(request, "versionID")
	if eventErr != nil || versionErr != nil {
		http.Error(response, "Attachment Version not found", http.StatusNotFound)
		return
	}
	found, content, err := handlers.attachments.ReadVersion(request.Context(), actor, eventID, versionID)
	if errors.Is(err, attachments.ErrUploadTargetNotFound) {
		http.Error(response, "Attachment Version not found", http.StatusNotFound)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read Attachment Version failed", "error", err)
		http.Error(response, "Attachment Version unavailable", http.StatusInternalServerError)
		return
	}
	if found.MediaType != "" {
		response.Header().Set("Content-Type", found.MediaType)
	}
	response.Header().Set("Content-Disposition", mime.FormatMediaType(
		"attachment", map[string]string{"filename": found.OriginalFilename},
	))
	response.WriteHeader(http.StatusOK)
	if _, err = response.Write(content); err != nil { //nolint:gosec // Verified file bytes are an attachment response, not HTML.
		handlers.logger.ErrorContext(request.Context(), "write Attachment bytes", "error", err)
	}
}

func (handlers attachmentHandlers) authenticate(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Account session lookup failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return actor, true
}

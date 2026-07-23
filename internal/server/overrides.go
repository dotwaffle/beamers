package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/overrides"
	"github.com/dotwaffle/beamers/internal/store"
)

type overrideHandlers struct {
	authentication     *auth.Service
	service            *overrides.Service
	notify             func()
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func registerOverrideRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	service *overrides.Service,
	notify func(),
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := overrideHandlers{
		authentication: authentication, service: service, notify: notify, logger: logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc(
		"/crew/events/{eventID}/stage-message-configuration",
		handlers.configureStageMessages,
	)
	mux.HandleFunc("/crew/events/{eventID}/stage-messages", handlers.sendStageMessage)
	mux.HandleFunc(
		"/crew/events/{eventID}/stage-messages/preview",
		handlers.previewStageMessage,
	)
	mux.HandleFunc(
		"/crew/events/{eventID}/technical-difficulties",
		handlers.activateTechnicalDifficulties,
	)
	mux.HandleFunc(
		"/crew/events/{eventID}/technical-difficulties/preview",
		handlers.previewTechnicalDifficulties,
	)
	mux.HandleFunc(
		"/crew/events/{eventID}/overrides/{overrideID}/clear",
		handlers.clearOverride,
	)
	mux.HandleFunc("/crew/events/{eventID}/overrides", handlers.listActiveOverrides)
	mux.HandleFunc("/crew/events/{eventID}/urgent-notices/preview", handlers.previewUrgentNotice)
	mux.HandleFunc("/crew/events/{eventID}/urgent-notices", handlers.activateUrgentNotice)
	mux.HandleFunc("/crew/events/{eventID}/emergency-alerts/preview", handlers.previewEmergencyAlert)
	mux.HandleFunc("/crew/events/{eventID}/emergency-alerts", handlers.activateEmergencyAlert)
	mux.HandleFunc(
		"/crew/events/{eventID}/emergency-alerts/confirmation",
		handlers.emergencyAlertConfirmation,
	)
	mux.HandleFunc(
		"/crew/events/{eventID}/overrides/{overrideID}/clear-confirmation",
		handlers.emergencyClearConfirmation,
	)
}

func (handlers overrideHandlers) emergencyAlertConfirmation(
	response http.ResponseWriter,
	request *http.Request,
) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requestAllowed(response, request, request.Method, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	if request.Method == http.MethodPost {
		if err := request.ParseForm(); err != nil {
			http.Error(response, "invalid request", http.StatusBadRequest)
			return
		}
		input := priorityFormInput(eventID, request)
		input.Confirmed = true
		result, err := handlers.service.ActivateEmergencyAlert(request.Context(), actor, input)
		handlers.writeResult(response, request, result, err)
		return
	}
	input := priorityQueryInput(eventID, request)
	preview, err := handlers.service.PreviewEmergencyAlert(request.Context(), actor, input)
	if err != nil {
		handlers.writePriorityPreview(response, request, nil, err)
		return
	}
	commandID, err := confirmationCommandID("emergency")
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "create Emergency command identity", "error", err)
		http.Error(response, "Emergency confirmation unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err = overrides.EmergencyAlertConfirmationPage(preview, commandID). //nolint:contextcheck // Generated templ closures receive context when rendered.
										Render(request.Context(), response); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Emergency confirmation", "error", err)
	}
}

func confirmationCommandID(prefix string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(nonce[:]), nil
}

func (handlers overrideHandlers) emergencyClearConfirmation(
	response http.ResponseWriter,
	request *http.Request,
) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requestAllowed(response, request, request.Method, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	overrideID, err := positivePathID(request, "overrideID")
	if err != nil {
		http.Error(response, "Display Override not found", http.StatusNotFound)
		return
	}
	if request.Method == http.MethodPost {
		if err = request.ParseForm(); err != nil {
			http.Error(response, "invalid request", http.StatusBadRequest)
			return
		}
		revision, parseErr := strconv.Atoi(request.FormValue("expected_revision"))
		if parseErr != nil {
			http.Error(response, "invalid request", http.StatusBadRequest)
			return
		}
		result, clearErr := handlers.service.Clear(request.Context(), actor, overrides.ClearInput{
			EventID: eventID, OverrideID: overrideID, ExpectedRevision: revision,
			CommandID: request.FormValue("command_id"), Confirmed: true,
			ConfirmationMethod: request.FormValue("confirmation_method"),
		})
		handlers.writeResult(response, request, result, clearErr)
		return
	}
	active, err := handlers.service.ListActive(request.Context(), actor, eventID)
	if err != nil {
		handlers.writePreviewList(response, request, nil, err)
		return
	}
	for _, item := range active {
		if item.ID != overrideID || item.Kind != store.DisplayOverrideEmergencyAlert {
			continue
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err = overrides.EmergencyClearConfirmationPage(item). //nolint:contextcheck // Generated templ closures receive context when rendered.
										Render(request.Context(), response); err != nil {
			handlers.logger.ErrorContext(request.Context(), "write Emergency clear confirmation", "error", err)
		}
		return
	}
	http.Error(response, "Display Override not found", http.StatusNotFound)
}

func priorityQueryInput(eventID int, request *http.Request) overrides.PriorityInput {
	targetID, _ := strconv.Atoi(request.URL.Query().Get("target_id"))
	return overrides.PriorityInput{
		EventID: eventID, Text: request.URL.Query().Get("text"),
		Target: overrides.Target{
			Type: overrides.TargetType(request.URL.Query().Get("target_type")),
			ID:   targetID, Key: request.URL.Query().Get("target_key"),
		},
	}
}

func priorityFormInput(eventID int, request *http.Request) overrides.PriorityInput {
	targetID, _ := strconv.Atoi(request.FormValue("target_id"))
	return overrides.PriorityInput{
		EventID: eventID, Text: request.FormValue("text"),
		Target: overrides.Target{
			Type: overrides.TargetType(request.FormValue("target_type")),
			ID:   targetID, Key: request.FormValue("target_key"),
		},
		PreviewFingerprint: request.FormValue("preview_fingerprint"),
		CommandID:          request.FormValue("command_id"),
		ConfirmationMethod: request.FormValue("confirmation_method"),
	}
}

func (handlers overrideHandlers) previewUrgentNotice(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.priorityRequest(response, request, true, func(
		ctx context.Context, actor auth.Account, input overrides.PriorityInput,
	) (any, error) {
		return handlers.service.PreviewUrgentNotice(ctx, actor, input)
	})
}

func (handlers overrideHandlers) activateUrgentNotice(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.priorityRequest(response, request, false, func(
		ctx context.Context, actor auth.Account, input overrides.PriorityInput,
	) (any, error) {
		return handlers.service.ActivateUrgentNotice(ctx, actor, input)
	})
}

func (handlers overrideHandlers) previewEmergencyAlert(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.priorityRequest(response, request, true, func(
		ctx context.Context, actor auth.Account, input overrides.PriorityInput,
	) (any, error) {
		return handlers.service.PreviewEmergencyAlert(ctx, actor, input)
	})
}

func (handlers overrideHandlers) activateEmergencyAlert(
	response http.ResponseWriter,
	request *http.Request,
) {
	handlers.priorityRequest(response, request, false, func(
		ctx context.Context, actor auth.Account, input overrides.PriorityInput,
	) (any, error) {
		return handlers.service.ActivateEmergencyAlert(ctx, actor, input)
	})
}

func (handlers overrideHandlers) priorityRequest(
	response http.ResponseWriter,
	request *http.Request,
	preview bool,
	apply func(context.Context, auth.Account, overrides.PriorityInput) (any, error),
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.PriorityInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := apply(request.Context(), actor, input)
	if preview {
		handlers.writePriorityPreview(response, request, result, err)
		return
	}
	handlers.writeResult(response, request, result, err)
}

func (handlers overrideHandlers) writePriorityPreview(
	response http.ResponseWriter,
	request *http.Request,
	result any,
	err error,
) {
	switch {
	case errors.Is(err, overrides.ErrScopeDenied):
		http.Error(response, "Display Group access denied", http.StatusForbidden)
	case errors.Is(err, overrides.ErrNotFound):
		http.Error(response, "Display Override not found", http.StatusNotFound)
	case errors.Is(err, overrides.ErrRevision):
		http.Error(response, "Display Override conflict", http.StatusConflict)
	case errors.Is(err, overrides.ErrInvalidInput),
		errors.Is(err, overrides.ErrEventNotActive):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "Display Override preview failed", "error", err)
		http.Error(response, "Display Override unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(response).Encode(result); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Display Override preview", "error", encodeErr)
		}
	}
}

func (handlers overrideHandlers) listActiveOverrides(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	result, err := handlers.service.ListActive(request.Context(), actor, eventID)
	handlers.writePreviewList(response, request, result, err)
}

func (handlers overrideHandlers) previewStageMessage(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.SendStageMessageInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := handlers.service.PreviewStageMessage(request.Context(), actor, input)
	handlers.writePreview(response, request, result, err)
}

func (handlers overrideHandlers) configureStageMessages(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPatch, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.ConfigureInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := handlers.service.ConfigureStageMessages(request.Context(), actor, input)
	handlers.writeResult(response, request, result, err)
}

func (handlers overrideHandlers) previewTechnicalDifficulties(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.TechnicalDifficultiesInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := handlers.service.PreviewTechnicalDifficulties(request.Context(), actor, input)
	handlers.writePreview(response, request, result, err)
}

func (handlers overrideHandlers) sendStageMessage(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.SendStageMessageInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := handlers.service.SendStageMessage(request.Context(), actor, input)
	handlers.writeResult(response, request, result, err)
}

func (handlers overrideHandlers) activateTechnicalDifficulties(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	var input overrides.TechnicalDifficultiesInput
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID = eventID
	result, err := handlers.service.ActivateTechnicalDifficulties(request.Context(), actor, input)
	handlers.writeResult(response, request, result, err)
}

func (handlers overrideHandlers) clearOverride(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, eventID, ok := handlers.commandContext(response, request)
	if !ok {
		return
	}
	overrideID, err := positivePathID(request, "overrideID")
	if err != nil {
		http.Error(response, "Display Override not found", http.StatusNotFound)
		return
	}
	var input overrides.ClearInput
	if err = decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	input.EventID, input.OverrideID = eventID, overrideID
	result, err := handlers.service.Clear(request.Context(), actor, input)
	handlers.writeResult(response, request, result, err)
}

func (handlers overrideHandlers) commandContext(
	response http.ResponseWriter,
	request *http.Request,
) (auth.Account, int, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, 0, false
	}
	actor, err := handlers.authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, 0, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Account session lookup failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, 0, false
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "Event not found", http.StatusNotFound)
		return auth.Account{}, 0, false
	}
	return actor, eventID, true
}

func (handlers overrideHandlers) writeResult(
	response http.ResponseWriter,
	request *http.Request,
	result any,
	err error,
) {
	switch {
	case errors.Is(err, overrides.ErrProducerRequired),
		errors.Is(err, overrides.ErrScopeDenied):
		http.Error(response, "Display Group access denied", http.StatusForbidden)
	case errors.Is(err, overrides.ErrNotFound):
		http.Error(response, "Display Override not found", http.StatusNotFound)
	case errors.Is(err, overrides.ErrRevision),
		errors.Is(err, overrides.ErrConfigurationRevision),
		errors.Is(err, overrides.ErrCommandConflict):
		http.Error(response, "Display Override conflict", http.StatusConflict)
	case errors.Is(err, overrides.ErrInvalidInput),
		errors.Is(err, overrides.ErrEventNotActive),
		errors.Is(err, command.ErrInvalidID):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "Display Override command failed", "error", err)
		http.Error(response, "Display Override unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(response).Encode(result); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Display Override", "error", encodeErr)
			return
		}
		handlers.notify()
	}
}

func (handlers overrideHandlers) writePreview(
	response http.ResponseWriter,
	request *http.Request,
	result overrides.Preview,
	err error,
) {
	switch {
	case errors.Is(err, overrides.ErrScopeDenied):
		http.Error(response, "Display Group access denied", http.StatusForbidden)
	case errors.Is(err, overrides.ErrNotFound):
		http.Error(response, "Display Override not found", http.StatusNotFound)
	case errors.Is(err, overrides.ErrInvalidInput),
		errors.Is(err, overrides.ErrEventNotActive):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "Display Override preview failed", "error", err)
		http.Error(response, "Display Override unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(response).Encode(result); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Display Override preview", "error", encodeErr)
		}
	}
}

func (handlers overrideHandlers) writePreviewList(
	response http.ResponseWriter,
	request *http.Request,
	result []overrides.ActiveOverride,
	err error,
) {
	switch {
	case errors.Is(err, overrides.ErrNotFound):
		http.Error(response, "Display Override not found", http.StatusNotFound)
	case errors.Is(err, overrides.ErrInvalidInput),
		errors.Is(err, overrides.ErrEventNotActive):
		http.Error(response, "invalid request", http.StatusUnprocessableEntity)
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "list Display Overrides failed", "error", err)
		http.Error(response, "Display Override unavailable", http.StatusInternalServerError)
	default:
		response.Header().Set("Content-Type", "application/json")
		if encodeErr := json.NewEncoder(response).Encode(result); encodeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Display Overrides", "error", encodeErr)
		}
	}
}

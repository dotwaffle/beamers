package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/overrides"
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

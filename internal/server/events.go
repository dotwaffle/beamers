package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
)

type eventHandlers struct {
	authentication     *auth.Service
	events             *events.Service
	logger             *slog.Logger
	allowPlaintextCrew bool
}

func registerEventRoutes(
	mux *http.ServeMux,
	authentication *auth.Service,
	eventService *events.Service,
	logger *slog.Logger,
	listenerAddress net.Addr,
) {
	handlers := eventHandlers{
		authentication:     authentication,
		events:             eventService,
		logger:             logger,
		allowPlaintextCrew: listenerIsLoopback(listenerAddress),
	}
	mux.HandleFunc("/admin/events", handlers.createEvent)
	mux.HandleFunc("/admin/accounts", handlers.createAccount)
	mux.HandleFunc("/admin/events/{eventID}/grants", handlers.grantEventAccess)
	mux.HandleFunc("/crew/events/{eventID}", handlers.crewEvent)
}

func (handlers eventHandlers) grantEventAccess(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "Event not found", http.StatusNotFound)
		return
	}
	var input struct {
		AccountID int    `json:"account_id"`
		Role      string `json:"role"`
		CommandID string `json:"command_id"`
	}
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	created, err := handlers.events.GrantProducer(
		request.Context(), actor, eventID, input.AccountID, input.Role, input.CommandID,
	)
	switch {
	case errors.Is(err, events.ErrAdministratorRequired):
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	case errors.Is(err, events.ErrProducerRoleRequired):
		http.Error(response, "role must be Producer", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, events.ErrEventNotFound):
		http.Error(response, "Event not found", http.StatusNotFound)
		return
	case errors.Is(err, events.ErrAccountNotFound):
		http.Error(response, "Account not found", http.StatusNotFound)
		return
	case errors.Is(err, events.ErrEventGrantExists):
		http.Error(response, "Event Grant already exists", http.StatusConflict)
		return
	case errors.Is(err, events.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "Event Grant failed", "error", err)
		http.Error(response, "Event Grant unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(response).Encode(created); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Event Grant", "error", err)
	}
}

func (handlers eventHandlers) crewEvent(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPut {
		handlers.updateCrewEvent(response, request)
		return
	}
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "Event access denied", http.StatusForbidden)
		return
	}
	found, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if errors.Is(err, events.ErrEventAccessDenied) {
		http.Error(response, "Event access denied", http.StatusForbidden)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "crew Event read failed", "error", err)
		http.Error(response, "Event read unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(found); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write crew Event", "error", err)
	}
}

func (handlers eventHandlers) updateCrewEvent(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPut, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil {
		http.Error(response, "Event access denied", http.StatusForbidden)
		return
	}
	var input events.CreateInput
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	updated, err := handlers.events.Update(request.Context(), actor, eventID, input)
	var validation *events.ValidationError
	switch {
	case errors.As(err, &validation):
		if writeErr := writeValidationError(response, validation); writeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Event validation error", "error", writeErr)
		}
		return
	case errors.Is(err, events.ErrEventAccessDenied):
		http.Error(response, "Event access denied", http.StatusForbidden)
		return
	case errors.Is(err, events.ErrRevisionConflict), errors.Is(err, events.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(request.Context(), "crew Event update failed", "error", err)
		http.Error(response, "Event update unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(updated); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write updated Event", "error", err)
	}
}

func positivePathID(request *http.Request, name string) (int, error) {
	id, err := strconv.Atoi(request.PathValue(name))
	if err != nil || id <= 0 {
		return 0, errors.New("invalid path ID")
	}
	return id, nil
}

func (handlers eventHandlers) createAccount(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		handlers.listAccounts(response, request)
		return
	}
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	var input struct {
		Name      string `json:"name"`
		Password  string `json:"password"`
		CommandID string `json:"command_id"`
	}
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	created, err := handlers.authentication.CreateAccount(
		request.Context(), actor, input.Name, input.Password, input.CommandID,
	)
	switch {
	case errors.Is(err, auth.ErrInvalidAccountDetails):
		http.Error(response, "invalid Account details", http.StatusUnprocessableEntity)
		return
	case errors.Is(err, auth.ErrAccountExists):
		http.Error(response, "Account name already exists", http.StatusConflict)
		return
	case errors.Is(err, auth.ErrAdministratorRequired):
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	case errors.Is(err, auth.ErrAuthenticationBusy):
		writeAuthRateLimit(response, time.Second)
		return
	case errors.Is(err, auth.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(
			request.Context(), "Account creation failed", "component", "authentication", "error", err,
		)
		http.Error(response, "Account creation unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(response).Encode(struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		Administrator bool   `json:"administrator"`
	}{created.ID, created.Name, created.Administrator}); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write created Account", "error", err)
	}
}

func (handlers eventHandlers) listAccounts(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodGet, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	found, err := handlers.authentication.ListAccounts(request.Context(), actor)
	if errors.Is(err, auth.ErrAdministratorRequired) {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Account listing failed", "error", err)
		http.Error(response, "Account listing unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	accounts := make([]struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		Administrator bool   `json:"administrator"`
	}, 0, len(found))
	for _, item := range found {
		accounts = append(accounts, struct {
			ID            int    `json:"id"`
			Name          string `json:"name"`
			Administrator bool   `json:"administrator"`
		}{item.ID, item.Name, item.Administrator})
	}
	if err := json.NewEncoder(response).Encode(accounts); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write Account listing", "error", err)
	}
}

func (handlers eventHandlers) createEvent(response http.ResponseWriter, request *http.Request) {
	if !requestAllowed(response, request, http.MethodPost, handlers.allowPlaintextCrew) {
		return
	}
	actor, ok := handlers.authenticate(response, request)
	if !ok {
		return
	}
	var input events.CreateInput
	if decodeErr := decodeAuthJSON(response, request, &input); decodeErr != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	created, err := handlers.events.Create(request.Context(), actor, input)
	var validation *events.ValidationError
	switch {
	case errors.As(err, &validation):
		if writeErr := writeValidationError(response, validation); writeErr != nil {
			handlers.logger.ErrorContext(request.Context(), "write Event validation error", "error", writeErr)
		}
		return
	case errors.Is(err, events.ErrAdministratorRequired):
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return
	case errors.Is(err, events.ErrCommandConflict):
		http.Error(response, err.Error(), http.StatusConflict)
		return
	case err != nil:
		handlers.logger.ErrorContext(
			request.Context(),
			"Event creation failed",
			"component", "events",
			"error", err,
		)
		http.Error(response, "Event creation unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(response).Encode(created); err != nil {
		handlers.logger.ErrorContext(request.Context(), "write created Event", "error", err)
	}
}

func (handlers eventHandlers) authenticate(
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
		handlers.logger.ErrorContext(
			request.Context(),
			"Account session lookup failed",
			"component", "authentication",
			"error", err,
		)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	return actor, true
}

func writeValidationError(response http.ResponseWriter, validation *events.ValidationError) error {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusUnprocessableEntity)
	return json.NewEncoder(response).Encode(struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	}{Field: validation.Field, Message: validation.Message})
}

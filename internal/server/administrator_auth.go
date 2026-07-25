package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/dotwaffle/beamers/internal/auth"
)

func authenticateAdministrator(
	response http.ResponseWriter,
	request *http.Request,
	authentication *auth.Service,
	logger *slog.Logger,
	operation string,
) (auth.Account, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	actor, err := authentication.Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return auth.Account{}, false
	}
	if err != nil {
		logger.ErrorContext(request.Context(), operation+" authentication failed", "error", err)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return auth.Account{}, false
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return auth.Account{}, false
	}
	return actor, true
}

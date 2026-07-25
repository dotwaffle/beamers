package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/operations"
)

func (application *application) serveUpgrade(
	response http.ResponseWriter,
	request *http.Request,
) {
	switch request.URL.Path {
	case "/admin/upgrade":
		application.upgradePreview(response, request)
	case "/admin/upgrade/apply":
		application.upgradeApply(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (application *application) upgradePreview(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(
		response,
		request,
		http.MethodGet,
		listenerIsLoopback(application.config.ListenerAddress),
	) {
		return
	}
	upgrade, _, ok := application.authenticatedUpgrade(response, request)
	if !ok {
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(upgrade.Plan()); err != nil {
		application.config.Logger.ErrorContext(
			request.Context(),
			"write storage upgrade preview",
			"error",
			err,
		)
	}
}

func (application *application) upgradeApply(
	response http.ResponseWriter,
	request *http.Request,
) {
	if !requestAllowed(
		response,
		request,
		http.MethodPost,
		listenerIsLoopback(application.config.ListenerAddress),
	) {
		return
	}
	upgrade, actor, ok := application.authenticatedUpgrade(response, request)
	if !ok {
		return
	}
	var input struct {
		Password                   string `json:"password"`
		ApproveConsequences        bool   `json:"approve_consequences"`
		AcknowledgeNoDownMigration bool   `json:"acknowledge_no_down_migration"`
		ForceLive                  bool   `json:"force_live"`
		Reason                     string `json:"reason"`
		PreviewDigest              string `json:"preview_digest"`
	}
	if err := decodeAuthJSON(response, request, &input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	if err := upgrade.Authentication().Reauthenticate(
		request.Context(),
		actor,
		input.Password,
	); errors.Is(err, auth.ErrAuthenticationFailed) {
		http.Error(response, "reauthentication failed", http.StatusUnauthorized)
		return
	} else if err != nil {
		application.config.Logger.ErrorContext(
			request.Context(),
			"storage upgrade reauthentication failed",
			"error",
			err,
		)
		http.Error(response, "reauthentication unavailable", http.StatusInternalServerError)
		return
	}
	result, err := application.applyPreparedUpgrade(
		request.Context(),
		operations.UpgradeApproval{
			ApproveConsequences:        input.ApproveConsequences,
			AcknowledgeNoDownMigration: input.AcknowledgeNoDownMigration,
			ForceLive:                  input.ForceLive,
			Reason:                     input.Reason,
			ActorAccountID:             actor.ID,
			PreviewDigest:              input.PreviewDigest,
		},
	)
	if err != nil {
		application.config.Logger.ErrorContext(
			request.Context(),
			"storage upgrade failed",
			"error",
			err,
		)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		if encodeErr := json.NewEncoder(response).Encode(struct {
			Error  string                   `json:"error"`
			Result operations.UpgradeResult `json:"result"`
		}{
			Error:  err.Error(),
			Result: result,
		}); encodeErr != nil {
			application.config.Logger.ErrorContext(
				request.Context(),
				"write failed storage upgrade result",
				"error",
				encodeErr,
			)
		}
		return
	}
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(result); err != nil {
		application.config.Logger.ErrorContext(
			request.Context(),
			"write storage upgrade result",
			"error",
			err,
		)
	}
}

func (application *application) authenticatedUpgrade(
	response http.ResponseWriter,
	request *http.Request,
) (*operations.Upgrade, auth.Account, bool) {
	application.mu.Lock()
	upgrade := application.upgrade
	applying := application.upgradeApplying
	application.mu.Unlock()
	if upgrade == nil || applying || upgrade.Authentication() == nil {
		http.Error(response, "upgrade is not available", http.StatusConflict)
		return nil, auth.Account{}, false
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return nil, auth.Account{}, false
	}
	actor, err := upgrade.Authentication().Authenticate(request.Context(), cookie.Value)
	if errors.Is(err, auth.ErrInvalidSession) {
		http.Error(response, "authentication required", http.StatusUnauthorized)
		return nil, auth.Account{}, false
	}
	if err != nil {
		application.config.Logger.ErrorContext(
			request.Context(),
			"storage upgrade authentication failed",
			"error",
			err,
		)
		http.Error(response, "authentication unavailable", http.StatusInternalServerError)
		return nil, auth.Account{}, false
	}
	if !actor.Administrator {
		http.Error(response, "Administrator authority required", http.StatusForbidden)
		return nil, auth.Account{}, false
	}
	return upgrade, actor, true
}

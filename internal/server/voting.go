package server

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/voting"
)

type votingHandlers struct {
	browser *frontendHandlers
	events  *events.Service
	voting  *voting.Service
	limiter *authFailureLimiter
}

func registerVotingRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	votingService *voting.Service,
	limiter *authFailureLimiter,
	logger *slog.Logger,
) {
	handlers := votingHandlers{
		browser: &frontendHandlers{
			authentication: authentication, limiter: limiter, logger: logger, random: rand.Reader,
		},
		events: eventService, voting: votingService, limiter: limiter,
	}
	redemptionRoute := browserPageRoute()
	redemptionRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/voting", redemptionRoute, handlers.redeem)
	backstageRoute := backstagePageRoute()
	backstageRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc(
		"/backstage/events/{eventID}/voting-keys",
		backstageRoute,
		handlers.keys,
	)
}

func (handlers votingHandlers) redeem(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderRedemption(
			response, request, actor, csrfToken, 0, http.StatusOK, "", false,
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		eventID, parseErr := strconv.Atoi(request.Form.Get("event_id"))
		token := request.Form.Get("voting_key")
		if parseErr != nil || eventID <= 0 {
			handlers.renderRedemption(
				response, request, actor, csrfToken, 0,
				http.StatusUnprocessableEntity, unavailableVotingKeyMessage, false,
			)
			return
		}
		clientKey, tokenKey := votingKeyFailureKeys(request, token)
		if retryAfter, blocked := handlers.limiter.reserve(clientKey, tokenKey); blocked {
			writeAuthRateLimit(response, retryAfter)
			return
		}
		err = handlers.voting.Redeem(
			request.Context(), actor, eventID, token, request.Form.Get("command_id"),
		)
		switch {
		case errors.Is(err, voting.ErrKeyUnavailable),
			errors.Is(err, voting.ErrAlreadyEligible),
			errors.Is(err, voting.ErrInvalidInput):
			handlers.renderRedemption(
				response, request, actor, csrfToken, eventID,
				http.StatusUnprocessableEntity, unavailableVotingKeyMessage, false,
			)
		case err != nil:
			handlers.limiter.release(clientKey, tokenKey)
			handlers.browser.frontendError(response, request, "redeem Voting Key", err)
		default:
			handlers.limiter.release(clientKey, tokenKey)
			handlers.renderRedemption(
				response, request, actor, csrfToken, eventID,
				http.StatusOK, "Voting Eligibility granted.", true,
			)
		}
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

const unavailableVotingKeyMessage = "Voting Key unavailable. Check the Event and key."

func (handlers votingHandlers) renderRedemption(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	eventID, status int,
	message string,
	success bool,
) {
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Voting command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.VotingRedemption(
		frontend.VotingRedemptionPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Backstage: backstageAccessible(request) &&
				backstageAvailable(backstageNavigation(actor)),
			CommandID: commandID, EventID: eventID, Message: message, Success: success,
		},
	))
}

func (handlers votingHandlers) keys(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := strconv.Atoi(request.PathValue("eventID"))
	if err != nil || eventID <= 0 || !actor.CanProduceEvent(eventID) {
		http.NotFound(response, request)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderKeys(response, request, actor, csrfToken, eventID, nil, http.StatusOK, "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submitKeys(response, request, actor, csrfToken, eventID)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers votingHandlers) submitKeys(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	eventID int,
) {
	var (
		issued []voting.IssuedKey
		err    error
	)
	switch request.Form.Get("action") {
	case "issue":
		count, countErr := strconv.Atoi(request.Form.Get("count"))
		foundEvent, eventErr := handlers.events.CrewEvent(request.Context(), actor, eventID)
		if countErr != nil || eventErr != nil {
			err = voting.ErrInvalidInput
			break
		}
		location, locationErr := time.LoadLocation(foundEvent.Timezone)
		if locationErr != nil {
			err = voting.ErrInvalidInput
			break
		}
		expiresAt, expiryErr := time.ParseInLocation(
			"2006-01-02T15:04", request.Form.Get("expires_at"), location,
		)
		if expiryErr != nil {
			err = voting.ErrInvalidInput
			break
		}
		issued, err = handlers.voting.Issue(request.Context(), actor, voting.IssueInput{
			EventID: eventID, Count: count, ExpiresAt: expiresAt,
			CommandID: request.Form.Get("command_id"),
		})
	case "revoke":
		keyID, parseErr := strconv.Atoi(request.Form.Get("key_id"))
		if parseErr != nil {
			err = voting.ErrInvalidInput
			break
		}
		_, err = handlers.voting.Revoke(
			request.Context(), actor, eventID, keyID, request.Form.Get("command_id"),
		)
	default:
		err = voting.ErrInvalidInput
	}
	if err != nil {
		status, message := votingManagementError(err)
		handlers.renderKeys(response, request, actor, csrfToken, eventID, nil, status, message)
		return
	}
	handlers.renderKeys(response, request, actor, csrfToken, eventID, issued, http.StatusOK, "")
}

func (handlers votingHandlers) renderKeys(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	eventID int,
	issued []voting.IssuedKey,
	status int,
	message string,
) {
	foundEvent, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	keys, err := handlers.voting.Keys(request.Context(), actor, eventID)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Voting Key command identity", err)
		return
	}
	now := time.Now()
	location, err := time.LoadLocation(foundEvent.Timezone)
	if err != nil {
		handlers.browser.frontendError(response, request, "load Event timezone", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.VotingKeys(frontend.VotingKeyPage{
		AccountName: actor.Name, CSRFToken: csrfToken,
		ReducedEffects: reducedEffectsCookie(request), Navigation: backstageNavigation(actor),
		CommandID: commandID, Event: foundEvent,
		ExpiryValue: now.Add(24 * time.Hour).In(location).Format("2006-01-02T15:04"),
		Keys:        keys, Issued: issued, Message: message, Now: now,
	}))
}

func votingManagementError(err error) (int, string) {
	switch {
	case errors.Is(err, voting.ErrInvalidInput):
		return http.StatusUnprocessableEntity, "Check the Voting Key request and try again."
	case errors.Is(err, voting.ErrKeysAlreadyIssued):
		return http.StatusConflict, "These Voting Keys were already issued and cannot be shown again."
	case errors.Is(err, voting.ErrKeyUnavailable):
		return http.StatusUnprocessableEntity, "Voting Key unavailable."
	case errors.Is(err, voting.ErrProducerRequired):
		return http.StatusNotFound, "Event not found."
	default:
		return http.StatusInternalServerError, "Voting Key action failed."
	}
}

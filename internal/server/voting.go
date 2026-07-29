package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/voting"
)

type votingHandlers struct {
	browser *frontendHandlers
	events  *events.Service
	voting  *voting.Service
	limiter *authFailureLimiter
	stream  *displaystream.Hub
}

func registerVotingRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	votingService *voting.Service,
	limiter *authFailureLimiter,
	stream *displaystream.Hub,
	logger *slog.Logger,
) {
	handlers := votingHandlers{
		browser: &frontendHandlers{
			authentication: authentication, limiter: limiter, logger: logger, random: rand.Reader,
		},
		events: eventService, voting: votingService, limiter: limiter, stream: stream,
	}
	redemptionRoute := browserPageRoute()
	redemptionRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc("/voting", redemptionRoute, handlers.redeem)
	mux.HandleFunc("/voting/{sessionID}", redemptionRoute, handlers.ballot)
	streamRoute := browserPageRoute()
	streamRoute.persistent = true
	mux.HandleFunc("/voting/{sessionID}/events", streamRoute, handlers.ballotEvents)
	backstageRoute := backstagePageRoute()
	backstageRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc(
		"/backstage/events/{eventID}/voting-keys",
		backstageRoute,
		handlers.keys,
	)
	mux.HandleFunc(
		"/backstage/events/{eventID}/competitions/{sessionID}/voting",
		backstageRoute,
		handlers.competition,
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
	ballots, err := handlers.voting.OpenBallots(request.Context(), actor)
	if err != nil {
		handlers.browser.frontendError(response, request, "list open Ballots", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.VotingRedemption(
		frontend.VotingRedemptionPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Backstage: backstageAccessible(request) &&
				backstageAvailable(backstageNavigation(actor, "")),
			CommandID: commandID, EventID: eventID, Message: message, Success: success,
			Errors:      redemptionErrors(message, success),
			OpenBallots: ballots,
		},
	))
}

func redemptionErrors(message string, success bool) frontend.FormErrors {
	if message == "" || success {
		return nil
	}
	return frontend.FormErrors{
		{FieldID: "voting-event", Label: "Event number", Message: message},
		{FieldID: "voting-key", Label: "Voting Key", Message: message},
	}
}

func (handlers votingHandlers) ballot(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	sessionID, sessionErr := positivePathID(request, "sessionID")
	eventID, eventErr := strconv.Atoi(request.URL.Query().Get("event_id"))
	if sessionErr != nil || eventErr != nil || eventID <= 0 {
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
		handlers.renderBallot(
			response, request, actor, csrfToken, eventID, sessionID,
			http.StatusOK, 0, 0, "", false,
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		entryID, entryErr := strconv.Atoi(request.Form.Get("entry_id"))
		value, valueErr := strconv.Atoi(request.Form.Get("value"))
		revision, revisionErr := strconv.Atoi(request.Form.Get("expected_revision"))
		if entryErr != nil || valueErr != nil || revisionErr != nil {
			handlers.renderBallot(
				response, request, actor, csrfToken, eventID, sessionID,
				http.StatusUnprocessableEntity, entryID, value,
				"Check the Vote and try again.", valueErr != nil || value < 1 || value > 5,
			)
			return
		}
		_, err = handlers.voting.Vote(request.Context(), actor, voting.VoteInput{
			EventID: eventID, SessionID: sessionID, EntryID: entryID,
			Value: value, ExpectedRevision: revision, CommandID: request.Form.Get("command_id"),
		})
		if err != nil {
			status, message := ballotError(err)
			if status == http.StatusNotFound || status == http.StatusInternalServerError {
				http.Error(response, message, status)
				return
			}
			handlers.renderBallot(
				response, request, actor, csrfToken, eventID, sessionID,
				status, entryID, value, message,
				errors.Is(err, voting.ErrInvalidInput) && (value < 1 || value > 5),
			)
			return
		}
		handlers.stream.Notify()
		http.Redirect(
			response, request,
			"/voting/"+strconv.Itoa(sessionID)+"?event_id="+strconv.Itoa(eventID),
			http.StatusSeeOther,
		)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers votingHandlers) renderBallot(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	eventID, sessionID int,
	status int,
	errorEntryID, submittedValue int,
	message string,
	fieldFailure bool,
) {
	cursor := handlers.stream.Cursor()
	ballot, err := handlers.voting.Ballot(request.Context(), actor, eventID, sessionID)
	if err != nil {
		errorStatus, errorMessage := ballotError(err)
		http.Error(response, errorMessage, errorStatus)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Vote command identity", err)
		return
	}
	formErrors := frontend.FormErrors(nil)
	if message != "" && fieldFailure && ballotContainsEntry(ballot, errorEntryID) {
		formErrors = frontend.FormErrors{{
			FieldID: "vote-" + strconv.Itoa(errorEntryID),
			Label:   "Score",
			Message: message,
		}}
	}
	if message != "" && len(formErrors) == 0 {
		formErrors = frontend.FormErrors{{Message: message}}
	}
	handlers.browser.render(response, request, status, frontend.VotingBallot(
		frontend.VotingBallotPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Backstage:      backstageAccessible(request) && backstageAvailable(backstageNavigation(actor, "")),
			CommandID:      commandID, Ballot: ballot,
			Errors: formErrors, ErrorEntryID: errorEntryID, SubmittedValue: submittedValue,
			StreamID: cursor.StreamID, StreamPosition: cursor.Position,
		},
	))
}

func ballotContainsEntry(ballot voting.Ballot, entryID int) bool {
	for _, entry := range ballot.Entries {
		if entry.ID == entryID && entry.CanVote {
			return true
		}
	}
	return false
}

func ballotError(err error) (int, string) {
	switch {
	case errors.Is(err, voting.ErrVotingRevision):
		return http.StatusConflict, "The Ballot changed. Review the latest Entries and try again."
	case errors.Is(err, voting.ErrInvalidInput):
		return http.StatusUnprocessableEntity, "Check the Vote and try again."
	case errors.Is(err, voting.ErrVotingIneligible),
		errors.Is(err, voting.ErrVoteUnavailable),
		errors.Is(err, voting.ErrWindowUnavailable):
		return http.StatusNotFound, "Ballot not found."
	default:
		return http.StatusInternalServerError, "Ballot unavailable."
	}
}

func (handlers votingHandlers) ballotEvents(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		frontendMethodNotAllowed(response, http.MethodGet)
		return
	}
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	sessionID, sessionErr := positivePathID(request, "sessionID")
	eventID, eventErr := strconv.Atoi(request.URL.Query().Get("event_id"))
	if sessionErr != nil || eventErr != nil || eventID <= 0 {
		http.NotFound(response, request)
		return
	}
	if _, err := handlers.voting.Ballot(
		request.Context(), actor, eventID, sessionID,
	); err != nil {
		http.NotFound(response, request)
		return
	}
	after, err := scheduleStreamPosition(request)
	if err != nil {
		http.Error(response, "invalid Ballot stream position", http.StatusBadRequest)
		return
	}
	cursor := handlers.stream.Cursor()
	streamChanged := request.URL.Query().Get("stream_id") != cursor.StreamID
	unknownPosition := after > cursor.Position
	incompatible := streamChanged || unknownPosition
	if incompatible {
		after = cursor.Position
	}
	subscription := handlers.stream.Subscribe(after)
	defer subscription.Close()
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err = writeDisplayHeartbeat(response); err != nil {
		return
	}
	if incompatible {
		if err = writeBallotInvalidation(response, cursor); err != nil {
			return
		}
	}
	heartbeats := time.NewTicker(displayHeartbeatInterval)
	defer heartbeats.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case notification, open := <-subscription.Notifications:
			if !open {
				return
			}
			if writeBallotInvalidation(response, notification) != nil {
				return
			}
		case <-heartbeats.C:
			if writeDisplayHeartbeat(response) != nil {
				return
			}
		}
	}
}

func writeBallotInvalidation(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
) error {
	return writeDisplaySSE(response, fmt.Sprintf(
		"id: %d\nevent: ballot\ndata: refresh\n\n",
		cursor.Position,
	))
}

func (handlers votingHandlers) competition(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, eventErr := positivePathID(request, "eventID")
	sessionID, sessionErr := positivePathID(request, "sessionID")
	if eventErr != nil || sessionErr != nil {
		http.NotFound(response, request)
		return
	}
	if _, crew := actor.EventRoles[eventID]; !crew {
		http.NotFound(response, request)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	message := ""
	status := http.StatusOK
	if request.Method == http.MethodPost {
		if !handlers.browser.validForm(response, request) {
			return
		}
		if !actor.CanProduceEvent(eventID) {
			http.NotFound(response, request)
			return
		}
		err = handlers.submitCompetition(request, actor, eventID, sessionID)
		if err == nil {
			handlers.stream.Notify()
			http.Redirect(
				response, request,
				"/backstage/events/"+strconv.Itoa(eventID)+"/competitions/"+
					strconv.Itoa(sessionID)+"/voting",
				http.StatusSeeOther,
			)
			return
		}
		status, message = ballotError(err)
	} else if request.Method != http.MethodGet && request.Method != http.MethodHead {
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		return
	}
	handlers.renderCompetition(
		response, request, actor, csrfToken, eventID, sessionID, status, message,
	)
}

func (handlers votingHandlers) submitCompetition(
	request *http.Request,
	actor auth.Account,
	eventID, sessionID int,
) error {
	revision, err := strconv.Atoi(request.Form.Get("expected_revision"))
	if err != nil || revision < 0 {
		return voting.ErrInvalidInput
	}
	switch request.Form.Get("action") {
	case "configure":
		method, policy := "", ""
		if request.Form.Get("method_override") == "true" {
			method = request.Form.Get("voting_method")
		}
		if request.Form.Get("self_vote_override") == "true" {
			policy = request.Form.Get("self_vote_policy")
		}
		_, err = handlers.voting.Configure(request.Context(), actor, voting.ConfigureInput{
			EventID: eventID, SessionID: sessionID, ExpectedRevision: revision,
			MethodOverride: method, SelfVoteOverride: policy,
			CommandID: request.Form.Get("command_id"),
		})
	case "open":
		_, err = handlers.voting.Open(request.Context(), actor, voting.WindowInput{
			EventID: eventID, SessionID: sessionID, ExpectedRevision: revision,
			CommandID: request.Form.Get("command_id"),
		})
	case "close":
		_, err = handlers.voting.Close(request.Context(), actor, voting.WindowInput{
			EventID: eventID, SessionID: sessionID, ExpectedRevision: revision,
			CommandID: request.Form.Get("command_id"),
		})
	default:
		err = voting.ErrInvalidInput
	}
	return err
}

func (handlers votingHandlers) renderCompetition(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken string,
	eventID, sessionID, status int,
	message string,
) {
	window, err := handlers.voting.Window(request.Context(), actor, eventID, sessionID)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	participation, err := handlers.voting.Participation(
		request.Context(), actor, eventID, sessionID,
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Voting participation", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Voting command identity", err)
		return
	}
	handlers.browser.render(response, request, status, frontend.CompetitionVoting(
		frontend.CompetitionVotingPage{
			AccountName: actor.Name, CSRFToken: csrfToken,
			ReducedEffects: reducedEffectsCookie(request),
			Navigation:     backstageNavigation(actor, request.URL.Path),
			Producer:       actor.CanProduceEvent(eventID), CommandID: commandID,
			Window: window, Participation: participation, Message: message,
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
		ReducedEffects: reducedEffectsCookie(request),
		Navigation:     backstageNavigation(actor, request.URL.Path),
		CommandID:      commandID, Event: foundEvent,
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

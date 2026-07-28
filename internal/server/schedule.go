package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/schedule"
)

type scheduleHandlers struct {
	frontend     frontendHandlers
	eventService *events.Service
	schedule     *schedule.Service
	stream       *displaystream.Hub
	metrics      *scheduleStreamMetrics
	logger       *slog.Logger
}

type scheduleStreamMetrics struct {
	connections            atomic.Uint64
	gapRecoveries          atomic.Uint64
	incompatibleRecoveries atomic.Uint64
	resnapshots            atomic.Uint64
	slowDrops              atomic.Uint64
	disconnects            atomic.Uint64
}

type scheduleStreamMetricSnapshot struct {
	Connections            uint64
	GapRecoveries          uint64
	IncompatibleRecoveries uint64
	Resnapshots            uint64
	SlowDrops              uint64
	Disconnects            uint64
}

func (metrics *scheduleStreamMetrics) snapshot() scheduleStreamMetricSnapshot {
	return scheduleStreamMetricSnapshot{
		Connections:            metrics.connections.Load(),
		GapRecoveries:          metrics.gapRecoveries.Load(),
		IncompatibleRecoveries: metrics.incompatibleRecoveries.Load(),
		Resnapshots:            metrics.resnapshots.Load(),
		SlowDrops:              metrics.slowDrops.Load(),
		Disconnects:            metrics.disconnects.Load(),
	}
}

func registerScheduleRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	service *schedule.Service,
	stream *displaystream.Hub,
	metrics *scheduleStreamMetrics,
	logger *slog.Logger,
) {
	handlers := scheduleHandlers{
		frontend: frontendHandlers{
			authentication: authentication, logger: logger, random: rand.Reader,
		},
		eventService: eventService,
		schedule:     service,
		stream:       stream,
		metrics:      metrics,
		logger:       logger,
	}
	mux.HandleFunc("/schedule", browserPageRoute(), handlers.list)
	mux.HandleFunc("/events/{slug}/schedule", browserPageRoute(), handlers.eventSchedule)
	mux.HandleFunc(
		"/events/{slug}/competitions",
		browserPageRoute(),
		handlers.eventCompetitions,
	)
	mux.HandleFunc(
		"/events/{slug}/competitions/{sessionID}",
		browserPageRoute(),
		handlers.eventCompetition,
	)
	mux.HandleFunc("/my-schedule", browserPageRoute(), handlers.mySchedule)
	mux.HandleFunc(
		"/schedule/events",
		routeContract{kind: publicInterface, persistent: true},
		handlers.events,
	)
	mux.HandleFunc("/schedule/sessions/{sessionID}", browserPageRoute(), handlers.session)
	mux.HandleFunc(
		"/events/{slug}/schedule/sessions/{sessionID}",
		browserPageRoute(),
		handlers.eventSession,
	)
	favoriteRoute := browserPageRoute()
	favoriteRoute.maxBodyBytes = maxAuthBodyBytes
	mux.HandleFunc(
		"/schedule/sessions/{sessionID}/favorite",
		favoriteRoute,
		handlers.favorite,
	)
	mux.HandleFunc("/assets/schedule.css", publicRoute(), handlers.stylesheet)
	mux.HandleFunc("/assets/schedule.js", publicRoute(), handlers.script)
}

func (handlers scheduleHandlers) list(response http.ResponseWriter, request *http.Request) {
	handlers.schedulePage(response, request, false, nil, false)
}

func (handlers scheduleHandlers) mySchedule(response http.ResponseWriter, request *http.Request) {
	handlers.schedulePage(response, request, true, nil, false)
}

func (handlers scheduleHandlers) eventSchedule(
	response http.ResponseWriter,
	request *http.Request,
) {
	event, ok := handlers.publicEvent(response, request, "/schedule")
	if ok {
		handlers.schedulePage(response, request, false, &event, false)
	}
}

func (handlers scheduleHandlers) eventCompetitions(
	response http.ResponseWriter,
	request *http.Request,
) {
	event, ok := handlers.publicEvent(response, request, "/competitions")
	if ok {
		handlers.schedulePage(response, request, false, &event, true)
	}
}

func (handlers scheduleHandlers) eventCompetition(
	response http.ResponseWriter,
	request *http.Request,
) {
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		http.NotFound(response, request)
		return
	}
	event, ok := handlers.publicEvent(
		response,
		request,
		"/competitions/"+strconv.Itoa(sessionID),
	)
	if !ok {
		return
	}
	account, signedIn, ok := handlers.account(
		response,
		request,
		false,
		request.URL.RequestURI(),
	)
	if !ok {
		return
	}
	snapshot, session, found, err := handlers.schedule.FindCompetition(
		request.Context(),
		sessionID,
		event.Slug,
	)
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "public Competition read failed", "error", err)
		http.Error(response, "Competitions unavailable", http.StatusInternalServerError)
		return
	}
	if !found || snapshot.EventID != event.ID {
		http.NotFound(response, request)
		return
	}
	if signedIn {
		snapshot.AccountName = account.Name
	}
	snapshot.ReducedEffects = reducedEffectsCookie(request)
	handlers.render(
		response,
		request,
		snapshot.ETag,
		signedIn || reducedEffectsPreferenceCookie(request),
		schedule.CompetitionPage(snapshot, session), //nolint:contextcheck // Generated templ closures receive context when rendered.
		"public Competition",
	)
}

func (handlers scheduleHandlers) schedulePage(
	response http.ResponseWriter,
	request *http.Request,
	favoritesOnly bool,
	event *events.PublicEvent,
	competitions bool,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	account, signedIn, ok := handlers.account(response, request, favoritesOnly, request.URL.RequestURI())
	if !ok {
		return
	}
	filter := schedule.Filter{}
	var err error
	if !competitions {
		filter, err = publicScheduleFilter(request)
		if err != nil {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
	}
	snapshot, err := currentScheduleSnapshot(
		request.Context(),
		handlers.stream,
		func(ctx context.Context) (schedule.Snapshot, error) {
			if signedIn {
				return handlers.schedule.Personalized(ctx, filter, account.ID, favoritesOnly)
			}
			return handlers.schedule.Current(ctx, filter)
		},
	)
	if err != nil {
		if errors.Is(err, schedule.ErrInvalidFilter) {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "public Schedule read failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if request.Header.Get("HX-Request") == "true" ||
		strings.Contains(request.Header.Get("Cache-Control"), "no-cache") {
		handlers.metrics.resnapshots.Add(1)
	}
	unavailable := event != nil && snapshot.EventID != event.ID
	if event != nil {
		if unavailable {
			snapshot = schedule.Snapshot{
				EventID: event.ID, EventName: event.Name, EventSlug: event.Slug,
				Language: event.EventLocale, Locale: event.EventLocale,
				ETag: fmt.Sprintf(`"event-%d-schedule-unavailable"`, event.ID),
			}
		} else {
			snapshot.EventSlug = event.Slug
			for index := range snapshot.Sessions {
				snapshot.Sessions[index].EventSlug = event.Slug
			}
			for dayIndex := range snapshot.Days {
				for sessionIndex := range snapshot.Days[dayIndex].Sessions {
					snapshot.Days[dayIndex].Sessions[sessionIndex].EventSlug = event.Slug
				}
			}
		}
	}
	if signedIn {
		snapshot.AccountName = account.Name
		snapshot.CSRFToken, err = handlers.frontend.csrfToken(response, request)
		if err != nil {
			handlers.frontend.frontendError(response, request, "create CSRF proof", err)
			return
		}
	}
	snapshot.ReducedEffects = reducedEffectsCookie(request)
	if unavailable {
		section := "Schedule"
		if competitions {
			section = "Competitions"
		}
		handlers.render(
			response,
			request,
			snapshot.ETag,
			signedIn || reducedEffectsPreferenceCookie(request),
			schedule.UnavailablePage(snapshot, section), //nolint:contextcheck // Generated templ closures receive context when rendered.
			"unavailable public Schedule",
		)
		return
	}
	component := schedule.Page(snapshot) //nolint:contextcheck // Generated templ closures receive context when rendered.
	name := "public Schedule"
	if competitions {
		sessions := snapshot.Sessions[:0]
		for _, session := range snapshot.Sessions {
			if session.Type == "Competition" {
				sessions = append(sessions, session)
			}
		}
		snapshot.Sessions = sessions
		component = schedule.CompetitionsPage(snapshot) //nolint:contextcheck // Generated templ closures receive context when rendered.
		name = "public Competitions"
	}
	handlers.render(
		response,
		request,
		snapshot.ETag,
		signedIn || reducedEffectsPreferenceCookie(request),
		component,
		name,
	)
}

func currentScheduleSnapshot(
	ctx context.Context,
	stream *displaystream.Hub,
	load func(context.Context) (schedule.Snapshot, error),
) (schedule.Snapshot, error) {
	cursor := stream.Cursor()
	snapshot, err := load(ctx)
	if err != nil {
		return schedule.Snapshot{}, err
	}
	snapshot.StreamID = cursor.StreamID
	snapshot.StreamPosition = cursor.Position
	return snapshot, nil
}

func (handlers scheduleHandlers) events(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cursor := handlers.stream.Cursor()
	after, err := scheduleStreamPosition(request)
	if err != nil {
		http.Error(response, "invalid Schedule stream position", http.StatusBadRequest)
		return
	}
	streamChanged := request.URL.Query().Get("stream_id") != cursor.StreamID
	unknownPosition := after > cursor.Position
	incompatible := streamChanged || unknownPosition
	if incompatible {
		handlers.metrics.incompatibleRecoveries.Add(1)
		after = cursor.Position
	} else if after < cursor.Position {
		handlers.metrics.gapRecoveries.Add(1)
	}
	subscription := handlers.stream.Subscribe(after)
	defer subscription.Close()
	handlers.metrics.connections.Add(1)

	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	if err = writeDisplayHeartbeat(response); err != nil {
		handlers.metrics.disconnects.Add(1)
		return
	}
	if incompatible {
		if err = writeScheduleInvalidation(response, cursor); err != nil {
			handlers.metrics.disconnects.Add(1)
			return
		}
	}
	heartbeats := time.NewTicker(displayHeartbeatInterval)
	defer heartbeats.Stop()
	for {
		select {
		case <-request.Context().Done():
			handlers.metrics.disconnects.Add(1)
			return
		case notification, open := <-subscription.Notifications:
			if !open {
				handlers.metrics.slowDrops.Add(1)
				return
			}
			if writeScheduleInvalidation(response, notification) != nil {
				handlers.metrics.disconnects.Add(1)
				return
			}
		case <-heartbeats.C:
			if writeDisplayHeartbeat(response) != nil {
				handlers.metrics.disconnects.Add(1)
				return
			}
		}
	}
}

func scheduleStreamPosition(request *http.Request) (uint64, error) {
	value := request.Header.Get("Last-Event-ID")
	if value == "" {
		value = request.URL.Query().Get("after")
	}
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func writeScheduleInvalidation(
	response http.ResponseWriter,
	cursor displaystream.Cursor,
) error {
	return writeDisplaySSE(response, fmt.Sprintf(
		"id: %d\nevent: schedule\ndata: refresh\n\n",
		cursor.Position,
	))
}

func (handlers scheduleHandlers) session(response http.ResponseWriter, request *http.Request) {
	handlers.sessionPage(response, request, nil)
}

func (handlers scheduleHandlers) eventSession(
	response http.ResponseWriter,
	request *http.Request,
) {
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		publicSessionNotFound(response)
		return
	}
	event, ok := handlers.publicEvent(
		response,
		request,
		"/schedule/sessions/"+strconv.Itoa(sessionID),
	)
	if ok {
		handlers.sessionPage(response, request, &event)
	}
}

func (handlers scheduleHandlers) sessionPage(
	response http.ResponseWriter,
	request *http.Request,
	event *events.PublicEvent,
) {
	if !publicMethodAllowed(response, request) {
		return
	}
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		publicSessionNotFound(response)
		return
	}
	account, signedIn, proceed := handlers.account(
		response,
		request,
		false,
		request.URL.RequestURI(),
	)
	if !proceed {
		return
	}
	var snapshot schedule.Snapshot
	var session schedule.Session
	var ok bool
	if signedIn {
		snapshot, session, ok, err = handlers.schedule.FindPersonalized(
			request.Context(),
			sessionID,
			request.URL.Query().Get("time_zone"),
			account.ID,
		)
	} else {
		snapshot, session, ok, err = handlers.schedule.Find(
			request.Context(), sessionID, request.URL.Query().Get("time_zone"),
		)
	}
	if err != nil {
		if errors.Is(err, schedule.ErrInvalidFilter) {
			http.Error(response, "invalid Schedule filters", http.StatusBadRequest)
			return
		}
		handlers.logger.ErrorContext(request.Context(), "public Session read failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if !ok {
		publicSessionNotFound(response)
		return
	}
	if event != nil {
		if snapshot.EventID != event.ID {
			publicSessionNotFound(response)
			return
		}
		snapshot.EventSlug = event.Slug
		session.EventSlug = event.Slug
	}
	if signedIn {
		snapshot.AccountName = account.Name
		snapshot.CSRFToken, err = handlers.frontend.csrfToken(response, request)
		if err != nil {
			handlers.frontend.frontendError(response, request, "create CSRF proof", err)
			return
		}
	}
	snapshot.ReducedEffects = reducedEffectsCookie(request)
	handlers.render(
		response,
		request,
		snapshot.ETag,
		signedIn || reducedEffectsPreferenceCookie(request),
		schedule.SessionPage(snapshot, session), //nolint:contextcheck // Generated templ closures receive context when rendered.
		"public Session",
	)
}

func (handlers scheduleHandlers) publicEvent(
	response http.ResponseWriter,
	request *http.Request,
	suffix string,
) (events.PublicEvent, bool) {
	if !publicMethodAllowed(response, request) {
		return events.PublicEvent{}, false
	}
	found, alias, err := handlers.eventService.PublicEvent(
		request.Context(),
		request.PathValue("slug"),
	)
	if errors.Is(err, events.ErrEventNotFound) {
		http.NotFound(response, request)
		return events.PublicEvent{}, false
	}
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Event", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return events.PublicEvent{}, false
	}
	if alias {
		location := "/events/" + found.Slug + suffix
		if request.URL.RawQuery != "" {
			location += "?" + request.URL.RawQuery
		}
		http.Redirect( //nolint:gosec // The Event Slug is validated and suffix is fixed or a parsed positive ID.
			response,
			request,
			location,
			http.StatusFound,
		)
		return events.PublicEvent{}, false
	}
	return found, true
}

func (handlers scheduleHandlers) favorite(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		frontendMethodNotAllowed(response, http.MethodPost)
		return
	}
	sessionID, err := positivePathID(request, "sessionID")
	if err != nil {
		publicSessionNotFound(response)
		return
	}
	sessionPath := "/schedule/sessions/" + strconv.Itoa(sessionID)
	account, _, ok := handlers.account(response, request, true, sessionPath)
	if !ok {
		return
	}
	if !handlers.frontend.validForm(response, request) {
		return
	}
	var favorite bool
	switch request.Form.Get("favorite") {
	case "true":
		favorite = true
	case "false":
	default:
		http.Error(response, "invalid Favorite Session action", http.StatusBadRequest)
		return
	}
	if err = handlers.schedule.SetFavorite(
		request.Context(),
		account.ID,
		sessionID,
		favorite,
	); errors.Is(err, schedule.ErrSessionUnavailable) {
		publicSessionNotFound(response)
		return
	} else if err != nil {
		handlers.logger.ErrorContext(request.Context(), "Favorite Session update failed", "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	returnTo := safeFrontendReturnTo(request.Form.Get("return_to"), sessionPath)
	if request.Header.Get("HX-Request") == "true" {
		returnURL, _ := url.ParseRequestURI(returnTo)
		if !favorite && returnURL.Path == "/my-schedule" {
			setScheduleHeaders(response, "", true)
			response.Header().Set(
				"HX-Retarget",
				"#schedule-session-card-"+strconv.Itoa(sessionID),
			)
			response.Header().Set("HX-Reswap", "delete")
			response.WriteHeader(http.StatusOK)
			return
		}
		handlers.render(
			response,
			request,
			"",
			true,
			schedule.FavoriteControl( //nolint:contextcheck // Generated templ closures receive context when rendered.
				schedule.Snapshot{
					AccountName: account.Name,
					CSRFToken:   request.Form.Get("csrf_token"),
				},
				schedule.Session{ID: sessionID, Favorite: favorite},
				returnTo,
			),
			"Favorite Session control",
		)
		return
	}
	//nolint:gosec // The helper accepts only same-origin absolute paths.
	http.Redirect(
		response,
		request,
		returnTo,
		http.StatusSeeOther,
	)
}

func (handlers scheduleHandlers) account(
	response http.ResponseWriter,
	request *http.Request,
	required bool,
	returnTo string,
) (auth.Account, bool, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		if required {
			http.Redirect(
				response,
				request,
				"/sign-in?return_to="+url.QueryEscape(returnTo),
				http.StatusSeeOther,
			)
			return auth.Account{}, false, false
		}
		return auth.Account{}, false, true
	}
	found, err := handlers.frontend.authentication.Authenticate(request.Context(), cookie.Value)
	switch {
	case err == nil:
		return found, true, true
	case errors.Is(err, auth.ErrInvalidSession):
		clearSessionCookie(response, request)
		if required {
			http.Redirect(
				response,
				request,
				"/sign-in?return_to="+url.QueryEscape(returnTo),
				http.StatusSeeOther,
			)
			return auth.Account{}, false, false
		}
		return auth.Account{}, false, true
	default:
		handlers.frontend.frontendError(response, request, "authenticate Schedule session", err)
		return auth.Account{}, false, false
	}
}

func publicScheduleFilter(request *http.Request) (schedule.Filter, error) {
	locationID, err := optionalPositiveQueryID(request, "location")
	if err != nil {
		return schedule.Filter{}, err
	}
	laneID, err := optionalPositiveQueryID(request, "lane")
	if err != nil {
		return schedule.Filter{}, err
	}
	trackID, err := optionalPositiveQueryID(request, "track")
	if err != nil {
		return schedule.Filter{}, err
	}
	return schedule.Filter{
		Day: request.URL.Query().Get("day"), LocationID: locationID,
		LaneID: laneID, TrackID: trackID,
		ViewerTimezone: request.URL.Query().Get("time_zone"),
	}, nil
}

func optionalPositiveQueryID(request *http.Request, name string) (int, error) {
	value := request.URL.Query().Get(name)
	if value == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, schedule.ErrInvalidFilter
	}
	return id, nil
}

func (handlers scheduleHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	etag string,
	private bool,
	component templ.Component,
	name string,
) {
	setScheduleHeaders(response, etag, private)
	if !private && scheduleNotModified(response, request, etag) {
		return
	}
	var content bytes.Buffer
	if err := component.Render(request.Context(), &content); err != nil {
		handlers.logger.ErrorContext(request.Context(), "render "+name, "error", err)
		http.Error(response, "Schedule unavailable", http.StatusInternalServerError)
		return
	}
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content.Bytes())
}

func publicSessionNotFound(response http.ResponseWriter) {
	http.Error(response, "Session not found", http.StatusNotFound)
}

func (handlers scheduleHandlers) stylesheet(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	content, err := schedule.Stylesheet()
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Schedule stylesheet", "error", err)
		http.Error(response, "stylesheet unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=3600")
	response.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content)
}

func (handlers scheduleHandlers) script(response http.ResponseWriter, request *http.Request) {
	if !publicMethodAllowed(response, request) {
		return
	}
	content, err := schedule.Script()
	if err != nil {
		handlers.logger.ErrorContext(request.Context(), "read public Schedule script", "error", err)
		http.Error(response, "script unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=3600")
	response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(content)
}

func publicMethodAllowed(response http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	response.Header().Set("Allow", "GET, HEAD")
	http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func setScheduleHeaders(response http.ResponseWriter, etag string, private bool) {
	response.Header().Add("Vary", "Cookie")
	cacheControl := "public, max-age=15, must-revalidate"
	if private {
		cacheControl = "private, no-store"
	} else {
		response.Header().Set("ETag", etag)
	}
	response.Header().Set("Cache-Control", cacheControl)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func scheduleNotModified(response http.ResponseWriter, request *http.Request, etag string) bool {
	if request.Header.Get("If-None-Match") != etag {
		return false
	}
	response.WriteHeader(http.StatusNotModified)
	return true
}

package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/rundown"
)

type planningHandlers struct {
	browser        frontendHandlers
	events         *events.Service
	commands       *rundown.Commands
	queries        *rundown.Queries
	notifyDisplays func()
}

func registerPlanningRoutes(
	mux *routeMux,
	authentication *auth.Service,
	eventService *events.Service,
	commands *rundown.Commands,
	queries *rundown.Queries,
	notifyDisplays func(),
	logger *slog.Logger,
) {
	handlers := planningHandlers{
		browser: frontendHandlers{
			authentication: authentication,
			logger:         logger,
			random:         rand.Reader,
		},
		events:         eventService,
		commands:       commands,
		queries:        queries,
		notifyDisplays: notifyDisplays,
	}
	route := backstagePageRoute()
	route.maxBodyBytes = maxRundownRPCBodyBytes
	mux.HandleFunc("/backstage/events/{eventID}/planning", route, handlers.planning)
	mux.HandleFunc("/backstage/events/new", route, handlers.newEvent)
}

func (handlers planningHandlers) newEvent(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	if !actor.Administrator {
		http.NotFound(response, request)
		return
	}
	csrfToken, err := handlers.browser.csrfToken(response, request)
	if err != nil {
		handlers.browser.frontendError(response, request, "create CSRF proof", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event command identity", err)
		return
	}
	grantCommandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create Event Grant command identity", err)
		return
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
		handlers.renderNewEvent(
			response, request, actor, csrfToken, commandID, grantCommandID,
			http.StatusOK, "",
		)
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		commandID = request.Form.Get("command_id")
		grantCommandID = request.Form.Get("grant_command_id")
		input, inputErr := newEventFormInput(request)
		if inputErr != nil {
			status, message := planningError(inputErr)
			handlers.renderNewEvent(
				response, request, actor, csrfToken, commandID, grantCommandID,
				status, message,
			)
			return
		}
		created, createErr := handlers.events.Create(request.Context(), actor, input)
		if createErr == nil && request.Form.Get("grant_self") == "true" {
			_, createErr = handlers.events.GrantEventAccess(
				request.Context(),
				actor,
				created.ID,
				actor.ID,
				"Producer",
				grantCommandID,
			)
		}
		if createErr != nil {
			status, message := planningError(createErr)
			handlers.renderNewEvent(
				response, request, actor, csrfToken, commandID, grantCommandID,
				status, message,
			)
			return
		}
		target := "/backstage"
		if request.Form.Get("grant_self") == "true" {
			target = "/backstage/events/" + strconv.Itoa(created.ID) + "/planning"
		}
		http.Redirect(response, request, target, http.StatusSeeOther)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers planningHandlers) renderNewEvent(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	csrfToken, commandID, grantCommandID string,
	status int,
	message string,
) {
	handlers.browser.render(response, request, status, frontend.NewEvent(frontend.NewEventPage{
		AccountName: actor.Name, CSRFToken: csrfToken,
		ReducedEffects: reducedEffectsCookie(request), Navigation: backstageNavigation(actor),
		CommandID: commandID, GrantCommandID: grantCommandID, Error: message,
	}))
}

func (handlers planningHandlers) planning(response http.ResponseWriter, request *http.Request) {
	actor, ok := handlers.browser.browserAccount(response, request)
	if !ok {
		return
	}
	eventID, err := positivePathID(request, "eventID")
	if err != nil || !actor.CanProduceEvent(eventID) {
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
		handlers.render(response, request, actor, eventID, csrfToken, http.StatusOK, "", nil, nil, nil, "", "", "", "")
	case http.MethodPost:
		if !handlers.browser.validForm(response, request) {
			return
		}
		handlers.submit(response, request, actor, eventID, csrfToken)
	default:
		frontendMethodNotAllowed(response, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func (handlers planningHandlers) submit(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
) {
	var (
		publishPreview   *rundown.PublishPreview
		csvPreview       *rundown.CSVImportPreview
		icalendarPreview *rundown.ICalendarImportPreview
		err              error
		mutated          bool
	)
	switch request.Form.Get("action") {
	case "event":
		var input events.CreateInput
		input, err = eventFormInput(request)
		if err == nil {
			_, err = handlers.events.Update(request.Context(), actor, eventID, input)
			mutated = err == nil
			if mutated {
				handlers.notifyDisplays()
			}
		}
	case "draft":
		var input rundown.EditDraftInput
		var event events.Event
		event, err = handlers.events.CrewEvent(request.Context(), actor, eventID)
		if err == nil {
			var draft rundown.DraftRundown
			draft, err = handlers.queries.DraftRundown(request.Context(), actor, eventID)
			if err == nil {
				input, err = draftFormInput(request, eventID, event.Timezone, draft)
			}
		}
		if err == nil {
			_, err = handlers.commands.EditDraft(request.Context(), actor, input)
			mutated = err == nil
		}
	case "publish-preview":
		var preview rundown.PublishPreview
		var changeIDs []int
		changeIDs, err = positiveFormIDs(request.Form["change_id"])
		if err == nil {
			preview, err = handlers.queries.PublishPreview(request.Context(), actor, rundown.PublishPreviewInput{
				EventID: eventID, ChangeIDs: changeIDs,
			})
			publishPreview = &preview
		}
	case "publish":
		var input rundown.PublishInput
		input, err = publishFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.Publish(request.Context(), actor, input)
			mutated = err == nil
			if mutated {
				handlers.notifyDisplays()
			}
		}
	case "csv-preview":
		var mappings []rundown.CSVFieldMapping
		mappings, err = csvFormMappings(request.Form.Get("csv_mappings"))
		if err == nil {
			preview, previewErr := handlers.queries.PreviewCSVImport(
				request.Context(),
				actor,
				rundown.CSVImportPreviewInput{
					EventID:  eventID,
					CSVData:  request.Form.Get("csv_data"),
					Mappings: mappings,
				},
			)
			if previewErr != nil {
				var validation *rundown.ValidationError
				if !errors.As(previewErr, &validation) {
					previewErr = formValidationError("csv_data", previewErr.Error())
				}
			}
			err, csvPreview = previewErr, &preview
		}
	case "csv-import":
		var input rundown.CSVImportInput
		input, err = csvImportFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.ImportCSV(request.Context(), actor, input)
			mutated = err == nil
		}
	case "icalendar-preview":
		var choices []rundown.ICalendarOccurrenceChoice
		choices, err = icalendarFormChoices(request.Form.Get("icalendar_choices"))
		if err == nil {
			preview, previewErr := handlers.queries.PreviewICalendarImport(
				request.Context(),
				actor,
				rundown.ICalendarImportPreviewInput{
					EventID: eventID,
					Data:    request.Form.Get("icalendar_data"),
					Choices: choices,
				},
			)
			if previewErr != nil {
				var validation *rundown.ValidationError
				if !errors.As(previewErr, &validation) {
					previewErr = formValidationError("icalendar_data", previewErr.Error())
				}
			}
			err, icalendarPreview = previewErr, &preview
		}
	case "icalendar-import":
		var input rundown.ICalendarImportInput
		input, err = icalendarImportFormInput(request, eventID)
		if err == nil {
			_, err = handlers.commands.ImportICalendar(request.Context(), actor, input)
			mutated = err == nil
		}
	default:
		err = formValidationError("action", "must identify a planning action")
	}
	if mutated {
		http.Redirect(
			response,
			request,
			"/backstage/events/"+strconv.Itoa(eventID)+"/planning",
			http.StatusSeeOther,
		)
		return
	}
	status, message := planningError(err)
	handlers.render(
		response,
		request,
		actor,
		eventID,
		csrfToken,
		status,
		message,
		publishPreview,
		csvPreview,
		icalendarPreview,
		request.Form.Get("csv_data"),
		request.Form.Get("csv_mappings"),
		request.Form.Get("icalendar_data"),
		request.Form.Get("icalendar_choices"),
	)
}

func (handlers planningHandlers) render(
	response http.ResponseWriter,
	request *http.Request,
	actor auth.Account,
	eventID int,
	csrfToken string,
	status int,
	message string,
	publishPreview *rundown.PublishPreview,
	csvPreview *rundown.CSVImportPreview,
	icalendarPreview *rundown.ICalendarImportPreview,
	csvData string,
	csvMappings string,
	icalendarData string,
	icalendarChoices string,
) {
	event, err := handlers.events.CrewEvent(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Event planning state", err)
		return
	}
	crew, err := handlers.queries.CrewRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Published Rundown", err)
		return
	}
	draft, err := handlers.queries.DraftRundown(request.Context(), actor, eventID)
	if err != nil {
		handlers.browser.frontendError(response, request, "read materialized Draft Rundown", err)
		return
	}
	current, err := handlers.queries.PublishPreview(
		request.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: eventID},
	)
	if err != nil {
		handlers.browser.frontendError(response, request, "read Draft review", err)
		return
	}
	commandID, err := planningCommandID(handlers.browser.random)
	if err != nil {
		handlers.browser.frontendError(response, request, "create command identity", err)
		return
	}
	sessionBases := make(map[int]string, len(draft.Sessions))
	for _, session := range draft.Sessions {
		encoded, encodeErr := json.Marshal(session)
		if encodeErr != nil {
			handlers.browser.frontendError(response, request, "encode viewed Draft Session", encodeErr)
			return
		}
		sessionBases[session.ID] = string(encoded)
	}
	handlers.browser.render(response, request, status, frontend.Planning(frontend.PlanningPage{
		AccountName: actor.Name, CSRFToken: csrfToken,
		ReducedEffects: reducedEffectsCookie(request), Navigation: backstageNavigation(actor),
		Event: event, Rundown: crew, Draft: draft, SessionBases: sessionBases, CurrentPreview: current,
		PublishPreview: publishPreview, CSVPreview: csvPreview,
		ICalendarPreview: icalendarPreview, CSVData: csvData, CSVMappings: csvMappings,
		ICalendarData: icalendarData, ICalendarChoices: icalendarChoices,
		CommandID: commandID, Error: message,
	}))
}

func planningCommandID(random io.Reader) (string, error) {
	value := make([]byte, 18)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", err
	}
	return "browser-" + base64.RawURLEncoding.EncodeToString(value), nil
}

func planningError(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	var eventValidation *events.ValidationError
	var rundownValidation *rundown.ValidationError
	switch {
	case errors.As(err, &eventValidation):
		return http.StatusUnprocessableEntity, eventValidation.Error()
	case errors.As(err, &rundownValidation):
		return http.StatusUnprocessableEntity, rundownValidation.Error()
	case errors.Is(err, events.ErrRevisionConflict):
		return http.StatusConflict, "Event changed. Reload and review the latest revision."
	case errors.Is(err, rundown.ErrDraftRevisionConflict):
		return http.StatusConflict, "Draft changed. Reload and review the latest revision."
	case errors.Is(err, rundown.ErrStalePreview):
		return http.StatusConflict, "Publish Preview is stale. Preview the current Draft again."
	case errors.Is(err, events.ErrCommandConflict), errors.Is(err, rundown.ErrCommandConflict):
		return http.StatusConflict, "Command identity was already used for different work."
	case errors.Is(err, events.ErrEventAccessDenied), errors.Is(err, rundown.ErrEventAccessDenied):
		return http.StatusNotFound, "Event not found."
	default:
		return http.StatusInternalServerError, "Planning action failed."
	}
}

func eventFormInput(request *http.Request) (events.CreateInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_event_revision")
	if err != nil || revision == 0 {
		return events.CreateInput{}, formValidationError("expected_event_revision", "must be positive")
	}
	input, err := eventConfigurationFormInput(request)
	if err != nil {
		return events.CreateInput{}, err
	}
	input.ExpectedRevision = revision
	return input, nil
}

func newEventFormInput(request *http.Request) (events.CreateInput, error) {
	return eventConfigurationFormInput(request)
}

func eventConfigurationFormInput(request *http.Request) (events.CreateInput, error) {
	presets, err := commaSeparatedInts(request.Form.Get("target_adjustment_presets_seconds"))
	if err != nil {
		return events.CreateInput{}, formValidationError(
			"target_adjustment_presets_seconds",
			"must contain comma-separated integers",
		)
	}
	return events.CreateInput{
		Name:                           request.Form.Get("event_name"),
		PlannedStartDate:               request.Form.Get("planned_start_date"),
		PlannedEndDate:                 request.Form.Get("planned_end_date"),
		Timezone:                       request.Form.Get("timezone"),
		EventLocale:                    request.Form.Get("event_locale"),
		ContentLanguage:                request.Form.Get("content_language"),
		EventDayBoundary:               request.Form.Get("event_day_boundary"),
		EntryDefaultDisposition:        request.Form.Get("entry_default_disposition"),
		TargetAdjustmentPresetsSeconds: presets,
		CommandID:                      request.Form.Get("command_id"),
	}, nil
}

func draftFormInput(
	request *http.Request,
	eventID int,
	timezone string,
	draft rundown.DraftRundown,
) (rundown.EditDraftInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	input := rundown.EditDraftInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision,
	}
	locationID, err := optionalPositiveFormInt(request, "location_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	locationName := strings.TrimSpace(request.Form.Get("location_name"))
	if locationID != 0 || locationName != "" {
		location := rundown.LocationDraftInput{ID: locationID, Name: locationName}
		if locationID == 0 {
			location.Ref = "browser-location"
		} else {
			if _, ok := draftLocation(draft, locationID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"location_id",
					"must identify current Draft structure",
				)
			}
			if locationName != request.Form.Get("base_location_name") {
				location.UpdateFields = []string{"name"}
			}
		}
		input.Locations = append(input.Locations, location)
	}
	laneID, err := optionalPositiveFormInt(request, "lane_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	laneName := strings.TrimSpace(request.Form.Get("lane_name"))
	if laneID != 0 || laneName != "" {
		lane := rundown.LaneDraftInput{ID: laneID, Name: laneName}
		if laneID == 0 {
			locationTarget, targetErr := formTarget(
				request,
				"lane_location_id",
				locationName != "" && locationID == 0,
				"browser-location",
			)
			if targetErr != nil {
				return rundown.EditDraftInput{}, targetErr
			}
			lane.Ref = "browser-lane"
			lane.Location = locationTarget
		} else {
			if _, ok := draftLane(draft, laneID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"lane_id",
					"must identify current Draft structure",
				)
			}
			if laneName != "" {
				if laneName != request.Form.Get("base_lane_name") {
					lane.UpdateFields = append(lane.UpdateFields, "name")
				}
			}
			if request.Form.Get("lane_location_id") != "" {
				locationTarget, targetErr := formTarget(
					request,
					"lane_location_id",
					false,
					"",
				)
				if targetErr != nil {
					return rundown.EditDraftInput{}, targetErr
				}
				baseLocationID, baseErr := strconv.Atoi(request.Form.Get("base_lane_location_id"))
				if baseErr != nil || baseLocationID <= 0 {
					return rundown.EditDraftInput{}, formValidationError(
						"base_lane_location_id",
						"must identify the viewed Draft Location",
					)
				}
				if locationTarget.ID != baseLocationID {
					lane.Location = locationTarget
					lane.UpdateFields = append(lane.UpdateFields, "location")
				}
			}
		}
		input.Lanes = append(input.Lanes, lane)
	}
	trackID, err := optionalPositiveFormInt(request, "track_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	trackName := strings.TrimSpace(request.Form.Get("track_name"))
	if trackID != 0 || trackName != "" {
		track := rundown.TrackDraftInput{ID: trackID, Name: trackName}
		if trackID == 0 {
			track.Ref = "browser-track"
		} else {
			if _, ok := draftTrack(draft, trackID); !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"track_id",
					"must identify current Draft structure",
				)
			}
			if trackName != request.Form.Get("base_track_name") {
				track.UpdateFields = []string{"name"}
			}
		}
		input.Tracks = append(input.Tracks, track)
	}
	sessionID, err := optionalPositiveFormInt(request, "session_id")
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	sessionTitle := strings.TrimSpace(request.Form.Get("session_title"))
	if sessionID != 0 || sessionTitle != "" {
		var current, base *rundown.CrewSession
		if sessionID != 0 {
			found, ok := draftSession(draft, sessionID)
			if !ok {
				return rundown.EditDraftInput{}, formValidationError(
					"session_id",
					"must identify current Draft structure",
				)
			}
			current = &found
			var viewed rundown.CrewSession
			if decodeErr := json.Unmarshal(
				[]byte(request.Form.Get("base_session")),
				&viewed,
			); decodeErr != nil || viewed.ID != sessionID {
				return rundown.EditDraftInput{}, formValidationError(
					"base_session",
					"must identify the viewed Draft Session",
				)
			}
			base = &viewed
		}
		session, sessionErr := sessionFormInput(
			request,
			sessionID,
			timezone,
			current,
			base,
			locationName != "" && locationID == 0,
			laneName != "" && laneID == 0,
			trackName != "" && trackID == 0,
		)
		if sessionErr != nil {
			return rundown.EditDraftInput{}, sessionErr
		}
		input.Sessions = append(input.Sessions, session)
	}
	if len(input.Locations)+len(input.Lanes)+len(input.Tracks)+len(input.Sessions) == 0 {
		return rundown.EditDraftInput{}, formValidationError("draft", "must include a structural edit")
	}
	return input, nil
}

func sessionFormInput(
	request *http.Request,
	sessionID int,
	timezone string,
	current *rundown.CrewSession,
	base *rundown.CrewSession,
	newLocation bool,
	newLane bool,
	newTrack bool,
) (rundown.SessionDraftInput, error) {
	location, err := formTarget(request, "session_location_id", newLocation, "browser-location")
	if err != nil && sessionID == 0 {
		return rundown.SessionDraftInput{}, err
	}
	lane, err := formTarget(request, "session_lane_id", newLane, "browser-lane")
	if err != nil && sessionID == 0 {
		return rundown.SessionDraftInput{}, err
	}
	track, trackErr := formTarget(request, "session_track_id", newTrack, "browser-track")
	if trackErr != nil && strings.TrimSpace(request.Form.Get("session_track_id")) != "" {
		return rundown.SessionDraftInput{}, trackErr
	}
	plannedStart, err := planningFormTime(
		request.Form.Get("planned_start"), timezone,
		request.Form.Get("planned_start_occurrence"), currentSessionTime(base, "start"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("planned_start", err.Error())
	}
	plannedEnd, err := planningFormTime(
		request.Form.Get("planned_end"), timezone,
		request.Form.Get("planned_end_occurrence"), currentSessionTime(base, "end"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("planned_end", err.Error())
	}
	minimumDuration, err := time.ParseDuration(request.Form.Get("minimum_duration"))
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("minimum_duration", "must be a duration such as 15m")
	}
	uploadDeadline, err := optionalPlanningFormTime(
		request.Form.Get("upload_deadline"),
		timezone,
		request.Form.Get("upload_deadline_occurrence"),
		currentSessionTime(base, "upload"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("upload_deadline", err.Error())
	}
	submissionDeadline, err := optionalPlanningFormTime(
		request.Form.Get("submission_deadline"),
		timezone,
		request.Form.Get("submission_deadline_occurrence"),
		currentSessionTime(base, "submission"),
	)
	if err != nil {
		return rundown.SessionDraftInput{}, formValidationError("submission_deadline", err.Error())
	}
	session := rundown.SessionDraftInput{
		ID: sessionID, Title: request.Form.Get("session_title"),
		Speaker:            request.Form.Get("session_speaker"),
		Type:               rundown.SessionType(request.Form.Get("session_type")),
		AudienceVisibility: rundown.AudienceVisibility(request.Form.Get("audience_visibility")),
		PublicDetails:      request.Form.Get("public_details"), CrewNotes: request.Form.Get("crew_notes"),
		PlannedStart: plannedStart, PlannedEnd: plannedEnd,
		TimingPolicy:    rundown.TimingPolicy(request.Form.Get("timing_policy")),
		MinimumDuration: minimumDuration,
		StartBoundary:   rundown.Boundary(request.Form.Get("start_boundary")),
		EndBoundary:     rundown.Boundary(request.Form.Get("end_boundary")),
		UploadDeadline:  uploadDeadline, SubmissionDeadline: submissionDeadline,
		EntryDefault: rundown.EntryDisposition(request.Form.Get("session_entry_default_disposition")),
	}
	if session.Type != rundown.SessionPresentation {
		session.UploadDeadline = time.Time{}
	}
	if session.Type != rundown.SessionCompetition {
		session.SubmissionDeadline = time.Time{}
		session.EntryDefault = ""
	}
	if sessionID == 0 {
		session.Ref = "browser-session"
		session.Lanes = []rundown.TargetRef{lane}
		session.Locations = []rundown.TargetRef{location}
		if track.ID != 0 || track.Ref != "" {
			session.Tracks = []rundown.TargetRef{track}
		}
	} else {
		if current == nil || base == nil {
			return rundown.SessionDraftInput{}, formValidationError("session_id", "must identify current Draft structure")
		}
		for _, field := range []struct {
			name    string
			changed bool
		}{
			{"title", session.Title != base.Title},
			{"speaker", session.Speaker != base.Speaker},
			{"type", session.Type != base.Type},
			{"audience_visibility", session.AudienceVisibility != base.AudienceVisibility},
			{"public_details", session.PublicDetails != base.PublicDetails},
			{"crew_notes", session.CrewNotes != base.CrewNotes},
			{"planned_start", !session.PlannedStart.Equal(base.PlannedStart)},
			{"planned_end", !session.PlannedEnd.Equal(base.PlannedEnd)},
			{"timing_policy", session.TimingPolicy != base.TimingPolicy},
			{"minimum_duration", session.MinimumDuration != base.MinimumDuration},
			{"start_boundary", session.StartBoundary != base.StartBoundary},
			{"end_boundary", session.EndBoundary != base.EndBoundary},
			{"upload_deadline", !session.UploadDeadline.Equal(base.UploadDeadline)},
			{"submission_deadline", !session.SubmissionDeadline.Equal(base.SubmissionDeadline)},
			{"entry_default_disposition", session.EntryDefault != base.EntryDefault},
		} {
			if field.changed {
				session.UpdateFields = append(session.UpdateFields, field.name)
			}
		}
		selectedLanes, idsErr := positiveFormIDs(request.Form["session_lane_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_lane_ids", idsErr.Error())
		}
		selectedLocations, idsErr := positiveFormIDs(request.Form["session_location_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_location_ids", idsErr.Error())
		}
		selectedTracks, idsErr := positiveFormIDs(request.Form["session_track_ids"])
		if idsErr != nil {
			return rundown.SessionDraftInput{}, formValidationError("session_track_ids", idsErr.Error())
		}
		session.AddLanes, session.RemoveLanes = membershipChanges(base.LaneIDs, selectedLanes)
		session.AddLocations, session.RemoveLocations = membershipChanges(base.LocationIDs, selectedLocations)
		session.AddTracks, session.RemoveTracks = membershipChanges(base.TrackIDs, selectedTracks)
		if len(session.AddLanes) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_lanes")
		}
		if len(session.RemoveLanes) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_lanes")
		}
		if len(session.AddLocations) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_locations")
		}
		if len(session.RemoveLocations) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_locations")
		}
		if len(session.AddTracks) != 0 {
			session.UpdateFields = append(session.UpdateFields, "add_tracks")
		}
		if len(session.RemoveTracks) != 0 {
			session.UpdateFields = append(session.UpdateFields, "remove_tracks")
		}
	}
	return session, nil
}

func publishFormInput(request *http.Request, eventID int) (rundown.PublishInput, error) {
	draftRevision, err := nonnegativeFormInt(request, "draft_revision")
	if err != nil {
		return rundown.PublishInput{}, err
	}
	publishedRevision, err := nonnegativeFormInt(request, "published_revision")
	if err != nil {
		return rundown.PublishInput{}, err
	}
	changeIDs, err := positiveFormIDs(request.Form["change_id"])
	if err != nil {
		return rundown.PublishInput{}, formValidationError("change_id", "must identify Draft changes")
	}
	return rundown.PublishInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: draftRevision, PublishedRevision: publishedRevision,
			ChangeIDs: changeIDs, Fingerprint: request.Form.Get("fingerprint"),
		},
		PublishNote: request.Form.Get("publish_note"),
	}, nil
}

func csvImportFormInput(request *http.Request, eventID int) (rundown.CSVImportInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.CSVImportInput{}, err
	}
	mappings, err := csvFormMappings(request.Form.Get("csv_mappings"))
	if err != nil {
		return rundown.CSVImportInput{}, err
	}
	return rundown.CSVImportInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision, CSVData: request.Form.Get("csv_data"),
		Mappings: mappings, Fingerprint: request.Form.Get("fingerprint"),
		ProposalIDs: request.Form["proposal_id"],
	}, nil
}

func icalendarImportFormInput(
	request *http.Request,
	eventID int,
) (rundown.ICalendarImportInput, error) {
	revision, err := nonnegativeFormInt(request, "expected_draft_revision")
	if err != nil {
		return rundown.ICalendarImportInput{}, err
	}
	choices, err := icalendarFormChoices(request.Form.Get("icalendar_choices"))
	if err != nil {
		return rundown.ICalendarImportInput{}, err
	}
	return rundown.ICalendarImportInput{
		EventID: eventID, CommandID: request.Form.Get("command_id"),
		ExpectedDraftRevision: revision, Data: request.Form.Get("icalendar_data"),
		Choices: choices, Fingerprint: request.Form.Get("fingerprint"),
		ProposalIDs: request.Form["proposal_id"],
	}, nil
}

func csvFormMappings(value string) ([]rundown.CSVFieldMapping, error) {
	var mappings []rundown.CSVFieldMapping
	for line := range strings.SplitSeq(strings.TrimSpace(value), "\n") {
		source, target, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
			return nil, formValidationError("csv_mappings", "must use one source=target mapping per line")
		}
		mappings = append(mappings, rundown.CSVFieldMapping{
			SourceColumn: strings.TrimSpace(source),
			TargetField:  strings.TrimSpace(target),
		})
	}
	if len(mappings) == 0 {
		return nil, formValidationError("csv_mappings", "must include at least one mapping")
	}
	return mappings, nil
}

func icalendarFormChoices(value string) ([]rundown.ICalendarOccurrenceChoice, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var choices []rundown.ICalendarOccurrenceChoice
	for line := range strings.SplitSeq(strings.TrimSpace(value), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) != 3 {
			return nil, formValidationError(
				"icalendar_choices",
				"must use one UID,PROPERTY,Earlier|Later choice per line",
			)
		}
		choices = append(choices, rundown.ICalendarOccurrenceChoice{
			UID: strings.TrimSpace(parts[0]), Property: strings.TrimSpace(parts[1]),
			Occurrence: strings.TrimSpace(parts[2]),
		})
	}
	return choices, nil
}

func formTarget(
	request *http.Request,
	field string,
	useRef bool,
	ref string,
) (rundown.TargetRef, error) {
	id, err := optionalPositiveFormInt(request, field)
	if err != nil {
		return rundown.TargetRef{}, err
	}
	if id != 0 {
		return rundown.TargetRef{ID: id}, nil
	}
	if useRef {
		return rundown.TargetRef{Ref: ref}, nil
	}
	return rundown.TargetRef{}, formValidationError(field, "must identify published or new structure")
}

func planningFormTime(
	value, timezone, occurrence string,
	current time.Time,
) (time.Time, error) {
	if !current.IsZero() {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, errors.New("event timezone is invalid")
		}
		if occurrence == "" && current.In(location).Format("2006-01-02T15:04") == value {
			return current, nil
		}
	}
	return rundown.ResolveLocalDateTime(value, timezone, occurrence)
}

func membershipChanges(base, selected []int) (additions, removals []rundown.TargetRef) {
	for _, id := range selected {
		if !slices.Contains(base, id) {
			additions = append(additions, rundown.TargetRef{ID: id})
		}
	}
	for _, id := range base {
		if !slices.Contains(selected, id) {
			removals = append(removals, rundown.TargetRef{ID: id})
		}
	}
	return additions, removals
}

func optionalPlanningFormTime(
	value, timezone, occurrence string,
	current time.Time,
) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return planningFormTime(value, timezone, occurrence, current)
}

func currentSessionTime(session *rundown.CrewSession, field string) time.Time {
	if session == nil {
		return time.Time{}
	}
	switch field {
	case "start":
		return session.PlannedStart
	case "end":
		return session.PlannedEnd
	case "upload":
		return session.UploadDeadline
	case "submission":
		return session.SubmissionDeadline
	default:
		return time.Time{}
	}
}

func draftLocation(draft rundown.DraftRundown, id int) (rundown.CrewLocation, bool) {
	for _, item := range draft.Locations {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewLocation{}, false
}

func draftLane(draft rundown.DraftRundown, id int) (rundown.CrewLane, bool) {
	for _, item := range draft.Lanes {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewLane{}, false
}

func draftTrack(draft rundown.DraftRundown, id int) (rundown.CrewTrack, bool) {
	for _, item := range draft.Tracks {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewTrack{}, false
}

func draftSession(draft rundown.DraftRundown, id int) (rundown.CrewSession, bool) {
	for _, item := range draft.Sessions {
		if item.ID == id {
			return item, true
		}
	}
	return rundown.CrewSession{}, false
}

func optionalPositiveFormInt(request *http.Request, field string) (int, error) {
	value := strings.TrimSpace(request.Form.Get(field))
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, formValidationError(field, "must be a positive integer")
	}
	return parsed, nil
}

func nonnegativeFormInt(request *http.Request, field string) (int, error) {
	value := strings.TrimSpace(request.Form.Get(field))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, formValidationError(field, "must be a nonnegative integer")
	}
	return parsed, nil
}

func commaSeparatedInts(value string) ([]int, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	result := make([]int, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func formValidationError(field, message string) error {
	return &rundown.ValidationError{Field: field, Message: message}
}

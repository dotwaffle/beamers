// Package schedule projects and renders the public Schedule.
package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/store"
)

// ErrInvalidFilter means one attendee Schedule filter is malformed.
var ErrInvalidFilter = errors.New("invalid public Schedule filter")

// ErrSessionUnavailable means a Session cannot be bookmarked from the public Schedule.
var ErrSessionUnavailable = errors.New("Session is unavailable")

// Service owns the attendee-safe Schedule query.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// Snapshot is one cacheable public Schedule page model.
type Snapshot struct {
	EventID        int            `json:"event_id"`
	EventName      string         `json:"event_name"`
	EventSlug      string         `json:"-"`
	Language       string         `json:"language"`
	Locale         string         `json:"locale"`
	Timezone       string         `json:"timezone"`
	ViewerTimezone string         `json:"viewer_timezone,omitempty"`
	ViewerLocal    bool           `json:"viewer_local"`
	Filter         Filter         `json:"filter"`
	DayOptions     []string       `json:"day_options"`
	Locations      []FilterOption `json:"locations"`
	Lanes          []FilterOption `json:"lanes"`
	Tracks         []FilterOption `json:"tracks"`
	ETag           string         `json:"-"`
	StreamID       string         `json:"-"`
	StreamPosition uint64         `json:"-"`
	Sessions       []Session      `json:"sessions"`
	Days           []Day          `json:"days"`
	AccountName    string         `json:"-"`
	CSRFToken      string         `json:"-"`
	MySchedule     bool           `json:"-"`
	ReducedEffects bool           `json:"-"`
}

// Filter is the complete shareable attendee Schedule view state.
type Filter struct {
	Day            string `json:"day,omitempty"`
	LocationID     int    `json:"location_id,omitempty"`
	LaneID         int    `json:"lane_id,omitempty"`
	TrackID        int    `json:"track_id,omitempty"`
	ViewerTimezone string `json:"viewer_timezone,omitempty"`
}

// FilterOption is one public structural filter choice.
type FilterOption struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

// Value returns the stable URL value for one structural filter.
func (option FilterOption) Value() string {
	return strconv.Itoa(option.ID)
}

// Day groups Sessions by Event Day Boundary in Event time.
type Day struct {
	Date     string    `json:"date"`
	Sessions []Session `json:"sessions"`
}

// Session is one attendee-visible Schedule entry.
type Session struct {
	ID                   int                `json:"id"`
	Type                 string             `json:"type"`
	EventSlug            string             `json:"-"`
	Title                string             `json:"title"`
	Speaker              string             `json:"speaker,omitempty"`
	PublicDetails        string             `json:"public_details,omitempty"`
	CancellationMessage  string             `json:"cancellation_message,omitempty"`
	Lifecycle            string             `json:"lifecycle"`
	Was                  *TimePoint         `json:"was,omitempty"`
	EventDay             string             `json:"event_day"`
	LocalDate            string             `json:"local_date"`
	CalendarDateRollover bool               `json:"calendar_date_rollover"`
	Time                 TimePresentation   `json:"time"`
	LocationIDs          []int              `json:"-"`
	LaneIDs              []int              `json:"-"`
	TrackIDs             []int              `json:"-"`
	Locations            []string           `json:"locations,omitempty"`
	Lanes                []string           `json:"lanes,omitempty"`
	Tracks               []string           `json:"tracks,omitempty"`
	Attachments          []Attachment       `json:"attachments,omitempty"`
	CompetitionEntries   []CompetitionEntry `json:"competition_entries,omitempty"`
	ResultsPath          string             `json:"-"`
	Favorite             bool               `json:"favorite,omitempty"`
}

// CompetitionEntry is one attendee-visible Included submission.
type CompetitionEntry struct {
	ID                            int          `json:"id"`
	Name                          string       `json:"name"`
	PublicDetails                 string       `json:"public_details,omitempty"`
	ResultDisposition             string       `json:"result_disposition"`
	PublicDisqualificationMessage string       `json:"public_disqualification_message,omitempty"`
	Attachments                   []Attachment `json:"attachments,omitempty"`
}

// Attachment is one released immutable attendee file.
type Attachment struct {
	ID               int    `json:"id"`
	Name             string `json:"name"`
	OriginalFilename string `json:"original_filename"`
}

// TimePoint is one labeled attendee-facing operational instant.
type TimePoint struct {
	Label    string `json:"label"`
	Datetime string `json:"datetime"`
	Display  string `json:"display"`
	Event    string `json:"event"`
}

// TimePresentation is one lifecycle-specific public time range.
type TimePresentation struct {
	Start              TimePoint `json:"start"`
	End                TimePoint `json:"end"`
	ViewerLocal        bool      `json:"viewer_local"`
	TimezoneLabel      string    `json:"timezone_label"`
	EventTimezoneLabel string    `json:"event_timezone_label"`
}

// New creates a public Schedule query with explicit persistence.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("schedule storage is required")
	}
	if now == nil {
		return nil, errors.New("schedule clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Current returns the Active Event's cacheable public Schedule snapshot.
func (service *Service) Current(ctx context.Context, filter Filter) (Snapshot, error) {
	return service.snapshot(ctx, true, filter)
}

// Personalized returns one Account's Schedule or My Schedule projection.
func (service *Service) Personalized(
	ctx context.Context,
	filter Filter,
	accountID int,
	favoritesOnly bool,
) (Snapshot, error) {
	if accountID <= 0 {
		return Snapshot{}, errors.New("account is required for Schedule")
	}
	snapshot, err := service.snapshot(ctx, true, filter)
	if err != nil {
		return Snapshot{}, err
	}
	return service.personalize(ctx, snapshot, accountID, favoritesOnly)
}

func (service *Service) personalize(
	ctx context.Context,
	snapshot Snapshot,
	accountID int,
	favoritesOnly bool,
) (Snapshot, error) {
	favoriteIDs, err := service.storage.FavoriteSessionIDs(ctx, accountID)
	if err != nil {
		return Snapshot{}, err
	}
	favorites := make(map[int]struct{}, len(favoriteIDs))
	for _, sessionID := range favoriteIDs {
		favorites[sessionID] = struct{}{}
	}
	sessions := snapshot.Sessions[:0]
	for index := range snapshot.Sessions {
		_, snapshot.Sessions[index].Favorite = favorites[snapshot.Sessions[index].ID]
		if favoritesOnly && !snapshot.Sessions[index].Favorite {
			continue
		}
		sessions = append(sessions, snapshot.Sessions[index])
	}
	snapshot.Sessions = sessions
	snapshot.Days = groupScheduleDays(snapshot.Sessions)
	snapshot.MySchedule = favoritesOnly
	return snapshot, nil
}

func (service *Service) snapshot(ctx context.Context, upcomingOnly bool, filter Filter) (Snapshot, error) {
	viewerZone, err := validateFilter(filter)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := service.storage.LoadPublicSchedule(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	language := scheduleLanguage(state.ContentLanguage, state.EventLocale)
	result := Snapshot{
		EventID: state.EventID, EventName: state.EventName,
		Language: language, Locale: state.EventLocale, Timezone: state.Timezone,
		Filter: filter,
	}
	zone, err := time.LoadLocation(state.Timezone)
	if state.EventID != 0 && err != nil {
		return Snapshot{}, errors.New("load public Schedule timezone")
	}
	displayZone := zone
	if viewerZone != nil {
		displayZone = viewerZone
		result.ViewerTimezone = filter.ViewerTimezone
		result.ViewerLocal = true
	}
	result.Locations = projectFilterOptions(
		state.Locations, filter.LocationID,
		func(item store.PublicScheduleLocation) (int, string) { return item.ID, item.Name },
	)
	result.Lanes = projectFilterOptions(
		state.Lanes, filter.LaneID,
		func(item store.PublicScheduleLane) (int, string) { return item.ID, item.Name },
	)
	result.Tracks = projectFilterOptions(
		state.Tracks, filter.TrackID,
		func(item store.PublicScheduleTrack) (int, string) { return item.ID, item.Name },
	)
	locationNames := locationNames(state.Locations)
	laneNames := laneNames(state.Lanes)
	trackNames := trackNames(state.Tracks)
	presentations := make(map[int]publictime.Presentation, len(state.Sessions))
	for _, item := range state.Sessions {
		presentation, presentationErr := publictime.Present(item.PublicTime)
		if presentationErr != nil {
			return Snapshot{}, fmt.Errorf(
				"present public Schedule Session %d: %w",
				item.ID,
				presentationErr,
			)
		}
		presentations[item.ID] = presentation
	}
	sortScheduleSessions(state.Sessions)
	eventDays := make(map[int]string, len(state.Sessions))
	dayOptions := make(map[string]struct{})
	eligible := make([]store.PublicScheduleSession, 0, len(state.Sessions))
	for _, item := range state.Sessions {
		lifecycle := item.PublicTime.Lifecycle
		if upcomingOnly && (lifecycle == publictime.Ended ||
			(lifecycle != publictime.Live && lifecycle != publictime.Canceled &&
				!item.PublicTime.Forecast.End.After(service.now()))) {
			continue
		}
		eventDay, dayErr := groupedEventDay(
			item.PublicTime.Forecast.Start.In(zone),
			zone,
			state.EventDayBoundary,
		)
		if dayErr != nil {
			return Snapshot{}, dayErr
		}
		eventDays[item.ID] = eventDay
		dayOptions[eventDay] = struct{}{}
		eligible = append(eligible, item)
	}
	state.Sessions = filterScheduleSessions(eligible, filter, func(item store.PublicScheduleSession) string {
		return eventDays[item.ID]
	})
	for _, item := range state.Sessions {
		presentation := presentations[item.ID]
		localStart := item.PublicTime.Forecast.Start.In(zone)
		displayStart := presentation.Start.Time.In(displayZone)
		var was *TimePoint
		if presentation.Was != nil &&
			formatEventTime(presentation.Start.Time.In(displayZone), state.EventLocale) !=
				formatEventTime(presentation.Was.Time.In(displayZone), state.EventLocale) {
			projected := projectedTimePoint(
				string(presentation.Was.Label),
				presentation.Was.Time,
				displayZone,
				zone,
				state.EventLocale,
			)
			was = &projected
		}
		eventDay := eventDays[item.ID]
		presentedStart := projectedTimePoint(
			string(presentation.Start.Label),
			presentation.Start.Time,
			displayZone,
			zone,
			state.EventLocale,
		)
		presentedEnd := projectedTimePoint(
			string(presentation.End.Label),
			presentation.End.Time,
			displayZone,
			zone,
			state.EventLocale,
		)
		timePresentation := TimePresentation{Start: presentedStart, End: presentedEnd}
		timePresentation.ViewerLocal = result.ViewerLocal
		timePresentation.TimezoneLabel = displayStart.Format("MST -07:00")
		timePresentation.EventTimezoneLabel = presentation.Start.Time.In(zone).Format("MST -07:00")
		competitionEntries := make([]CompetitionEntry, 0, len(item.CompetitionEntries))
		for _, foundEntry := range item.CompetitionEntries {
			competitionEntries = append(competitionEntries, CompetitionEntry{
				ID: foundEntry.ID, Name: foundEntry.Name, PublicDetails: foundEntry.PublicDetails,
				ResultDisposition:             foundEntry.ResultDisposition,
				PublicDisqualificationMessage: foundEntry.PublicDisqualificationMessage,
			})
		}
		result.Sessions = append(result.Sessions, Session{
			ID: item.ID, Type: item.Type,
			Title: item.Title, Speaker: item.Speaker, PublicDetails: item.PublicDetails,
			CancellationMessage: item.CancellationMessage,
			Lifecycle:           string(item.PublicTime.Lifecycle), Was: was,
			EventDay: eventDay, LocalDate: localStart.Format(time.DateOnly),
			Time:        timePresentation,
			LocationIDs: item.LocationIDs, LaneIDs: item.LaneIDs, TrackIDs: item.TrackIDs,
			Locations:          names(item.LocationIDs, locationNames),
			Lanes:              names(item.LaneIDs, laneNames),
			Tracks:             names(item.TrackIDs, trackNames),
			CompetitionEntries: competitionEntries,
		})
	}
	result.DayOptions = sortedKeys(dayOptions)
	result.Days = groupScheduleDays(result.Sessions)
	if err := setSnapshotETag(&result); err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

func setSnapshotETag(snapshot *Snapshot) error {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return errors.New("encode public Schedule validator")
	}
	snapshot.ETag = fmt.Sprintf(`"schedule-%x"`, sha256.Sum256(encoded))
	return nil
}

func groupedEventDay(local time.Time, zone *time.Location, boundary string) (string, error) {
	year, month, day := local.Date()
	calendarDate := time.Date(year, month, day, 0, 0, 0, 0, zone)
	resolved, err := events.ResolveDayBoundary(calendarDate, zone, boundary)
	if err != nil {
		return "", errors.New("resolve public Schedule Event Day Boundary")
	}
	eventDate := calendarDate
	if local.Before(resolved) {
		eventDate = calendarDate.AddDate(0, 0, -1)
	}
	return eventDate.Format(time.DateOnly), nil
}

func groupScheduleDays(sessions []Session) []Day {
	days := make([]Day, 0)
	for index := range sessions {
		session := &sessions[index]
		newDay := len(days) == 0 || days[len(days)-1].Date != session.EventDay
		if newDay {
			days = append(days, Day{Date: session.EventDay})
			if session.LocalDate != session.EventDay {
				session.CalendarDateRollover = true
			}
		}
		last := len(days) - 1
		if daySessions := days[last].Sessions; !newDay && len(daySessions) > 0 &&
			daySessions[len(daySessions)-1].LocalDate != session.LocalDate {
			session.CalendarDateRollover = true
		}
		days[last].Sessions = append(days[last].Sessions, *session)
	}
	return days
}

func scheduleLanguage(contentLanguage, eventLocale string) string {
	if contentLanguage != "" {
		return contentLanguage
	}
	if eventLocale != "" {
		return eventLocale
	}
	return "en"
}

func sortScheduleSessions(sessions []store.PublicScheduleSession) {
	sort.SliceStable(sessions, func(first, second int) bool {
		return sessions[first].PublicTime.Forecast.Start.Before(
			sessions[second].PublicTime.Forecast.Start,
		)
	})
}

func formatEventTime(value time.Time, locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "en-us") {
		return value.Format("Jan 2, 2006 3:04 PM MST")
	}
	return value.Format("02 Jan 2006 15:04 MST")
}

// Find returns one public Session by stable identity.
func (service *Service) Find(
	ctx context.Context,
	sessionID int,
	viewerTimezone string,
) (Snapshot, Session, bool, error) {
	snapshot, err := service.snapshot(ctx, false, Filter{ViewerTimezone: viewerTimezone})
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	if session, found := findSession(snapshot.Sessions, sessionID); found {
		snapshot, session, err = service.attachReleasedSessionFiles(ctx, snapshot, session)
		return snapshot, session, true, err
	}
	return snapshot, Session{}, false, nil
}

// FindCompetition returns one complete attendee-safe Competition page projection.
func (service *Service) FindCompetition(
	ctx context.Context,
	sessionID int,
	eventSlug string,
) (Snapshot, Session, bool, error) {
	snapshot, session, found, err := service.Find(ctx, sessionID, "")
	if err != nil || !found || session.Type != "Competition" {
		return snapshot, session, false, err
	}
	snapshot.EventSlug = eventSlug
	session.EventSlug = eventSlug
	released, err := service.storage.LoadReleasedAttachmentVersions(ctx)
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	attachReleasedCompetitionFiles(&session, released)
	publication, err := service.storage.LoadResultsPublication(
		ctx,
		snapshot.EventID,
		store.ResultsPublicationEvent,
		snapshot.EventID,
	)
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	if publication.Revision > 0 {
		var resultsReleased bool
		resultsReleased, err = competitionResultsReleased(
			publication.RenderedJSON,
			session.ID,
		)
		if err != nil {
			return Snapshot{}, Session{}, false, err
		}
		if resultsReleased {
			session.ResultsPath = "/events/" + eventSlug + "/results"
		}
	}
	validator, err := json.Marshal(struct {
		ScheduleETag    string  `json:"schedule_etag"`
		Session         Session `json:"session"`
		ResultsRevision int     `json:"results_revision"`
	}{
		ScheduleETag: snapshot.ETag, Session: session,
		ResultsRevision: publication.Revision,
	})
	if err != nil {
		return Snapshot{}, Session{}, false,
			fmt.Errorf("encode public Competition validator: %w", err)
	}
	snapshot.ETag = fmt.Sprintf(`"competition-%x"`, sha256.Sum256(validator))
	return snapshot, session, true, nil
}

func attachReleasedCompetitionFiles(
	session *Session,
	released []store.AttachmentVersion,
) {
	entries := make(map[int]*CompetitionEntry, len(session.CompetitionEntries))
	for index := range session.CompetitionEntries {
		entry := &session.CompetitionEntries[index]
		entries[entry.ID] = entry
	}
	for _, version := range released {
		entry := entries[version.OwnerID]
		if version.OwnerType != store.UploadTargetEntry || entry == nil {
			continue
		}
		entry.Attachments = append(entry.Attachments, Attachment{
			ID: version.ID, Name: version.Name, OriginalFilename: version.OriginalFilename,
		})
	}
}

func competitionResultsReleased(renderedJSON string, sessionID int) (bool, error) {
	var publication results.PublicResultsPublication
	if err := json.Unmarshal([]byte(renderedJSON), &publication); err != nil {
		return false, fmt.Errorf("decode public Competition Results: %w", err)
	}
	for _, item := range publication.Items {
		if item.Competition != nil && item.Competition.SessionID == sessionID ||
			item.NoPublicResults != nil && item.NoPublicResults.SessionID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// FindPersonalized returns one public Session with one Account's Favorite state.
func (service *Service) FindPersonalized(
	ctx context.Context,
	sessionID int,
	viewerTimezone string,
	accountID int,
) (Snapshot, Session, bool, error) {
	if accountID <= 0 {
		return Snapshot{}, Session{}, false, errors.New("account is required for Schedule")
	}
	snapshot, err := service.snapshot(
		ctx,
		false,
		Filter{ViewerTimezone: viewerTimezone},
	)
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	snapshot, err = service.personalize(ctx, snapshot, accountID, false)
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	if session, found := findSession(snapshot.Sessions, sessionID); found {
		snapshot, session, err = service.attachReleasedSessionFiles(ctx, snapshot, session)
		return snapshot, session, true, err
	}
	return snapshot, Session{}, false, nil
}

func (service *Service) attachReleasedSessionFiles(
	ctx context.Context,
	snapshot Snapshot,
	session Session,
) (Snapshot, Session, error) {
	if session.Type == "Presentation" {
		released, err := service.storage.LoadReleasedAttachmentVersions(ctx)
		if err != nil {
			return Snapshot{}, Session{}, err
		}
		for _, version := range released {
			if version.OwnerType != store.UploadTargetPresentation ||
				version.OwnerID != session.ID {
				continue
			}
			session.Attachments = append(session.Attachments, Attachment{
				ID: version.ID, Name: version.Name, OriginalFilename: version.OriginalFilename,
			})
		}
	}
	validator, err := json.Marshal(struct {
		ScheduleETag string  `json:"schedule_etag"`
		Session      Session `json:"session"`
	}{ScheduleETag: snapshot.ETag, Session: session})
	if err != nil {
		return Snapshot{}, Session{}, fmt.Errorf("encode public Session validator: %w", err)
	}
	snapshot.ETag = fmt.Sprintf(`"session-%x"`, sha256.Sum256(validator))
	return snapshot, session, nil
}

func findSession(sessions []Session, sessionID int) (Session, bool) {
	for _, session := range sessions {
		if session.ID == sessionID {
			return session, true
		}
	}
	return Session{}, false
}

// SetFavorite adds or removes one Account's bookmark of a public Session.
func (service *Service) SetFavorite(
	ctx context.Context,
	accountID int,
	sessionID int,
	favorite bool,
) error {
	if accountID <= 0 || sessionID <= 0 {
		return ErrSessionUnavailable
	}
	_, _, found, err := service.Find(ctx, sessionID, "")
	if err != nil {
		return err
	}
	if !found {
		return ErrSessionUnavailable
	}
	return service.storage.SetFavoriteSession(ctx, accountID, sessionID, favorite)
}

// Path returns the stable public deep link for a Session identity.
func (session Session) Path() string {
	if session.EventSlug != "" {
		return "/events/" + session.EventSlug + "/schedule/sessions/" + strconv.Itoa(session.ID)
	}
	return "/schedule/sessions/" + strconv.Itoa(session.ID)
}

// CompetitionPath returns the canonical focused public Competition page.
func (session Session) CompetitionPath() string {
	return "/events/" + session.EventSlug + "/competitions/" + strconv.Itoa(session.ID)
}

// Path returns the public immutable Attachment download route.
func (attachment Attachment) Path() string {
	return "/public/attachments/" + strconv.Itoa(attachment.ID)
}

// PathWithTimezone keeps attendee-local conversion across deep-link navigation.
func (session Session) PathWithTimezone(viewerTimezone string) string {
	path := session.Path()
	if viewerTimezone == "" {
		return path
	}
	return path + "?time_zone=" + url.QueryEscape(viewerTimezone)
}

// SchedulePath preserves the complete shareable Schedule view.
func (snapshot Snapshot) SchedulePath() string {
	if snapshot.EventSlug != "" {
		return snapshot.viewPath("/events/" + snapshot.EventSlug + "/schedule")
	}
	return snapshot.viewPath("/schedule")
}

// ViewPath preserves the current Schedule or My Schedule view.
func (snapshot Snapshot) ViewPath() string {
	if snapshot.MySchedule {
		return snapshot.viewPath("/my-schedule")
	}
	return snapshot.SchedulePath()
}

func (snapshot Snapshot) viewPath(base string) string {
	values := url.Values{}
	if snapshot.Filter.Day != "" {
		values.Set("day", snapshot.Filter.Day)
	}
	if snapshot.Filter.LocationID > 0 {
		values.Set("location", strconv.Itoa(snapshot.Filter.LocationID))
	}
	if snapshot.Filter.LaneID > 0 {
		values.Set("lane", strconv.Itoa(snapshot.Filter.LaneID))
	}
	if snapshot.Filter.TrackID > 0 {
		values.Set("track", strconv.Itoa(snapshot.Filter.TrackID))
	}
	if snapshot.ViewerTimezone != "" {
		values.Set("time_zone", snapshot.ViewerTimezone)
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

// SignInPath preserves the attendee's intended Schedule location.
func SignInPath(returnTo string) string {
	return "/sign-in?return_to=" + url.QueryEscape(returnTo)
}

// FavoritePath returns one Session's private bookmark mutation route.
func (session Session) FavoritePath() string {
	return "/schedule/sessions/" + strconv.Itoa(session.ID) + "/favorite"
}

// EventsPath resumes invalidations after the complete snapshot cursor.
func (snapshot Snapshot) EventsPath() string {
	values := url.Values{}
	values.Set("after", strconv.FormatUint(snapshot.StreamPosition, 10))
	values.Set("stream_id", snapshot.StreamID)
	return "/schedule/events?" + values.Encode()
}

func locationNames(items []store.PublicScheduleLocation) map[int]string {
	result := make(map[int]string, len(items))
	for _, item := range items {
		result[item.ID] = item.Name
	}
	return result
}

func laneNames(items []store.PublicScheduleLane) map[int]string {
	result := make(map[int]string, len(items))
	for _, item := range items {
		result[item.ID] = item.Name
	}
	return result
}

func trackNames(items []store.PublicScheduleTrack) map[int]string {
	result := make(map[int]string, len(items))
	for _, item := range items {
		result[item.ID] = item.Name
	}
	return result
}

func names(ids []int, byID map[int]string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if name := byID[id]; name != "" {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func filterScheduleSessions(
	sessions []store.PublicScheduleSession,
	filter Filter,
	eventDay func(store.PublicScheduleSession) string,
) []store.PublicScheduleSession {
	result := make([]store.PublicScheduleSession, 0, len(sessions))
	for _, item := range sessions {
		if filter.Day != "" && eventDay(item) != filter.Day ||
			filter.LocationID != 0 && !containsID(item.LocationIDs, filter.LocationID) ||
			filter.LaneID != 0 && !containsID(item.LaneIDs, filter.LaneID) ||
			filter.TrackID != 0 && !containsID(item.TrackIDs, filter.TrackID) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func containsID(ids []int, selected int) bool {
	return slices.Contains(ids, selected)
}

func validateFilter(filter Filter) (*time.Location, error) {
	if filter.LocationID < 0 || filter.LaneID < 0 || filter.TrackID < 0 {
		return nil, fmt.Errorf("%w: structural ID", ErrInvalidFilter)
	}
	if filter.Day != "" {
		if _, err := time.Parse(time.DateOnly, filter.Day); err != nil {
			return nil, fmt.Errorf("%w: day", ErrInvalidFilter)
		}
	}
	if filter.ViewerTimezone != "" {
		zone, err := time.LoadLocation(filter.ViewerTimezone)
		if err != nil {
			return nil, fmt.Errorf("%w: viewer timezone", ErrInvalidFilter)
		}
		return zone, nil
	}
	return nil, nil
}

func projectedTimePoint(
	label string,
	value time.Time,
	displayZone *time.Location,
	eventZone *time.Location,
	locale string,
) TimePoint {
	return TimePoint{
		Label: label, Datetime: value.In(displayZone).Format(time.RFC3339),
		Display: formatEventTime(value.In(displayZone), locale),
		Event:   formatEventTime(value.In(eventZone), locale),
	}
}

func projectFilterOptions[T any](
	items []T,
	selected int,
	identity func(T) (int, string),
) []FilterOption {
	result := make([]FilterOption, 0, len(items))
	for _, item := range items {
		id, name := identity(item)
		result = append(result, FilterOption{ID: id, Name: name, Selected: id == selected})
	}
	sortFilterOptions(result)
	return result
}

func sortFilterOptions(options []FilterOption) {
	sort.Slice(options, func(first, second int) bool {
		if options[first].Name == options[second].Name {
			return options[first].ID < options[second].ID
		}
		return options[first].Name < options[second].Name
	})
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

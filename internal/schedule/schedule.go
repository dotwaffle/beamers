// Package schedule projects and renders the public Schedule.
package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/store"
)

// Service owns the attendee-safe Schedule query.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// Snapshot is one cacheable public Schedule page model.
type Snapshot struct {
	EventName string    `json:"event_name"`
	Language  string    `json:"language"`
	Locale    string    `json:"locale"`
	Timezone  string    `json:"timezone"`
	ETag      string    `json:"-"`
	Sessions  []Session `json:"sessions"`
	Days      []Day     `json:"days"`
}

// Day groups Sessions by Event Day Boundary in Event time.
type Day struct {
	Date     string    `json:"date"`
	Sessions []Session `json:"sessions"`
}

// Session is one attendee-visible Schedule entry.
type Session struct {
	ID                   int      `json:"id"`
	Title                string   `json:"title"`
	Speaker              string   `json:"speaker,omitempty"`
	PublicDetails        string   `json:"public_details,omitempty"`
	ForecastStart        string   `json:"forecast_start"`
	ForecastEnd          string   `json:"forecast_end"`
	Lifecycle            string   `json:"lifecycle"`
	ActualStart          string   `json:"actual_start,omitempty"`
	ActualEnd            string   `json:"actual_end,omitempty"`
	DisplayForecastStart string   `json:"display_forecast_start"`
	DisplayForecastEnd   string   `json:"display_forecast_end"`
	EventDay             string   `json:"event_day"`
	LocalDate            string   `json:"local_date"`
	CalendarDateRollover bool     `json:"calendar_date_rollover"`
	TimezoneLabel        string   `json:"timezone_label"`
	Locations            []string `json:"locations,omitempty"`
	Lanes                []string `json:"lanes,omitempty"`
	Tracks               []string `json:"tracks,omitempty"`
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
func (service *Service) Current(ctx context.Context) (Snapshot, error) {
	return service.snapshot(ctx, true)
}

func (service *Service) snapshot(ctx context.Context, upcomingOnly bool) (Snapshot, error) {
	state, err := service.storage.LoadPublicSchedule(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	language := scheduleLanguage(state.ContentLanguage, state.EventLocale)
	result := Snapshot{
		EventName: state.EventName, Language: language, Locale: state.EventLocale, Timezone: state.Timezone,
	}
	zone, err := time.LoadLocation(state.Timezone)
	if state.EventID != 0 && err != nil {
		return Snapshot{}, errors.New("load public Schedule timezone")
	}
	locationNames := locationNames(state.Locations)
	laneNames := laneNames(state.Lanes)
	trackNames := trackNames(state.Tracks)
	sortScheduleSessions(state.Sessions)
	for _, item := range state.Sessions {
		if upcomingOnly && (item.Lifecycle == "Ended" ||
			(item.Lifecycle != "Live" && !item.ForecastEnd.After(service.now()))) {
			continue
		}
		actualStart := ""
		if !item.ActualStart.IsZero() {
			actualStart = item.ActualStart.In(zone).Format(time.RFC3339)
		}
		actualEnd := ""
		if item.ActualEnd != nil {
			actualEnd = item.ActualEnd.In(zone).Format(time.RFC3339)
		}
		localStart := item.ForecastStart.In(zone)
		localEnd := item.ForecastEnd.In(zone)
		eventDay, dayErr := groupedEventDay(localStart, zone, state.EventDayBoundary)
		if dayErr != nil {
			return Snapshot{}, dayErr
		}
		result.Sessions = append(result.Sessions, Session{
			ID: item.ID, Title: item.Title, Speaker: item.Speaker, PublicDetails: item.PublicDetails,
			ForecastStart:        item.ForecastStart.In(zone).Format(time.RFC3339),
			ForecastEnd:          item.ForecastEnd.In(zone).Format(time.RFC3339),
			Lifecycle:            item.Lifecycle,
			ActualStart:          actualStart,
			ActualEnd:            actualEnd,
			DisplayForecastStart: formatEventTime(localStart, state.EventLocale),
			DisplayForecastEnd:   formatEventTime(localEnd, state.EventLocale),
			EventDay:             eventDay, LocalDate: localStart.Format(time.DateOnly),
			TimezoneLabel: localStart.Format("MST -07:00"),
			Locations:     names(item.LocationIDs, locationNames),
			Lanes:         names(item.LaneIDs, laneNames),
			Tracks:        names(item.TrackIDs, trackNames),
		})
	}
	result.Days = groupScheduleDays(result.Sessions)
	encoded, err := json.Marshal(result)
	if err != nil {
		return Snapshot{}, errors.New("encode public Schedule validator")
	}
	result.ETag = fmt.Sprintf(`"schedule-%x"`, sha256.Sum256(encoded))
	return result, nil
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
		return sessions[first].ForecastStart.Before(sessions[second].ForecastStart)
	})
}

func formatEventTime(value time.Time, locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "en-us") {
		return value.Format("Jan 2, 2006 3:04 PM MST")
	}
	return value.Format("02 Jan 2006 15:04 MST")
}

// Find returns one public Session by stable identity.
func (service *Service) Find(ctx context.Context, sessionID int) (Snapshot, Session, bool, error) {
	snapshot, err := service.snapshot(ctx, false)
	if err != nil {
		return Snapshot{}, Session{}, false, err
	}
	for _, item := range snapshot.Sessions {
		if item.ID == sessionID {
			return snapshot, item, true, nil
		}
	}
	return snapshot, Session{}, false, nil
}

// Path returns the stable public deep link for a Session identity.
func (session Session) Path() string {
	return "/schedule/sessions/" + strconv.Itoa(session.ID)
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

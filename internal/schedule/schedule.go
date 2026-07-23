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
	"time"

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
	ETag      string    `json:"-"`
	Sessions  []Session `json:"sessions"`
}

// Session is one attendee-visible Schedule entry.
type Session struct {
	ID            int      `json:"id"`
	Title         string   `json:"title"`
	Speaker       string   `json:"speaker,omitempty"`
	PublicDetails string   `json:"public_details,omitempty"`
	ForecastStart string   `json:"forecast_start"`
	ForecastEnd   string   `json:"forecast_end"`
	Lifecycle     string   `json:"lifecycle"`
	ActualStart   string   `json:"actual_start,omitempty"`
	ActualEnd     string   `json:"actual_end,omitempty"`
	Locations     []string `json:"locations,omitempty"`
	Lanes         []string `json:"lanes,omitempty"`
	Tracks        []string `json:"tracks,omitempty"`
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
	language := state.ContentLanguage
	if language == "" {
		language = "en"
	}
	result := Snapshot{
		EventName: state.EventName, Language: language,
	}
	zone, err := time.LoadLocation(state.Timezone)
	if state.EventID != 0 && err != nil {
		return Snapshot{}, errors.New("load public Schedule timezone")
	}
	locationNames := locationNames(state.Locations)
	laneNames := laneNames(state.Lanes)
	trackNames := trackNames(state.Tracks)
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
		result.Sessions = append(result.Sessions, Session{
			ID: item.ID, Title: item.Title, Speaker: item.Speaker, PublicDetails: item.PublicDetails,
			ForecastStart: item.ForecastStart.In(zone).Format(time.RFC3339),
			ForecastEnd:   item.ForecastEnd.In(zone).Format(time.RFC3339),
			Lifecycle:     item.Lifecycle,
			ActualStart:   actualStart,
			ActualEnd:     actualEnd,
			Locations:     names(item.LocationIDs, locationNames),
			Lanes:         names(item.LaneIDs, laneNames),
			Tracks:        names(item.TrackIDs, trackNames),
		})
	}
	sort.Slice(result.Sessions, func(first, second int) bool {
		return result.Sessions[first].ForecastStart < result.Sessions[second].ForecastStart
	})
	encoded, err := json.Marshal(result)
	if err != nil {
		return Snapshot{}, errors.New("encode public Schedule validator")
	}
	result.ETag = fmt.Sprintf(`"schedule-%x"`, sha256.Sum256(encoded))
	return result, nil
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

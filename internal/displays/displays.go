// Package displays enrolls and routes persistent Display identities.
package displays

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/eventthemes"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/stagetimer"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

const (
	defaultEnrollmentTTL = 10 * time.Minute
	displayTokenBytes    = 32
	enrollmentCodeBytes  = 5
	credentialLifetime   = 10 * 365 * 24 * time.Hour
	protocolVersion      = "beamers.display.v1"
	maxClockHealthMillis = int64(24 * 60 * 60 * 1000)
)

var (
	// ErrAdministratorRequired means Display Enrollment lacked installation authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrCrewRequired means the current Active Event is not visible to the Account.
	ErrCrewRequired = errors.New("crew authority required")
	// ErrEnrollmentUnavailable means a claim code is unknown, expired, or already used.
	ErrEnrollmentUnavailable = errors.New("Display Enrollment is unavailable")
	// ErrInvalidDisplay means Display Enrollment input is invalid.
	ErrInvalidDisplay = errors.New("invalid Display details")
	// ErrDisplayAuthentication means a credential cannot authenticate a Display.
	ErrDisplayAuthentication = errors.New("Display authentication required")
	// ErrInvalidAcknowledgment means applied Display state is malformed.
	ErrInvalidAcknowledgment = errors.New("invalid Display acknowledgment")
	// ErrAcknowledgmentRegression means applied Display state moved backward.
	ErrAcknowledgmentRegression = errors.New("Display acknowledgment regressed")
	// ErrAcknowledgmentConflict means one stream position names different applied state.
	ErrAcknowledgmentConflict = errors.New("Display acknowledgment conflicts")
	// ErrDisplayNotFound means Assignment targeted no enrolled Display.
	ErrDisplayNotFound = errors.New("Display not found")
	// ErrDisplayAlreadyEnrolled means re-Enrollment targeted an active Display credential.
	ErrDisplayAlreadyEnrolled = errors.New("Display already has an active credential")
	// ErrAssignmentReference means Event, Location, or View routing is invalid.
	ErrAssignmentReference = errors.New("invalid Display Assignment reference")
	// ErrCommandConflict means a Command ID was reused for different Display work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Config contains explicit Display Enrollment dependencies.
type Config struct {
	Now            func() time.Time
	Random         io.Reader
	EnrollmentTTL  time.Duration
	NotifyDisplays func()
	Emergency      EmergencySnapshotProjector
	EventThemes    *eventthemes.Service
}

// EmergencySnapshotProjector preserves connected Display snapshots and applies
// the narrow process-owned Emergency Alert during runtime storage failure.
type EmergencySnapshotProjector interface {
	ProjectDisplaySnapshot(
		credentialHash string,
		current store.DisplaySnapshotState,
		loadErr error,
	) (store.DisplaySnapshotState, error)
}

// DefaultConfig returns production Display Enrollment dependencies.
func DefaultConfig() Config {
	return Config{Now: time.Now, Random: rand.Reader, EnrollmentTTL: defaultEnrollmentTTL}
}

// Service owns Display Enrollment credentials and Assignment commands.
type Service struct {
	storage        *store.SQLite
	now            func() time.Time
	random         io.Reader
	enrollmentTTL  time.Duration
	emergency      EmergencySnapshotProjector
	eventThemes    *eventthemes.Service
	notifyDisplays func()
	themeMu        sync.RWMutex
	themeCache     map[eventThemeCacheKey]themevalue.Config
}

type eventThemeCacheKey struct {
	eventID int
	variant string
}

// Enrollment is browser-held material for one pending Display claim.
type Enrollment struct {
	Code              string
	Credential        string
	ExpiresAt         time.Time
	CredentialExpires time.Time
}

// Display is one enrolled screen identity.
type Display struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// Snapshot is the current output routing projection for one Display.
type Snapshot struct {
	ProtocolVersion       string
	AssetVersion          string
	ServerTime            time.Time
	Display               Display
	ActiveEventID         int
	EventName             string
	EventTimezone         string
	EventDayBoundary      string
	ActivationGeneration  int
	PublishedRevision     int
	LocationID            int
	LocationName          string
	ViewKey               string
	Standby               bool
	Composition           displayviews.Composition
	Sessions              []Session
	StageTimer            *StageTimer
	ProgramChannelID      int
	ProgramOutputRevision int
	ProgramOutput         *ProgramItem
	StageMessage          *DisplayOverride
	TechnicalDifficulties *DisplayOverride
	UrgentNotice          *DisplayOverride
	EmergencyAlert        *DisplayOverride
}

// DisplayOverride is one attendee-safe currently presented Override.
type DisplayOverride struct {
	ID           int
	Revision     int
	Kind         string
	Text         string
	Emphasis     string
	UntilCleared bool
	ExpiresAt    time.Time
	Presentation string
}

// AcknowledgmentInput reports the exact state one Display has applied.
type AcknowledgmentInput struct {
	ProtocolVersion               string
	AssetVersion                  string
	StreamID                      string
	StreamPosition                uint64
	ActiveEventID                 int64
	ActivationGeneration          int64
	PublishedRevision             int64
	Standby                       bool
	ClockOffset                   int64
	ClockUncertainty              uint64
	RendererUnstable              bool
	StageMessageID                int64
	StageMessageRevision          int64
	TechnicalDifficultiesID       int64
	TechnicalDifficultiesRevision int64
	UrgentNoticeID                int64
	UrgentNoticeRevision          int64
	EmergencyAlertID              int64
	EmergencyAlertRevision        int64
}

// Acknowledgment is the latest durably recorded state one Display applied.
type Acknowledgment struct {
	DisplayID                     int
	ProtocolVersion               string
	AssetVersion                  string
	StreamID                      string
	StreamPosition                uint64
	ActiveEventID                 int
	ActivationGeneration          int
	PublishedRevision             int
	AppliedAt                     time.Time
	Standby                       bool
	ClockOffset                   int64
	ClockUncertainty              uint64
	RendererUnstable              bool
	StageMessageID                int
	StageMessageRevision          int
	TechnicalDifficultiesID       int
	TechnicalDifficultiesRevision int
	UrgentNoticeID                int
	UrgentNoticeRevision          int
	EmergencyAlertID              int
	EmergencyAlertRevision        int
}

// Session is one Display-safe committed Schedule item.
type Session struct {
	ID                    int
	Title                 string
	Speaker               string
	PublicDetails         string
	ForecastStart         time.Time
	ForecastEnd           time.Time
	Lifecycle             string
	LiveStateRevision     int
	ActualStart           time.Time
	ActualEnd             *time.Time
	LocationIDs           []int
	LaneIDs               []int
	TrackIDs              []int
	Unavailable           bool
	AvailabilityMessage   string
	PresentedStart        time.Time
	PresentedEnd          time.Time
	PresentedStartLabel   publictime.Label
	PresentedEndLabel     publictime.Label
	DisplayPresentedStart string
	DisplayPresentedEnd   string
	Timeline              TimelineGeometry
	orderAt               time.Time
}

// TimelineGeometry positions one Session block on its Event-day axis. Offset
// and Width are basis points of the day, with Offset+Width never exceeding
// 10000, and Lane is an index below LaneCount. The values are meaningful only
// on a Timeline View, and Lane is a visual overlap column there, not a
// Rundown Lane.
//
// NowOffset, DayStart, DayEnd, and Gridlines describe the whole Event day
// rather than this one Session, and so carry the same value on every
// Session sharing a Day, the same way LaneCount already does. NowOffset is
// nil when the server time falls outside this Event day. DayStart and
// DayEnd let a Display client re-derive the now-line locally from its
// synchronized clock between snapshots, rather than freezing it at the
// offset this snapshot happened to compute.
type TimelineGeometry struct {
	Day       string
	Offset    int
	Width     int
	Lane      int
	LaneCount int
	NowOffset *int
	DayStart  time.Time
	DayEnd    time.Time
	Gridlines []TimelineGridline
}

// TimelineGridline is one faint labeled hour mark on a Timeline Event day.
type TimelineGridline struct {
	Offset int
	Label  string
}

// StageTimer is one authoritative live Session clock for a Display.
type StageTimer struct {
	SessionID                 int
	Title                     string
	Mode                      stagetimer.Mode
	Anchor                    time.Time
	ForecastEnd               time.Time
	Thresholds                []stagetimer.Threshold
	AdjustmentSeconds         int
	AdjustmentNoticeExpiresAt time.Time
	// SpanStart and SpanEnd bound the Session the timer is counting, so the
	// elapsed bar can report progress. They are projected once, after the
	// Sessions are presented, so the entry document and the browser renderer
	// cannot disagree. A zero edge means the span is unmeasurable and no bar
	// is drawn.
	SpanStart time.Time
	SpanEnd   time.Time
}

// ProgramItem is one Display-safe committed presentation state.
type ProgramItem struct {
	Kind    string
	EntryID int
	Title   string
	Result  *store.ProgramResult
}

// ClaimInput confirms one Display Enrollment code.
type ClaimInput struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	DisplayID int    `json:"display_id,omitempty"`
	CommandID string `json:"command_id"`
}

// AssignInput binds one Display to one Event Location and normal View.
type AssignInput struct {
	DisplayID        int      `json:"display_id"`
	EventID          int      `json:"event_id"`
	LocationID       int      `json:"location_id"`
	ViewKey          string   `json:"view_key"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
	CommandID        string   `json:"command_id"`
}

// Assignment is one committed Event-specific Display route.
type Assignment struct {
	DisplayID        int      `json:"display_id"`
	EventID          int      `json:"event_id"`
	LocationID       int      `json:"location_id"`
	ViewKey          string   `json:"view_key"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
}

// Status is one crew-visible current Display routing summary.
type Status struct {
	ID                                   int        `json:"id"`
	Name                                 string     `json:"name"`
	ActiveEventID                        int        `json:"active_event_id"`
	Standby                              bool       `json:"standby"`
	EventName                            string     `json:"event_name,omitempty"`
	LocationID                           int        `json:"location_id,omitempty"`
	LocationName                         string     `json:"location_name,omitempty"`
	ViewKey                              string     `json:"view_key,omitempty"`
	DisplayGroupKeys                     []string   `json:"display_group_keys,omitempty"`
	ProgramChannelID                     int        `json:"program_channel_id,omitempty"`
	DeliveryState                        string     `json:"delivery_state"`
	AppliedActiveEventID                 int        `json:"applied_active_event_id"`
	AppliedActivationGeneration          int        `json:"applied_activation_generation"`
	AppliedPublishedRevision             int        `json:"applied_published_revision"`
	AppliedStageMessageID                int        `json:"applied_stage_message_id,omitempty"`
	AppliedStageMessageRevision          int        `json:"applied_stage_message_revision,omitempty"`
	AppliedTechnicalDifficultiesID       int        `json:"applied_technical_difficulties_id,omitempty"`
	AppliedTechnicalDifficultiesRevision int        `json:"applied_technical_difficulties_revision,omitempty"`
	AppliedUrgentNoticeID                int        `json:"applied_urgent_notice_id,omitempty"`
	AppliedUrgentNoticeRevision          int        `json:"applied_urgent_notice_revision,omitempty"`
	AppliedEmergencyAlertID              int        `json:"applied_emergency_alert_id,omitempty"`
	AppliedEmergencyAlertRevision        int        `json:"applied_emergency_alert_revision,omitempty"`
	AppliedStandby                       bool       `json:"applied_standby"`
	AppliedAt                            *time.Time `json:"applied_at,omitempty"`
	ClockOffset                          int64      `json:"clock_offset_milliseconds"`
	ClockUncertainty                     int64      `json:"clock_uncertainty_milliseconds"`
}

// New creates a Display service with explicit storage, clock, and randomness.
func New(storage *store.SQLite, config Config) (*Service, error) {
	if storage == nil {
		return nil, errors.New("Display storage is required")
	}
	if config.Now == nil {
		return nil, errors.New("Display clock is required")
	}
	if config.Random == nil {
		return nil, errors.New("Display randomness is required")
	}
	if config.EnrollmentTTL <= 0 {
		return nil, errors.New("Display Enrollment lifetime must be positive")
	}
	return &Service{
		storage: storage, now: config.Now, random: config.Random, enrollmentTTL: config.EnrollmentTTL,
		emergency: config.Emergency, eventThemes: config.EventThemes,
		notifyDisplays: config.NotifyDisplays,
	}, nil
}

// Authenticate validates one persistent Display credential.
func (service *Service) Authenticate(ctx context.Context, credential string) error {
	if !validDisplayToken(credential) {
		return ErrDisplayAuthentication
	}
	ctx = systemactor.NewContext(ctx, systemactor.Display)
	if _, err := service.storage.FindDisplayByCredential(ctx, digest(credential)); err != nil {
		if errors.Is(err, store.ErrDisplayCredential) {
			return ErrDisplayAuthentication
		}
		return err
	}
	return nil
}

// Current authenticates a Display and returns its complete authorized Active Event Snapshot.
func (service *Service) Current(ctx context.Context, credential string) (Snapshot, error) {
	if !validDisplayToken(credential) {
		return Snapshot{}, ErrDisplayAuthentication
	}
	ctx = systemactor.NewContext(ctx, systemactor.Display)
	now := service.now().UTC()
	credentialHash := digest(credential)
	found, err := service.storage.LoadDisplaySnapshot(ctx, credentialHash, now)
	if service.emergency != nil {
		found, err = service.emergency.ProjectDisplaySnapshot(credentialHash, found, err)
	}
	if errors.Is(err, store.ErrDisplayCredential) {
		return Snapshot{}, ErrDisplayAuthentication
	}
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{
		ProtocolVersion: protocolVersion, AssetVersion: AssetVersion(), ServerTime: now,
		Display: display(found.Display), ActiveEventID: found.ActiveEventID,
		EventName: found.EventName, EventTimezone: found.EventTimezone,
		EventDayBoundary:     found.EventDayBoundary,
		ActivationGeneration: found.ActivationGeneration,
		PublishedRevision:    found.PublishedRevision, LocationID: found.LocationID,
		LocationName: found.LocationName, ViewKey: found.ViewKey, Standby: found.Standby,
		ProgramChannelID:      found.ProgramChannelID,
		ProgramOutputRevision: found.ProgramOutputRevision,
	}
	result.StageMessage = displayOverride(found.StageMessage)
	result.TechnicalDifficulties = displayOverride(found.TechnicalDifficulties)
	result.UrgentNotice = displayOverride(found.UrgentNotice)
	result.EmergencyAlert = displayOverride(found.EmergencyAlert)
	if found.ProgramChannelID > 0 {
		result.ProgramOutput = &ProgramItem{
			Kind: string(found.ProgramOutput.Kind), EntryID: found.ProgramOutput.EntryID,
			Title: found.ProgramOutput.Title, Result: found.ProgramOutput.Result,
		}
	}
	configuration, err := displayviews.ParseConfiguration(found.DisplayConfiguration)
	if err != nil {
		return Snapshot{}, errors.Join(errors.New("load Display configuration"), err)
	}
	if service.eventThemes != nil && found.ActiveEventID > 0 {
		resolved, themeErr := service.activeEventTheme(
			ctx,
			found.ActiveEventID,
			displayThemeVariant(found.ViewKey, found.Standby),
		)
		if themeErr != nil {
			return Snapshot{}, errors.Join(errors.New("load active Event Theme"), themeErr)
		}
		configuration.Theme = eventDisplayTheme(resolved, configuration.Theme.Branding)
	}
	if configuration.ReducedEffects {
		configuration.Theme.Transition = displayviews.TransitionNone
	}
	result.Composition, err = displayviews.Compose(found.ViewKey, found.Standby, configuration)
	if err != nil {
		return Snapshot{}, errors.Join(errors.New("compose Display View"), err)
	}
	timer, ok, err := projectStageTimer(found, configuration)
	if err != nil {
		return Snapshot{}, errors.Join(errors.New("project Stage Timer"), err)
	}
	if ok {
		result.StageTimer = &timer
	}
	zone := time.UTC
	if found.EventTimezone != "" {
		zone, err = time.LoadLocation(found.EventTimezone)
		if err != nil {
			return Snapshot{}, errors.Join(errors.New("load Display Event timezone"), err)
		}
	}
	for _, item := range found.Sessions {
		selected, ok, presentationErr := displaySession(found, item, now, zone)
		if presentationErr != nil {
			return Snapshot{}, errors.Join(
				fmt.Errorf("present Display Session %d", item.ID),
				presentationErr,
			)
		}
		if ok {
			result.Sessions = append(result.Sessions, selected)
		}
	}
	sort.SliceStable(result.Sessions, func(first, second int) bool {
		return result.Sessions[first].orderAt.Before(result.Sessions[second].orderAt)
	})
	if result.ViewKey == displayviews.Timeline {
		if err := projectTimeline(result.Sessions, now, zone, found.EventDayBoundary); err != nil {
			return Snapshot{}, errors.Join(errors.New("project Timeline"), err)
		}
	}
	if result.StageTimer != nil {
		result.StageTimer.SpanStart, result.StageTimer.SpanEnd =
			projectStageTimerSpan(*result.StageTimer, result.Sessions)
	}
	completedAt := service.now().UTC()
	result.ServerTime = now.Add(completedAt.Sub(now) / 2)
	return result, nil
}

func (service *Service) activeEventTheme(
	ctx context.Context,
	eventID int,
	variant string,
) (themevalue.Config, error) {
	key := eventThemeCacheKey{eventID: eventID, variant: variant}
	service.themeMu.RLock()
	cached, ok := service.themeCache[key]
	service.themeMu.RUnlock()
	if ok {
		return cached, nil
	}
	service.themeMu.Lock()
	defer service.themeMu.Unlock()
	if cached, ok = service.themeCache[key]; ok {
		return cached, nil
	}
	active, err := service.eventThemes.Active(ctx, eventID, variant)
	if err != nil {
		return themevalue.Config{}, err
	}
	if service.themeCache == nil {
		service.themeCache = make(map[eventThemeCacheKey]themevalue.Config)
	}
	service.themeCache[key] = active.Resolved
	return active.Resolved, nil
}

// InvalidateThemeCache makes the next Display Snapshot resolve active Themes.
func (service *Service) InvalidateThemeCache() {
	service.themeMu.Lock()
	defer service.themeMu.Unlock()
	clear(service.themeCache)
}

func displayOverride(found *store.DisplayOverride) *DisplayOverride {
	if found == nil {
		return nil
	}
	return &DisplayOverride{
		ID: found.ID, Revision: found.Revision, Kind: string(found.Kind), Text: found.Text,
		Emphasis: string(found.Emphasis), UntilCleared: found.UntilCleared,
		ExpiresAt: found.ExpiresAt, Presentation: string(found.Presentation),
	}
}

func projectStageTimer(
	found store.DisplaySnapshotState,
	configuration displayviews.Configuration,
) (StageTimer, bool, error) {
	// Crew Overview carries a Stage Timer Region beside its other Regions, so the
	// projection follows the Layout rather than a single View key. Naming the
	// Stage Timer View directly left that Region permanently reporting that no
	// Session was live.
	if !displayviews.UsesWidget(found.ViewKey, found.Standby, displayviews.WidgetStageTimer) {
		return StageTimer{}, false, nil
	}
	eventThresholds := timerThresholds(configuration.TimerThresholds)
	typeThresholds := make(map[string][]stagetimer.Threshold, len(configuration.SessionTypeTimerThresholds))
	for sessionType, thresholds := range configuration.SessionTypeTimerThresholds {
		typeThresholds[sessionType] = timerThresholds(thresholds)
	}
	sessionThresholds := make(map[int][]stagetimer.Threshold, len(configuration.SessionTimerThresholds))
	for sessionID, thresholds := range configuration.SessionTimerThresholds {
		sessionThresholds[sessionID] = timerThresholds(thresholds)
	}
	var selected *store.DisplaySessionState
	for index := range found.Sessions {
		session := &found.Sessions[index]
		if session.Lifecycle != "Live" || !containsID(session.LocationIDs, found.LocationID) {
			continue
		}
		if selected == nil ||
			session.ActualStart.After(selected.ActualStart) ||
			session.ActualStart.Equal(selected.ActualStart) && session.ID < selected.ID {
			selected = session
		}
	}
	if selected == nil {
		return StageTimer{}, false, nil
	}
	resolved := stagetimer.ResolveThresholds(
		eventThresholds,
		typeThresholds,
		sessionThresholds,
		selected.Type,
		selected.ID,
	)
	timer, err := stagetimer.New(stagetimer.Spec{
		SessionID:    selected.ID,
		Policy:       stagetimer.Policy(selected.TimingPolicy),
		ActualStart:  selected.ActualStart,
		PlannedStart: selected.RunPlannedStart,
		PlannedEnd:   selected.RunPlannedEnd,
		TargetEnd:    selected.ForecastEnd,
		Thresholds:   resolved,
	})
	if err != nil {
		return StageTimer{}, false, err
	}
	return StageTimer{
		SessionID:                 timer.SessionID,
		Title:                     selected.TimerTitle,
		Mode:                      timer.Mode,
		Anchor:                    timer.Anchor,
		ForecastEnd:               selected.ForecastEnd,
		Thresholds:                timer.Thresholds,
		AdjustmentSeconds:         selected.TargetAdjustmentSeconds,
		AdjustmentNoticeExpiresAt: selected.TargetAdjustedAt.Add(5 * time.Second),
	}, true, nil
}

// projectStageTimerSpan bounds the Session a Stage Timer is counting. A
// countdown anchors on the end and an elapsed timer on the start, so the
// missing edge comes from the presented Session; a Session suppressed from
// the View leaves that edge zero and the span unmeasurable.
func projectStageTimerSpan(timer StageTimer, sessions []Session) (start, end time.Time) {
	var counted Session
	for _, session := range sessions {
		if session.ID == timer.SessionID {
			counted = session
			break
		}
	}
	if timer.Mode == stagetimer.Elapsed {
		end = timer.ForecastEnd
		if end.IsZero() {
			end = counted.PresentedEnd
		}
		return timer.Anchor, end
	}
	start = counted.PresentedStart
	if start.IsZero() {
		start = counted.ForecastStart
	}
	return start, timer.Anchor
}

func timerThresholds(found []displayviews.TimerThreshold) []stagetimer.Threshold {
	result := make([]stagetimer.Threshold, 0, len(found))
	for _, threshold := range found {
		result = append(result, stagetimer.Threshold{
			Remaining: time.Duration(threshold.RemainingSeconds) * time.Second,
			Emphasis:  stagetimer.Emphasis(threshold.Emphasis),
		})
	}
	return result
}

// Acknowledge independently records state already applied by one Display.
func (service *Service) Acknowledge(
	ctx context.Context,
	credential string,
	input AcknowledgmentInput,
) (Acknowledgment, error) {
	if !validDisplayToken(credential) {
		return Acknowledgment{}, ErrDisplayAuthentication
	}
	ctx = systemactor.NewContext(ctx, systemactor.Display)
	if input.ProtocolVersion != protocolVersion ||
		input.AssetVersion != AssetVersion() ||
		input.StreamID == "" ||
		len(input.StreamID) > 200 ||
		input.StreamPosition > math.MaxInt64 ||
		input.ActiveEventID < 0 || input.ActiveEventID > math.MaxInt ||
		input.ActivationGeneration < 0 || input.ActivationGeneration > math.MaxInt ||
		input.PublishedRevision < 0 || input.PublishedRevision > math.MaxInt ||
		input.StageMessageID < 0 || input.StageMessageID > math.MaxInt ||
		input.StageMessageRevision < 0 || input.StageMessageRevision > math.MaxInt ||
		input.TechnicalDifficultiesID < 0 || input.TechnicalDifficultiesID > math.MaxInt ||
		input.TechnicalDifficultiesRevision < 0 || input.TechnicalDifficultiesRevision > math.MaxInt ||
		input.UrgentNoticeID < 0 || input.UrgentNoticeID > math.MaxInt ||
		input.UrgentNoticeRevision < 0 || input.UrgentNoticeRevision > math.MaxInt ||
		input.EmergencyAlertID < 0 || input.EmergencyAlertID > math.MaxInt ||
		input.EmergencyAlertRevision < 0 || input.EmergencyAlertRevision > math.MaxInt ||
		input.ClockOffset < -maxClockHealthMillis ||
		input.ClockOffset > maxClockHealthMillis ||
		input.ClockUncertainty > uint64(maxClockHealthMillis) {
		return Acknowledgment{}, ErrInvalidAcknowledgment
	}
	stored, err := service.storage.RecordDisplayAcknowledgment(ctx, digest(credential), store.DisplayAcknowledgment{
		ProtocolVersion: input.ProtocolVersion, AssetVersion: input.AssetVersion,
		StreamID:       input.StreamID,
		StreamPosition: int64(input.StreamPosition), ActiveEventID: int(input.ActiveEventID),
		ActivationGeneration:          int(input.ActivationGeneration),
		PublishedRevision:             int(input.PublishedRevision),
		StageMessageID:                int(input.StageMessageID),
		StageMessageRevision:          int(input.StageMessageRevision),
		TechnicalDifficultiesID:       int(input.TechnicalDifficultiesID),
		TechnicalDifficultiesRevision: int(input.TechnicalDifficultiesRevision),
		UrgentNoticeID:                int(input.UrgentNoticeID),
		UrgentNoticeRevision:          int(input.UrgentNoticeRevision),
		EmergencyAlertID:              int(input.EmergencyAlertID),
		EmergencyAlertRevision:        int(input.EmergencyAlertRevision),
		AppliedAt:                     service.now().UTC(), AppliedStandby: input.Standby,
		ClockOffsetMilliseconds:      input.ClockOffset,
		ClockUncertaintyMilliseconds: int64(input.ClockUncertainty),
		RendererUnstable:             input.RendererUnstable,
	})
	switch {
	case errors.Is(err, store.ErrDisplayCredential):
		return Acknowledgment{}, ErrDisplayAuthentication
	case errors.Is(err, store.ErrDisplayAcknowledgmentRegression):
		return Acknowledgment{}, ErrAcknowledgmentRegression
	case errors.Is(err, store.ErrDisplayAcknowledgmentConflict):
		return Acknowledgment{}, ErrAcknowledgmentConflict
	case err != nil:
		return Acknowledgment{}, err
	}
	if stored.StreamPosition < 0 || stored.ClockUncertaintyMilliseconds < 0 {
		return Acknowledgment{}, errors.New("stored Display acknowledgment values are invalid")
	}
	return Acknowledgment{
		DisplayID: stored.DisplayID, ProtocolVersion: stored.ProtocolVersion,
		AssetVersion: stored.AssetVersion,
		StreamID:     stored.StreamID, StreamPosition: uint64(stored.StreamPosition),
		ActiveEventID:        stored.ActiveEventID,
		ActivationGeneration: stored.ActivationGeneration,
		PublishedRevision:    stored.PublishedRevision, AppliedAt: stored.AppliedAt,
		StageMessageID: stored.StageMessageID, StageMessageRevision: stored.StageMessageRevision,
		TechnicalDifficultiesID:       stored.TechnicalDifficultiesID,
		TechnicalDifficultiesRevision: stored.TechnicalDifficultiesRevision,
		UrgentNoticeID:                stored.UrgentNoticeID,
		UrgentNoticeRevision:          stored.UrgentNoticeRevision,
		EmergencyAlertID:              stored.EmergencyAlertID,
		EmergencyAlertRevision:        stored.EmergencyAlertRevision,
		Standby:                       stored.AppliedStandby, ClockOffset: stored.ClockOffsetMilliseconds,
		ClockUncertainty: uint64(stored.ClockUncertaintyMilliseconds),
		RendererUnstable: stored.RendererUnstable,
	}, nil
}

// List returns current Display routing summaries to the Active Event's Crew Members.
func (service *Service) List(
	ctx context.Context,
	actor auth.Account,
	cursor displaystream.Cursor,
) ([]Status, error) {
	activeEventID, stored, err := service.storage.ListDisplayStatuses(actor.Context(ctx))
	if err != nil {
		return nil, err
	}
	if !actor.Administrator && !authz.Holds(actor.Identity(), activeEventID, authz.ViewEventCrew) {
		return nil, ErrCrewRequired
	}
	result := make([]Status, 0, len(stored))
	for _, item := range stored {
		result = append(result, status(item, cursor, service.now().UTC()))
	}
	return result, nil
}

// Assign commits one Event-specific Display route.
func (service *Service) Assign(
	ctx context.Context,
	actor auth.Account,
	input AssignInput,
) (Assignment, error) {
	input.ViewKey = strings.TrimSpace(input.ViewKey)
	input.DisplayGroupKeys = normalizeDisplayGroupKeys(input.DisplayGroupKeys)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Assignment{}, ErrInvalidDisplay
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(input.DisplayID), strconv.Itoa(input.EventID),
			strconv.Itoa(input.LocationID), input.ViewKey,
			strings.Join(input.DisplayGroupKeys, "\x00"),
		),
		Action: "AssignDisplay", TargetType: "Display",
		TargetID: strconv.Itoa(input.DisplayID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	result, err := command.Execute(ctx, command.Plan[Assignment]{
		Storage: service.storage, Identity: identity, Notify: service.notifyDisplays,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: displayRejections,
		},
		Replay: replayAssignment,
		Apply: func(transaction *store.CommandTx) (command.Execution[Assignment], error) {
			if input.DisplayID <= 0 || input.EventID <= 0 || input.LocationID <= 0 ||
				!displayviews.IsNormal(input.ViewKey) ||
				!ValidDisplayGroupKeys(input.DisplayGroupKeys) {
				return assignmentRejection(ErrInvalidDisplay), nil
			}
			stored, storeErr := transaction.AssignDisplay(actor.Context(ctx), store.DisplayAssignment{
				DisplayID: input.DisplayID, EventID: input.EventID,
				LocationID: input.LocationID, ViewKey: input.ViewKey,
				DisplayGroupKeys: input.DisplayGroupKeys,
			}, identity.Now)
			switch {
			case errors.Is(storeErr, store.ErrDisplayNotFound):
				return assignmentRejection(ErrDisplayNotFound), nil
			case errors.Is(storeErr, store.ErrDisplayAssignmentReference):
				return assignmentRejection(ErrAssignmentReference), nil
			case storeErr != nil:
				return command.Execution[Assignment]{}, storeErr
			}
			encoded, encodeErr := jsonMarshal(stored)
			if encodeErr != nil {
				return command.Execution[Assignment]{}, encodeErr
			}
			return command.Success(assignment(stored), encoded), nil
		},
	})
	if err != nil {
		return Assignment{}, restoreDisplayRejection(err)
	}
	return result, nil
}

// ClaimEnrollment consumes one code and persistently enrolls its Display.
func (service *Service) ClaimEnrollment(
	ctx context.Context,
	actor auth.Account,
	input ClaimInput,
) (Display, error) {
	code := normalizeEnrollmentCode(input.Code)
	name := strings.TrimSpace(input.Name)
	if err := command.ValidateID(input.CommandID); err != nil {
		return Display{}, ErrInvalidDisplay
	}
	targetID := "unidentified"
	if input.DisplayID > 0 {
		targetID = store.DisplayTargetID(input.DisplayID)
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(code, name, strconv.Itoa(input.DisplayID)),
		Action:      "EnrollDisplay",
		TargetType:  "Display", TargetID: targetID, Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	result, err := command.Execute(ctx, command.Plan[Display]{
		Storage: service.storage, Identity: identity, Notify: service.notifyDisplays,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: displayRejections,
		},
		Replay: replayDisplay,
		Apply: func(transaction *store.CommandTx) (command.Execution[Display], error) {
			if !validEnrollmentCode(code) ||
				input.DisplayID < 0 ||
				input.DisplayID == 0 && !ValidDisplayName(name) {
				return displayRejection(ErrInvalidDisplay), nil
			}
			created, createErr := transaction.ClaimDisplayEnrollment(
				actor.Context(ctx), digest(code), name, input.DisplayID, identity.Now,
			)
			switch {
			case errors.Is(createErr, store.ErrDisplayEnrollmentUnavailable):
				return displayRejection(ErrEnrollmentUnavailable), nil
			case errors.Is(createErr, store.ErrDisplayNotFound):
				return displayRejection(ErrDisplayNotFound), nil
			case errors.Is(createErr, store.ErrDisplayAlreadyEnrolled):
				return displayRejection(ErrDisplayAlreadyEnrolled), nil
			}
			if createErr != nil {
				return command.Execution[Display]{}, createErr
			}
			encoded, encodeErr := jsonMarshal(created)
			if encodeErr != nil {
				return command.Execution[Display]{}, encodeErr
			}
			return command.Success(display(created), encoded).
				WithTargetID(store.DisplayTargetID(created.ID)), nil
		},
	})
	if err != nil {
		return Display{}, restoreDisplayRejection(err)
	}
	return result, nil
}

// EnrollmentForBrowser reuses exact pending material or issues a fresh offer.
func (service *Service) EnrollmentForBrowser(
	ctx context.Context,
	code string,
	credential string,
) (Enrollment, error) {
	ctx = systemactor.NewContext(ctx, systemactor.Display)
	now := service.now().UTC()
	if validEnrollmentCode(code) && validDisplayToken(credential) {
		expiresAt, pending, err := service.storage.PendingDisplayEnrollment(
			ctx, digest(code), digest(credential), now,
		)
		if err != nil {
			return Enrollment{}, err
		}
		if pending {
			return Enrollment{
				Code: code, Credential: credential, ExpiresAt: expiresAt,
				CredentialExpires: now.Add(credentialLifetime),
			}, nil
		}
	}
	for range 3 {
		issued, err := service.newEnrollment(ctx, now)
		if errors.Is(err, store.ErrDisplayEnrollmentConflict) {
			continue
		}
		return issued, err
	}
	return Enrollment{}, errors.New("generate unique Display Enrollment")
}

func (service *Service) newEnrollment(ctx context.Context, now time.Time) (Enrollment, error) {
	codeBytes := make([]byte, enrollmentCodeBytes)
	if _, err := io.ReadFull(service.random, codeBytes); err != nil {
		return Enrollment{}, errors.New("generate Display Enrollment code")
	}
	encodedCode := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(codeBytes)
	code := encodedCode[:4] + "-" + encodedCode[4:]
	tokenBytes := make([]byte, displayTokenBytes)
	if _, err := io.ReadFull(service.random, tokenBytes); err != nil {
		return Enrollment{}, errors.New("generate Display credential")
	}
	credential := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := now.Add(service.enrollmentTTL)
	if err := service.storage.IssueDisplayEnrollment(ctx, store.DisplayEnrollmentParams{
		CodeHash: digest(code), CredentialHash: digest(credential), CreatedAt: now, ExpiresAt: expiresAt,
	}); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{
		Code: code, Credential: credential, ExpiresAt: expiresAt,
		CredentialExpires: now.Add(credentialLifetime),
	}, nil
}

func validEnrollmentCode(code string) bool {
	compact := strings.ReplaceAll(code, "-", "")
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(compact)
	return err == nil && len(decoded) == enrollmentCodeBytes &&
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded) == compact &&
		len(code) == 9 && code[4] == '-'
}

func normalizeEnrollmentCode(code string) string {
	compact := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
	if len(compact) != 8 {
		return code
	}
	return compact[:4] + "-" + compact[4:]
}

// ValidDisplayName reports whether name is safe to persist for one Display.
func ValidDisplayName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validDisplayToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == displayTokenBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func digest(value string) string {
	hashed := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hashed)
}

func display(found store.Display) Display {
	return Display{ID: found.ID, Name: found.Name, EnrolledAt: found.EnrolledAt}
}

func assignment(found store.DisplayAssignment) Assignment {
	return Assignment{
		DisplayID: found.DisplayID, EventID: found.EventID,
		LocationID: found.LocationID, ViewKey: found.ViewKey,
		DisplayGroupKeys: slices.Clone(found.DisplayGroupKeys),
	}
}

func normalizeDisplayGroupKeys(keys []string) []string {
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key != "" && !slices.Contains(result, key) {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

// ValidDisplayGroupKeys reports whether keys are safe Display Group identifiers.
func ValidDisplayGroupKeys(keys []string) bool {
	keys = normalizeDisplayGroupKeys(keys)
	if len(keys) > 100 {
		return false
	}
	for _, key := range keys {
		if len(key) > 100 {
			return false
		}
		for _, character := range key {
			if character >= 'a' && character <= 'z' ||
				character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' ||
				character == '-' || character == '_' || character == ':' {
				continue
			}
			return false
		}
	}
	return true
}

const (
	displayOfflineAfter      = 15 * time.Second
	excessiveClockSkew       = 250 * time.Millisecond
	unstableClockUncertainty = time.Second
)

func status(found store.DisplayStatus, cursor displaystream.Cursor, now time.Time) Status {
	result := Status{
		ID: found.ID, Name: found.Name, ActiveEventID: found.ActiveEventID, Standby: found.Standby,
		EventName: found.EventName, LocationName: found.LocationName, ViewKey: found.ViewKey,
		LocationID:                           found.LocationID,
		DisplayGroupKeys:                     found.DisplayGroupKeys,
		ProgramChannelID:                     found.ProgramChannelID,
		AppliedActiveEventID:                 found.AppliedActiveEventID,
		AppliedActivationGeneration:          found.AppliedActivationGeneration,
		AppliedPublishedRevision:             found.AppliedPublishedRevision,
		AppliedStageMessageID:                found.AppliedStageMessageID,
		AppliedStageMessageRevision:          found.AppliedStageMessageRevision,
		AppliedTechnicalDifficultiesID:       found.AppliedTechnicalDifficultiesID,
		AppliedTechnicalDifficultiesRevision: found.AppliedTechnicalDifficultiesRevision,
		AppliedUrgentNoticeID:                found.AppliedUrgentNoticeID,
		AppliedUrgentNoticeRevision:          found.AppliedUrgentNoticeRevision,
		AppliedEmergencyAlertID:              found.AppliedEmergencyAlertID,
		AppliedEmergencyAlertRevision:        found.AppliedEmergencyAlertRevision,
		AppliedStandby:                       found.AppliedStandby, AppliedAt: found.AppliedAt,
		ClockOffset:      found.ClockOffsetMilliseconds,
		ClockUncertainty: found.ClockUncertaintyMilliseconds,
	}
	result.DeliveryState = displayDeliveryState(found, cursor, now)
	return result
}

// Delivery states name how faithfully a Display is presenting the output it was
// assigned. They are part of the wire contract, so callers that classify a
// Display name these constants rather than restating the strings.
const (
	// DeliveryOffline means the Display has not acknowledged output recently.
	DeliveryOffline = "offline"
	// DeliveryUnstable means the Display renders or keeps time unreliably.
	DeliveryUnstable = "unstable"
	// DeliveryExcessivelySkewed means the Display's clock is too far out.
	DeliveryExcessivelySkewed = "excessively_skewed"
	// DeliveryLagging means the Display has not yet applied current output.
	DeliveryLagging = "lagging"
	// DeliveryApplied means the Display is presenting current output.
	DeliveryApplied = "applied"
)

func displayDeliveryState(found store.DisplayStatus, cursor displaystream.Cursor, now time.Time) string {
	if found.AppliedAt == nil || now.Sub(*found.AppliedAt) > displayOfflineAfter {
		return DeliveryOffline
	}
	if found.RendererUnstable ||
		time.Duration(found.ClockUncertaintyMilliseconds)*time.Millisecond > unstableClockUncertainty {
		return DeliveryUnstable
	}
	if abs64(found.ClockOffsetMilliseconds) > excessiveClockSkew.Milliseconds() {
		return DeliveryExcessivelySkewed
	}
	if found.AppliedProtocolVersion != protocolVersion ||
		found.AppliedAssetVersion != AssetVersion() ||
		found.AppliedStreamID != cursor.StreamID ||
		found.AppliedStreamPosition < 0 ||
		uint64(found.AppliedStreamPosition) < cursor.Position ||
		found.AppliedActiveEventID != found.ActiveEventID ||
		found.AppliedActivationGeneration != found.ActivationGeneration ||
		found.AppliedPublishedRevision != found.PublishedRevision ||
		found.AppliedStandby != found.Standby {
		return DeliveryLagging
	}
	return DeliveryApplied
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func displaySession(
	snapshot store.DisplaySnapshotState,
	found store.DisplaySessionState,
	now time.Time,
	zone *time.Location,
) (Session, bool, error) {
	if snapshot.ViewKey != displayviews.Timeline &&
		(found.Lifecycle == "Ended" || found.Lifecycle != "Live" && !found.ForecastEnd.After(now)) {
		return Session{}, false, nil
	}
	if snapshot.ViewKey == displayviews.EventOverview && found.AudienceVisibility != "Public" {
		return Session{}, false, nil
	}
	if (snapshot.ViewKey == displayviews.LocationSignage ||
		snapshot.ViewKey == displayviews.CrewOverview) &&
		!containsID(found.LocationIDs, snapshot.LocationID) {
		return Session{}, false, nil
	}
	// Crew Overview is a crew surface, so a Crew Only Session reaches it in full.
	// ADR 0022's example is exactly this: a Published, Crew Only soundcheck goes
	// Live without appearing on public Views, and the crew still has to see it.
	// Every other View reports only that the span is taken and until when.
	if found.AudienceVisibility != "Public" && snapshot.ViewKey != displayviews.CrewOverview {
		return Session{
			// ID stays the zero value: a suppressed Session's real ID is
			// redacted along with its title and speaker (see
			// SessionRotationKey for the distinct, non-leaking identity a
			// rotation-widget View uses instead).
			Unavailable: true,
			AvailabilityMessage: "Location unavailable until " +
				publictime.FormatDisplayDateTime(found.ForecastEnd, zone),
			// The span's edges size a Timeline block. They reveal nothing the
			// message does not already state, and without them a suppressed
			// Session would collapse to no width at all.
			ForecastStart: found.ForecastStart,
			ForecastEnd:   found.ForecastEnd,
			orderAt:       found.ForecastStart,
		}, true, nil
	}
	presentation, err := publictime.Present(found.PublicTime)
	if err != nil {
		return Session{}, false, err
	}
	return Session{
		ID: found.ID, Title: found.Title, Speaker: found.Speaker, PublicDetails: found.PublicDetails,
		ForecastStart: found.ForecastStart, ForecastEnd: found.ForecastEnd,
		Lifecycle: found.Lifecycle, LiveStateRevision: found.LiveStateRevision,
		ActualStart: found.ActualStart, ActualEnd: found.ActualEnd,
		LocationIDs: found.LocationIDs, LaneIDs: found.LaneIDs, TrackIDs: found.TrackIDs,
		PresentedStart: presentation.Start.Time, PresentedEnd: presentation.End.Time,
		PresentedStartLabel: presentation.Start.Label, PresentedEndLabel: presentation.End.Label,
		DisplayPresentedStart: publictime.FormatDisplayDateTime(presentation.Start.Time, zone),
		DisplayPresentedEnd:   publictime.FormatDisplayDateTime(presentation.End.Time, zone),
		orderAt:               found.ForecastStart,
	}, true, nil
}

func containsID(ids []int, selected int) bool {
	return slices.Contains(ids, selected)
}

func displayRejection(reason error) command.Execution[Display] {
	return command.Reject(Display{}, displayCommandRejection(reason), reason)
}

func assignmentRejection(reason error) command.Execution[Assignment] {
	return command.Reject(Assignment{}, displayCommandRejection(reason), reason)
}

// displayRejections is the single source for Display command rejection codes
// in both directions.
var displayRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
		{Err: ErrEnrollmentUnavailable, Code: "enrollment_unavailable"},
		{Err: ErrDisplayNotFound, Code: "display_not_found"},
		{Err: ErrDisplayAlreadyEnrolled, Code: "display_already_enrolled"},
		{Err: ErrAssignmentReference, Code: "assignment_reference"},
		{Err: ErrInvalidDisplay, Code: "invalid_display"},
	},
}

func displayCommandRejection(reason error) store.CommandRejection {
	rejection, known := displayRejections.Rejection(reason)
	if !known {
		return store.CommandRejection{Code: "invalid_display"}
	}
	return rejection
}

func replayDisplay(outcome string) (Display, error) {
	var found store.Display
	if err := store.DecodeCommandReceipt(outcome, &found); err != nil {
		return Display{}, restoreDisplayRejection(err)
	}
	return display(found), nil
}

func replayAssignment(outcome string) (Assignment, error) {
	var found store.DisplayAssignment
	if err := store.DecodeCommandReceipt(outcome, &found); err != nil {
		return Assignment{}, restoreDisplayRejection(err)
	}
	return assignment(found), nil
}

func restoreDisplayRejection(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	if sentinel := displayRejections.Sentinel(rejected.Rejection.Code); sentinel != nil {
		return sentinel
	}
	return errors.New("Display command unavailable")
}

func jsonMarshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("encode Display command outcome")
	}
	return string(encoded), nil
}

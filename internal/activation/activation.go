// Package activation preflights and designates the installation's Active Event.
package activation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrAdministratorRequired means Active Event selection lacked installation authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrEventNotFound means the requested Event does not exist.
	ErrEventNotFound = store.ErrEventNotFound
	// ErrPreflightBlocked means one or more blocking findings prevent activation.
	ErrPreflightBlocked = errors.New("activation preflight is blocked")
	// ErrStalePreflight means durable Event or Published Rundown state changed after Preflight.
	ErrStalePreflight = store.ErrActivationRevisionConflict
	// ErrCommandConflict means a Command ID was reused for different activation work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Finding is one stable actionable Activation Preflight result.
type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Confirmation binds activation to the exact preflighted Event state.
type Confirmation struct {
	EventRevision        int    `json:"event_revision"`
	PublishedRevision    int    `json:"published_revision"`
	ActivationGeneration int    `json:"activation_generation"`
	Fingerprint          string `json:"fingerprint"`
}

// Preflight is the complete readiness review for one Event.
type Preflight struct {
	EventID      int          `json:"event_id"`
	Confirmation Confirmation `json:"confirmation"`
	Blockers     []Finding    `json:"blockers"`
	Warnings     []Finding    `json:"warnings"`
}

// ActivateInput is one exact confirmed Active Event command.
type ActivateInput struct {
	EventID      int          `json:"event_id"`
	CommandID    string       `json:"command_id"`
	Confirmation Confirmation `json:"confirmation"`
}

// ActiveEvent identifies current live authority routing.
type ActiveEvent struct {
	EventID    int `json:"event_id"`
	Generation int `json:"generation"`
}

// Service owns Activation Preflight and Active Event commands.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates an Activation service with explicit persistence and clock dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("activation storage is required")
	}
	if now == nil {
		return nil, errors.New("activation clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Preflight reports blocking structural failures and operational warnings.
func (service *Service) Preflight(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (Preflight, error) {
	if !actor.Administrator {
		return Preflight{}, ErrAdministratorRequired
	}
	state, err := service.storage.LoadActivationPreflight(actor.Context(ctx), eventID)
	if err != nil {
		return Preflight{}, err
	}
	return formPreflight(state, service.now().UTC()), nil
}

// Activate atomically changes live authority and records one new generation and Audit Entry.
func (service *Service) Activate(
	ctx context.Context,
	actor auth.Account,
	input ActivateInput,
) (ActiveEvent, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return ActiveEvent{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ActiveEvent{}, errors.New("encode Activate Event command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "ActivateEvent",
		TargetType: "Event", TargetID: strconv.Itoa(input.EventID), Now: service.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[ActiveEvent]{
		Storage: service.storage, Identity: identity,
		Replay: func(original string) (ActiveEvent, error) {
			var result ActiveEvent
			if decodeErr := store.DecodeCommandReceipt(original, &result); decodeErr != nil {
				return ActiveEvent{}, activationError(decodeErr)
			}
			return result, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[ActiveEvent], error) {
			if !actor.Administrator {
				return activationRejection("administrator_required", ErrAdministratorRequired), nil
			}
			state, loadErr := transaction.LoadActivationPreflight(actor.Context(ctx), input.EventID)
			if errors.Is(loadErr, ErrEventNotFound) {
				return activationRejection("event_not_found", ErrEventNotFound), nil
			}
			if loadErr != nil {
				return command.Execution[ActiveEvent]{}, loadErr
			}
			preflight := formPreflight(state, identity.Now)
			if len(preflight.Blockers) > 0 {
				return activationRejection("preflight_blocked", ErrPreflightBlocked), nil
			}
			if input.Confirmation != preflight.Confirmation {
				return activationRejection("stale_preflight", ErrStalePreflight), nil
			}
			stored, activateErr := transaction.ActivateEvent(
				actor.Context(ctx), input.EventID,
				input.Confirmation.EventRevision, input.Confirmation.PublishedRevision,
				input.Confirmation.ActivationGeneration,
			)
			if errors.Is(activateErr, store.ErrActivationRevisionConflict) {
				return activationRejection("stale_preflight", ErrStalePreflight), nil
			}
			if activateErr != nil {
				return command.Execution[ActiveEvent]{}, activateErr
			}
			result := ActiveEvent{EventID: stored.EventID, Generation: stored.Generation}
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[ActiveEvent]{}, errors.New("encode Activate Event outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

// ActiveEvent returns current live authority routing to an Administrator.
func (service *Service) ActiveEvent(ctx context.Context, actor auth.Account) (ActiveEvent, error) {
	if !actor.Administrator {
		return ActiveEvent{}, ErrAdministratorRequired
	}
	found, err := service.storage.LoadActiveEvent(actor.Context(ctx))
	if err != nil {
		return ActiveEvent{}, err
	}
	return ActiveEvent{EventID: found.EventID, Generation: found.Generation}, nil
}

func activationRejection(code string, reason error) command.Execution[ActiveEvent] {
	return command.Reject(
		ActiveEvent{}, store.CommandRejection{Code: code, Message: reason.Error()}, reason,
	)
}

func formPreflight(state store.ActivationPreflightState, now time.Time) Preflight {
	result := Preflight{EventID: state.EventID}
	result.Confirmation.EventRevision = state.EventRevision
	result.Confirmation.PublishedRevision = state.PublishedRundown.PublishedRevision
	result.Confirmation.ActivationGeneration = state.ActivationGeneration
	result.Blockers = structuralBlockers(state)
	result.Warnings = operationalWarnings(state, now)
	fingerprintMaterial := []string{
		strconv.Itoa(state.EventID),
		strconv.Itoa(state.EventRevision),
		strconv.Itoa(state.PublishedRundown.PublishedRevision),
		strconv.Itoa(state.ActivationGeneration),
		strconv.FormatBool(state.PublicScheduleBaselineCaptured),
		state.PlannedStartDate,
		state.PlannedEndDate,
		state.Timezone,
	}
	for _, display := range state.UnassignedDisplays {
		fingerprintMaterial = append(fingerprintMaterial, strconv.Itoa(display.ID), display.Name)
	}
	for _, assignment := range state.DisplayAssignments {
		fingerprintMaterial = append(fingerprintMaterial,
			strconv.Itoa(assignment.DisplayID), strconv.Itoa(assignment.EventID),
			strconv.Itoa(assignment.LocationID), assignment.ViewKey,
		)
	}
	result.Confirmation.Fingerprint = command.PayloadHash(fingerprintMaterial...)
	return result
}

func structuralBlockers(state store.ActivationPreflightState) []Finding {
	var blockers []Finding
	if state.PublishedRundown.PublishedRevision == 0 || len(state.PublishedRundown.Locations) == 0 || len(state.PublishedRundown.Lanes) == 0 {
		return []Finding{{Code: "published_rundown_missing", Message: "a Published Rundown with a Location and Lane is required"}}
	}
	if state.Timezone == "Local" || state.Timezone == "" ||
		strings.HasPrefix(state.Timezone, "/") || strings.Contains(state.Timezone, "\\") {
		blockers = append(blockers, Finding{Code: "timezone_invalid", Message: "Event timezone cannot be loaded"})
	} else if _, err := time.LoadLocation(state.Timezone); err != nil {
		blockers = append(blockers, Finding{Code: "timezone_invalid", Message: "Event timezone cannot be loaded"})
	}
	locations := make(map[int]struct{}, len(state.PublishedRundown.Locations))
	for _, location := range state.PublishedRundown.Locations {
		locations[location.ID] = struct{}{}
	}
	for _, assignment := range state.DisplayAssignments {
		if _, ok := locations[assignment.LocationID]; !ok || !displayviews.IsNormal(assignment.ViewKey) {
			blockers = append(blockers, Finding{
				Code:    "display_assignment_invalid",
				Message: "each Display Assignment requires a Published Location and normal View",
			})
			break
		}
	}
	lanes := make(map[int]struct{}, len(state.PublishedRundown.Lanes))
	tracks := make(map[int]struct{}, len(state.PublishedRundown.Tracks))
	for _, track := range state.PublishedRundown.Tracks {
		tracks[track.ID] = struct{}{}
	}
	for _, lane := range state.PublishedRundown.Lanes {
		lanes[lane.ID] = struct{}{}
		if _, ok := locations[lane.LocationID]; !ok {
			blockers = append(blockers, Finding{
				Code:    "published_reference_invalid",
				Message: fmt.Sprintf("Lane %d references an unavailable Location", lane.ID),
			})
		}
	}
	for _, session := range state.PublishedRundown.Sessions {
		if !validPublishedSession(session) {
			blockers = append(blockers, Finding{
				Code:    "published_structure_invalid",
				Message: fmt.Sprintf("Session %d contains invalid Published state", session.ID),
			})
		}
		if len(session.LaneIDs) == 0 || len(session.LocationIDs) == 0 {
			blockers = append(blockers, Finding{
				Code:    "published_reference_invalid",
				Message: fmt.Sprintf("Session %d requires a Lane and Location", session.ID),
			})
			continue
		}
		for _, laneID := range session.LaneIDs {
			if _, ok := lanes[laneID]; !ok {
				blockers = append(blockers, Finding{
					Code:    "published_reference_invalid",
					Message: fmt.Sprintf("Session %d references unavailable Lane %d", session.ID, laneID),
				})
			}
		}
		for _, locationID := range session.LocationIDs {
			if _, ok := locations[locationID]; !ok {
				blockers = append(blockers, Finding{
					Code:    "published_reference_invalid",
					Message: fmt.Sprintf("Session %d references unavailable Location %d", session.ID, locationID),
				})
			}
		}
		for _, trackID := range session.TrackIDs {
			if _, ok := tracks[trackID]; !ok {
				blockers = append(blockers, Finding{
					Code:    "published_reference_invalid",
					Message: fmt.Sprintf("Session %d references unavailable Track %d", session.ID, trackID),
				})
			}
		}
	}
	return blockers
}

func validPublishedSession(session store.PublishedSession) bool {
	const maximumDurationSeconds = int64((1<<63 - 1) / time.Second)
	if int64(session.MinimumDurationSeconds) > maximumDurationSeconds {
		return false
	}
	return rundown.ValidateSessionScalars(rundown.SessionDraftInput{
		Title: session.Title, Speaker: session.Speaker, Type: rundown.SessionType(session.Type),
		AudienceVisibility: rundown.AudienceVisibility(session.AudienceVisibility),
		PlannedStart:       session.PlannedStart, PlannedEnd: session.PlannedEnd,
		TimingPolicy:       rundown.TimingPolicy(session.TimingPolicy),
		MinimumDuration:    time.Duration(session.MinimumDurationSeconds) * time.Second,
		StartBoundary:      rundown.Boundary(session.StartBoundary),
		EndBoundary:        rundown.Boundary(session.EndBoundary),
		SubmissionDeadline: session.SubmissionDeadline,
		EntryDefault:       rundown.EntryDisposition(session.EntryDefaultDisposition),
	}) == nil
}

func operationalWarnings(state store.ActivationPreflightState, now time.Time) []Finding {
	var warnings []Finding
	if !state.PublicScheduleBaselineCaptured {
		warnings = append(warnings, Finding{
			Code:    "public_schedule_baseline_missing",
			Message: "Public Schedule Baseline has not been captured; attendee views cannot show “Was:” timing context",
		})
	}
	for _, display := range state.UnassignedDisplays {
		warnings = append(warnings, Finding{
			Code: "unassigned_display", Message: fmt.Sprintf("Display %s has no Assignment for this Event", display.Name),
		})
	}
	zone, zoneErr := time.LoadLocation(state.Timezone)
	if zoneErr == nil {
		seenDates := make(map[string]struct{})
		for _, session := range state.PublishedRundown.Sessions {
			local := session.PlannedStart.In(zone)
			date := local.Format(time.DateOnly)
			if _, exists := seenDates[date]; exists {
				continue
			}
			seenDates[date] = struct{}{}
			resolved, boundaryErr := events.ResolveDayBoundary(local, zone, state.EventDayBoundary)
			if boundaryErr == nil {
				warnings = append(warnings, Finding{
					Code:    "event_day_boundary_resolved",
					Message: fmt.Sprintf("Event Day Boundary for %s resolves to %s", date, resolved.In(zone).Format(time.RFC3339)),
				})
			}
		}
	}
	sessionsByLane := make(map[int]int)
	for _, session := range state.PublishedRundown.Sessions {
		for _, laneID := range session.LaneIDs {
			sessionsByLane[laneID]++
		}
	}
	for _, lane := range state.PublishedRundown.Lanes {
		if sessionsByLane[lane.ID] == 0 {
			warnings = append(warnings, Finding{
				Code: "empty_lane", Message: fmt.Sprintf("Lane %d has no Published Sessions", lane.ID),
			})
		}
	}
	start, startErr := time.Parse(time.DateOnly, state.PlannedStartDate)
	end, endErr := time.Parse(time.DateOnly, state.PlannedEndDate)
	today := eventCalendarDate(now, state.Timezone)
	if startErr != nil || endErr != nil || today.Before(start) || today.After(end) {
		warnings = append(warnings, Finding{
			Code: "suspicious_dates", Message: "current date is outside the Event's Planned Date Range",
		})
	}
	return warnings
}

func eventCalendarDate(now time.Time, timezone string) time.Time {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	year, month, day := now.In(location).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func activationError(err error) error {
	var rejected *store.RejectedCommandError
	if !errors.As(err, &rejected) {
		return err
	}
	switch rejected.Rejection.Code {
	case "administrator_required":
		return ErrAdministratorRequired
	case "preflight_blocked":
		return ErrPreflightBlocked
	case "stale_preflight":
		return ErrStalePreflight
	case "event_not_found":
		return ErrEventNotFound
	default:
		return err
	}
}

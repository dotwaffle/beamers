// Package rundown stages and publishes Event structure through deep command and query APIs.
package rundown

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrEventAccessDenied means the actor cannot produce the Event Rundown.
	ErrEventAccessDenied = errors.New("event access denied")
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrDraftRevisionConflict means the proposed Draft base is no longer valid.
	ErrDraftRevisionConflict = store.ErrDraftRevisionConflict
)

// DraftRevisionConflictError describes current overlapping facts for a stale edit.
type DraftRevisionConflictError = store.DraftRevisionConflictError

// ValidationError describes one actionable invalid Draft field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (err *ValidationError) Error() string {
	return err.Field + ": " + err.Message
}

// TargetRef identifies an existing stable identity or a creation in the same Draft Edit.
type TargetRef struct {
	ID  int    `json:"id,omitempty"`
	Ref string `json:"ref,omitempty"`
}

// LocationDraftInput creates one Location in shared Draft state.
type LocationDraftInput struct {
	ID           int      `json:"id,omitempty"`
	Ref          string   `json:"ref"`
	Name         string   `json:"name"`
	UpdateFields []string `json:"update_fields,omitempty"`
}

// LaneDraftInput creates one Lane in shared Draft state.
type LaneDraftInput struct {
	ID           int       `json:"id,omitempty"`
	Ref          string    `json:"ref"`
	Name         string    `json:"name"`
	Location     TargetRef `json:"location"`
	UpdateFields []string  `json:"update_fields,omitempty"`
}

// TrackDraftInput creates one Track in shared Draft state.
type TrackDraftInput struct {
	ID           int      `json:"id,omitempty"`
	Ref          string   `json:"ref"`
	Name         string   `json:"name"`
	UpdateFields []string `json:"update_fields,omitempty"`
}

// SessionType is one supported version-one Session type.
type SessionType string

const (
	// SessionPresentation is a talk or other presented content.
	SessionPresentation SessionType = "Presentation"
	// SessionCompetition is a judged or scored activity.
	SessionCompetition SessionType = "Competition"
	// SessionBreak is a planned pause in programmed content.
	SessionBreak SessionType = "Break"
	// SessionActivity is participatory programmed content.
	SessionActivity SessionType = "Activity"
	// SessionCeremony is formal programmed content.
	SessionCeremony SessionType = "Ceremony"
	// SessionPerformance is staged entertainment.
	SessionPerformance SessionType = "Performance"
	// SessionHold reserves time without committed content.
	SessionHold SessionType = "Hold"
)

// AudienceVisibility controls whether a Published Session reaches public projections.
type AudienceVisibility string

const (
	// AudiencePublic permits the Session to appear in public projections.
	AudiencePublic AudienceVisibility = "Public"
	// AudienceCrewOnly confines the Session to crew projections.
	AudienceCrewOnly AudienceVisibility = "CrewOnly"
)

// TimingPolicy determines a live Session's target behavior.
type TimingPolicy string

const (
	// TimingFixedEnd preserves the planned end when live timing changes.
	TimingFixedEnd TimingPolicy = "FixedEnd"
	// TimingFixedDuration preserves the planned duration when live timing changes.
	TimingFixedDuration TimingPolicy = "FixedDuration"
	// TimingManualEnd requires an operator to end the Session.
	TimingManualEnd TimingPolicy = "ManualEnd"
)

// Boundary controls whether automatic timing changes may move one Session edge.
type Boundary string

const (
	// BoundaryHard prevents automatic timing changes from moving an edge.
	BoundaryHard Boundary = "Hard"
	// BoundarySoft permits automatic timing changes to move an edge.
	BoundarySoft Boundary = "Soft"
)

// SessionDraftInput creates one Session in shared Draft state.
type SessionDraftInput struct {
	ID                 int                `json:"id,omitempty"`
	Ref                string             `json:"ref"`
	Title              string             `json:"title"`
	Speaker            string             `json:"speaker,omitempty"`
	Type               SessionType        `json:"type"`
	AudienceVisibility AudienceVisibility `json:"audience_visibility"`
	PublicDetails      string             `json:"public_details,omitempty"`
	CrewNotes          string             `json:"crew_notes,omitempty"`
	PlannedStart       time.Time          `json:"planned_start"`
	PlannedEnd         time.Time          `json:"planned_end"`
	TimingPolicy       TimingPolicy       `json:"timing_policy"`
	MinimumDuration    time.Duration      `json:"minimum_duration"`
	StartBoundary      Boundary           `json:"start_boundary"`
	EndBoundary        Boundary           `json:"end_boundary"`
	Lanes              []TargetRef        `json:"lanes"`
	Locations          []TargetRef        `json:"locations"`
	Tracks             []TargetRef        `json:"tracks,omitempty"`
	AddLanes           []TargetRef        `json:"add_lanes,omitempty"`
	RemoveLanes        []TargetRef        `json:"remove_lanes,omitempty"`
	AddLocations       []TargetRef        `json:"add_locations,omitempty"`
	RemoveLocations    []TargetRef        `json:"remove_locations,omitempty"`
	AddTracks          []TargetRef        `json:"add_tracks,omitempty"`
	RemoveTracks       []TargetRef        `json:"remove_tracks,omitempty"`
	UpdateFields       []string           `json:"update_fields,omitempty"`
}

// EditDraftInput is one atomic set of structural creations.
type EditDraftInput struct {
	EventID               int                  `json:"event_id"`
	CommandID             string               `json:"command_id"`
	ExpectedDraftRevision int                  `json:"expected_draft_revision"`
	Locations             []LocationDraftInput `json:"locations,omitempty"`
	Lanes                 []LaneDraftInput     `json:"lanes,omitempty"`
	Tracks                []TrackDraftInput    `json:"tracks,omitempty"`
	Sessions              []SessionDraftInput  `json:"sessions,omitempty"`
}

// DraftChange identifies one independently publishable effective change.
type DraftChange struct {
	ID               int    `json:"id"`
	Kind             string `json:"kind"`
	TargetType       string `json:"target_type"`
	TargetID         int    `json:"target_id"`
	FactKey          string `json:"fact_key,omitempty"`
	Status           string `json:"status,omitempty"`
	CurrentValueJSON string `json:"current_value_json,omitempty"`
}

// EditDraftResult is the minimal committed result of one Draft Edit.
type EditDraftResult struct {
	DraftRevision int           `json:"draft_revision"`
	Changes       []DraftChange `json:"changes"`
}

// Commands owns Rundown state transitions and their durable evidence.
type Commands struct {
	storage *store.SQLite
	now     func() time.Time
}

// NewCommands creates Rundown Commands with explicit persistence and clock dependencies.
func NewCommands(storage *store.SQLite, now func() time.Time) (*Commands, error) {
	if storage == nil {
		return nil, errors.New("rundown storage is required")
	}
	if now == nil {
		return nil, errors.New("rundown clock is required")
	}
	return &Commands{storage: storage, now: now}, nil
}

type editDraftOutcome struct {
	Result    *EditDraftResult `json:"result,omitempty"`
	Rejection *rejection       `json:"rejection,omitempty"`
}

type rejection struct {
	Code                 string        `json:"code"`
	Field                string        `json:"field,omitempty"`
	Message              string        `json:"message,omitempty"`
	CurrentDraftRevision int           `json:"current_draft_revision,omitempty"`
	OverlappingChanges   []DraftChange `json:"overlapping_changes,omitempty"`
}

// EditDraft validates and atomically records one set of structural creations.
func (commands *Commands) EditDraft(
	ctx context.Context,
	actor auth.Account,
	input EditDraftInput,
) (EditDraftResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return EditDraftResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return EditDraftResult{}, errors.New("encode Edit Draft command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "EditDraft",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            commands.now().UTC(),
	}
	return command.Execute(actor.Context(ctx), command.Plan[EditDraftResult]{
		Storage: commands.storage, Identity: identity, Replay: decodeEditDraftOutcome,
		Apply: func(transaction *store.CommandTx) (command.Execution[EditDraftResult], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return editDraftRejection(rejection{Code: "event_access_denied", Message: ErrEventAccessDenied.Error()})
			}
			normalized, validationErr := validateEditDraft(input)
			if validationErr != nil {
				var invalid *ValidationError
				_ = errors.As(validationErr, &invalid)
				return editDraftRejection(rejection{Code: "validation", Field: invalid.Field, Message: invalid.Message})
			}
			stored, editErr := transaction.EditDraft(actor.Context(ctx), editDraftParams(actor.ID, normalized, identity.Now))
			if errors.Is(editErr, store.ErrDraftReference) {
				return editDraftRejection(rejection{
					Code: "validation", Field: "references", Message: "must identify Draft structure in this Event",
				})
			}
			if errors.Is(editErr, ErrDraftRevisionConflict) {
				var conflict *store.DraftRevisionConflictError
				_ = errors.As(editErr, &conflict)
				rejected := rejection{Code: "draft_revision_conflict", Message: ErrDraftRevisionConflict.Error()}
				if conflict != nil {
					rejected.CurrentDraftRevision = conflict.CurrentDraftRevision
					rejected.OverlappingChanges = editDraftResult(store.EditDraftResult{Changes: conflict.OverlappingChanges}).Changes
				}
				return editDraftRejection(rejected)
			}
			if editErr != nil {
				return command.Execution[EditDraftResult]{}, editErr
			}
			result := editDraftResult(stored)
			encoded, encodeErr := json.Marshal(editDraftOutcome{Result: &result})
			if encodeErr != nil {
				return command.Execution[EditDraftResult]{}, errors.New("encode Edit Draft outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func editDraftRejection(rejected rejection) (command.Execution[EditDraftResult], error) {
	encoded, err := json.Marshal(editDraftOutcome{Rejection: &rejected})
	if err != nil {
		return command.Execution[EditDraftResult]{}, errors.New("encode rejected Edit Draft outcome")
	}
	return command.RejectEncoded(EditDraftResult{}, string(encoded), rejectionError(rejected)), nil
}

func decodeEditDraftOutcome(encoded string) (EditDraftResult, error) {
	var outcome editDraftOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return EditDraftResult{}, errors.New("decode Edit Draft Command Receipt")
	}
	if outcome.Rejection != nil {
		return EditDraftResult{}, rejectionError(*outcome.Rejection)
	}
	if outcome.Result == nil {
		return EditDraftResult{}, errors.New("edit Draft Command Receipt has no outcome")
	}
	return *outcome.Result, nil
}

func rejectionError(rejected rejection) error {
	switch rejected.Code {
	case "event_access_denied":
		return ErrEventAccessDenied
	case "draft_revision_conflict":
		if rejected.CurrentDraftRevision > 0 || len(rejected.OverlappingChanges) > 0 {
			changes := make([]store.DraftChangeResult, 0, len(rejected.OverlappingChanges))
			for _, change := range rejected.OverlappingChanges {
				changes = append(changes, store.DraftChangeResult{
					ID: change.ID, Kind: change.Kind, TargetType: change.TargetType, TargetID: change.TargetID,
					FactKey: change.FactKey, Status: change.Status,
					CurrentValueJSON: change.CurrentValueJSON,
				})
			}
			return &store.DraftRevisionConflictError{CurrentDraftRevision: rejected.CurrentDraftRevision, OverlappingChanges: changes}
		}
		return ErrDraftRevisionConflict
	case "validation":
		return &ValidationError{Field: rejected.Field, Message: rejected.Message}
	default:
		return errors.New("edit Draft command was rejected")
	}
}

func validateEditDraft(input EditDraftInput) (EditDraftInput, error) {
	if input.EventID <= 0 {
		return EditDraftInput{}, invalid("event_id", "must identify an Event")
	}
	if input.ExpectedDraftRevision < 0 {
		return EditDraftInput{}, invalid("expected_draft_revision", "must not be negative")
	}
	if len(input.Locations)+len(input.Lanes)+len(input.Tracks)+len(input.Sessions) == 0 {
		return EditDraftInput{}, invalid("changes", "must contain at least one structural change")
	}
	locationRefs, err := validateNamedRefs("locations", input.Locations, func(item LocationDraftInput) (int, string, string, []string) {
		return item.ID, item.Ref, item.Name, item.UpdateFields
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	laneRefs, err := validateNamedRefs("lanes", input.Lanes, func(item LaneDraftInput) (int, string, string, []string) {
		return item.ID, item.Ref, item.Name, item.UpdateFields
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	trackRefs, err := validateNamedRefs("tracks", input.Tracks, func(item TrackDraftInput) (int, string, string, []string) {
		return item.ID, item.Ref, item.Name, item.UpdateFields
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	for index := range input.Locations {
		input.Locations[index].Name = strings.TrimSpace(input.Locations[index].Name)
		if err := validateUpdateFields("locations", input.Locations[index].ID, input.Locations[index].UpdateFields, store.RundownDraftUpdateFields("Location")...); err != nil {
			return EditDraftInput{}, err
		}
	}
	for index := range input.Lanes {
		input.Lanes[index].Name = strings.TrimSpace(input.Lanes[index].Name)
		if err := validateUpdateFields("lanes", input.Lanes[index].ID, input.Lanes[index].UpdateFields, store.RundownDraftUpdateFields("Lane")...); err != nil {
			return EditDraftInput{}, err
		}
		if input.Lanes[index].ID == 0 || contains(input.Lanes[index].UpdateFields, "location") {
			if err := validateTarget("lanes.location", input.Lanes[index].Location, locationRefs); err != nil {
				return EditDraftInput{}, err
			}
		}
	}
	for index := range input.Tracks {
		input.Tracks[index].Name = strings.TrimSpace(input.Tracks[index].Name)
		if err := validateUpdateFields("tracks", input.Tracks[index].ID, input.Tracks[index].UpdateFields, store.RundownDraftUpdateFields("Track")...); err != nil {
			return EditDraftInput{}, err
		}
	}
	sessionRefs := make(map[string]struct{}, len(input.Sessions))
	for index := range input.Sessions {
		item := &input.Sessions[index]
		if item.ID == 0 {
			if err := validateRef("sessions.ref", item.Ref, sessionRefs); err != nil {
				return EditDraftInput{}, err
			}
		} else if item.Ref != "" {
			return EditDraftInput{}, invalid("sessions.ref", "must be empty for an update")
		}
		if err := validateUpdateFields(
			"sessions", item.ID, item.UpdateFields, store.RundownDraftUpdateFields("Session")...,
		); err != nil {
			return EditDraftInput{}, err
		}
		item.Title = strings.TrimSpace(item.Title)
		item.Speaker = strings.TrimSpace(item.Speaker)
		if item.ID == 0 {
			if err := ValidateSessionScalars(*item); err != nil {
				return EditDraftInput{}, err
			}
		} else if contains(item.UpdateFields, "title") && !validText(item.Title, 200) {
			return EditDraftInput{}, invalid("sessions.title", "must be 1 to 200 characters without control characters")
		}
		if (item.ID == 0 || contains(item.UpdateFields, "speaker")) && utf8.RuneCountInString(item.Speaker) > 200 {
			return EditDraftInput{}, invalid("sessions.speaker", "must not exceed 200 characters")
		}
		if item.ID > 0 && contains(item.UpdateFields, "public_details") && utf8.RuneCountInString(item.PublicDetails) > 10000 {
			return EditDraftInput{}, invalid("sessions.public_details", "must not exceed 10000 characters")
		}
		if item.ID > 0 && contains(item.UpdateFields, "crew_notes") && utf8.RuneCountInString(item.CrewNotes) > 10000 {
			return EditDraftInput{}, invalid("sessions.crew_notes", "must not exceed 10000 characters")
		}
		if item.ID > 0 && contains(item.UpdateFields, "type") && !validSessionType(item.Type) {
			return EditDraftInput{}, invalid("sessions.type", "must be a supported version-one Session type")
		}
		if item.ID > 0 && contains(item.UpdateFields, "audience_visibility") && item.AudienceVisibility != AudiencePublic && item.AudienceVisibility != AudienceCrewOnly {
			return EditDraftInput{}, invalid("sessions.audience_visibility", "must be Public or CrewOnly")
		}
		if item.ID > 0 && contains(item.UpdateFields, "timing_policy") && item.TimingPolicy != TimingFixedEnd && item.TimingPolicy != TimingFixedDuration && item.TimingPolicy != TimingManualEnd {
			return EditDraftInput{}, invalid("sessions.timing_policy", "must be FixedEnd, FixedDuration, or ManualEnd")
		}
		if item.ID > 0 && contains(item.UpdateFields, "start_boundary") && !validBoundary(item.StartBoundary) {
			return EditDraftInput{}, invalid("sessions.start_boundary", "must be Hard or Soft")
		}
		if item.ID > 0 && contains(item.UpdateFields, "end_boundary") && !validBoundary(item.EndBoundary) {
			return EditDraftInput{}, invalid("sessions.end_boundary", "must be Hard or Soft")
		}
		if item.ID > 0 && contains(item.UpdateFields, "minimum_duration") && item.MinimumDuration < 0 {
			return EditDraftInput{}, invalid("sessions.minimum_duration", "must not be negative")
		}
		if item.ID == 0 && len(item.Lanes) == 0 {
			return EditDraftInput{}, invalid("sessions.lanes", "must include at least one Lane")
		}
		for _, target := range item.Lanes {
			if err := validateTarget("sessions.lanes", target, laneRefs); err != nil {
				return EditDraftInput{}, err
			}
		}
		for _, target := range item.Locations {
			if err := validateTarget("sessions.locations", target, locationRefs); err != nil {
				return EditDraftInput{}, err
			}
		}
		for _, target := range item.Tracks {
			if err := validateTarget("sessions.tracks", target, trackRefs); err != nil {
				return EditDraftInput{}, err
			}
		}
		for _, targets := range [][]TargetRef{item.AddLanes, item.RemoveLanes} {
			for _, target := range targets {
				if err := validateTarget("sessions.lanes", target, laneRefs); err != nil {
					return EditDraftInput{}, err
				}
			}
		}
		for _, targets := range [][]TargetRef{item.AddLocations, item.RemoveLocations} {
			for _, target := range targets {
				if err := validateTarget("sessions.locations", target, locationRefs); err != nil {
					return EditDraftInput{}, err
				}
			}
		}
		for _, targets := range [][]TargetRef{item.AddTracks, item.RemoveTracks} {
			for _, target := range targets {
				if err := validateTarget("sessions.tracks", target, trackRefs); err != nil {
					return EditDraftInput{}, err
				}
			}
		}
		for _, membership := range []struct {
			field       string
			add, remove []TargetRef
		}{{"lanes", item.AddLanes, item.RemoveLanes}, {"locations", item.AddLocations, item.RemoveLocations}, {"tracks", item.AddTracks, item.RemoveTracks}} {
			if err := validateMembershipOperations("sessions."+membership.field, membership.add, membership.remove); err != nil {
				return EditDraftInput{}, err
			}
		}
	}
	return input, nil
}

func validateMembershipOperations(field string, additions, removals []TargetRef) error {
	seen := make(map[string]struct{}, len(additions)+len(removals))
	for _, target := range append(append([]TargetRef(nil), additions...), removals...) {
		key := target.Ref
		if target.ID > 0 {
			key = strconv.Itoa(target.ID)
		}
		if _, exists := seen[key]; exists {
			return invalid(field, "must not repeat or both add and remove one member")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateSessionScalars applies the authoritative invariants shared by Draft
// input and persisted Published Session state.
func ValidateSessionScalars(item SessionDraftInput) error {
	if !validText(item.Title, 200) {
		return invalid("sessions.title", "must be 1 to 200 characters without control characters")
	}
	if !validSessionType(item.Type) {
		return invalid("sessions.type", "must be a supported version-one Session type")
	}
	if item.AudienceVisibility != AudiencePublic && item.AudienceVisibility != AudienceCrewOnly {
		return invalid("sessions.audience_visibility", "must be Public or CrewOnly")
	}
	if item.PlannedStart.IsZero() || !item.PlannedEnd.After(item.PlannedStart) {
		return invalid("sessions.planned_end", "must be after planned_start")
	}
	if item.TimingPolicy != TimingFixedEnd && item.TimingPolicy != TimingFixedDuration && item.TimingPolicy != TimingManualEnd {
		return invalid("sessions.timing_policy", "must be FixedEnd, FixedDuration, or ManualEnd")
	}
	if item.MinimumDuration < 0 || item.MinimumDuration > item.PlannedEnd.Sub(item.PlannedStart) {
		return invalid("sessions.minimum_duration", "must fit within Planned Time")
	}
	if !validBoundary(item.StartBoundary) || !validBoundary(item.EndBoundary) {
		return invalid("sessions.boundary", "must be Hard or Soft")
	}
	return nil
}

func validateNamedRefs[T any](
	field string,
	items []T,
	values func(T) (int, string, string, []string),
) (map[string]struct{}, error) {
	refs := make(map[string]struct{}, len(items))
	for _, item := range items {
		id, ref, name, updateFields := values(item)
		if id == 0 {
			if err := validateRef(field+".ref", ref, refs); err != nil {
				return nil, err
			}
		} else if ref != "" {
			return nil, invalid(field+".ref", "must be empty for an update")
		}
		if (id == 0 || contains(updateFields, "name")) && !validText(strings.TrimSpace(name), 200) {
			return nil, invalid(field+".name", "must be 1 to 200 characters without control characters")
		}
	}
	return refs, nil
}

func validateUpdateFields(field string, id int, fields []string, allowed ...string) error {
	if id <= 0 {
		if len(fields) != 0 {
			return invalid(field+".update_mask", "must be empty for a creation")
		}
		return nil
	}
	if len(fields) == 0 {
		return invalid(field+".update_mask", "must select at least one field for an update")
	}
	seen := make(map[string]struct{}, len(fields))
	for _, candidate := range fields {
		if !contains(allowed, candidate) {
			return invalid(field+".update_mask", "contains an unsupported field")
		}
		if _, exists := seen[candidate]; exists {
			return invalid(field+".update_mask", "must not contain duplicate fields")
		}
		seen[candidate] = struct{}{}
	}
	return nil
}

func contains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

func validateRef(field, ref string, seen map[string]struct{}) error {
	if err := command.ValidateID(ref); err != nil {
		return invalid(field, "must be a unique visible batch-local reference")
	}
	if _, exists := seen[ref]; exists {
		return invalid(field, "must be unique within its entity type")
	}
	seen[ref] = struct{}{}
	return nil
}

func validateTarget(field string, target TargetRef, local map[string]struct{}) error {
	if (target.ID > 0) == (target.Ref != "") {
		return invalid(field, "must contain exactly one stable ID or batch-local reference")
	}
	if target.Ref != "" {
		if _, exists := local[target.Ref]; !exists {
			return invalid(field, "references an unknown batch-local entity")
		}
	}
	return nil
}

func validText(value string, maximum int) bool {
	if value == "" || utf8.RuneCountInString(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validSessionType(value SessionType) bool {
	switch value {
	case SessionPresentation, SessionCompetition, SessionBreak, SessionActivity,
		SessionCeremony, SessionPerformance, SessionHold:
		return true
	default:
		return false
	}
}

func validBoundary(value Boundary) bool {
	return value == BoundaryHard || value == BoundarySoft
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func editDraftParams(actorID int, input EditDraftInput, now time.Time) store.EditDraftParams {
	params := store.EditDraftParams{
		EventID: input.EventID, ActorAccountID: actorID,
		ExpectedDraftRevision: input.ExpectedDraftRevision, Now: now,
	}
	for _, item := range input.Locations {
		params.Locations = append(params.Locations, store.LocationDraftCreate{
			ID: item.ID, Ref: item.Ref, Name: item.Name, UpdateFields: item.UpdateFields,
		})
	}
	for _, item := range input.Lanes {
		params.Lanes = append(params.Lanes, store.LaneDraftCreate{
			ID: item.ID, Ref: item.Ref, Name: item.Name, Location: draftTarget(item.Location), UpdateFields: item.UpdateFields,
		})
	}
	for _, item := range input.Tracks {
		params.Tracks = append(params.Tracks, store.TrackDraftCreate{
			ID: item.ID, Ref: item.Ref, Name: item.Name, UpdateFields: item.UpdateFields,
		})
	}
	for _, item := range input.Sessions {
		created := store.SessionDraftCreate{
			ID: item.ID, Ref: item.Ref, Title: item.Title, Speaker: item.Speaker, Type: string(item.Type),
			AudienceVisibility: string(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails, CrewNotes: item.CrewNotes,
			PlannedStart: item.PlannedStart, PlannedEnd: item.PlannedEnd,
			TimingPolicy:           string(item.TimingPolicy),
			MinimumDurationSeconds: int(item.MinimumDuration / time.Second),
			StartBoundary:          string(item.StartBoundary), EndBoundary: string(item.EndBoundary),
			UpdateFields: item.UpdateFields,
		}
		for _, target := range item.Lanes {
			created.Lanes = append(created.Lanes, draftTarget(target))
		}
		for _, target := range item.Locations {
			created.Locations = append(created.Locations, draftTarget(target))
		}
		for _, target := range item.Tracks {
			created.Tracks = append(created.Tracks, draftTarget(target))
		}
		for _, target := range item.AddLanes {
			created.AddLanes = append(created.AddLanes, draftTarget(target))
		}
		for _, target := range item.RemoveLanes {
			created.RemoveLanes = append(created.RemoveLanes, draftTarget(target))
		}
		for _, target := range item.AddLocations {
			created.AddLocations = append(created.AddLocations, draftTarget(target))
		}
		for _, target := range item.RemoveLocations {
			created.RemoveLocations = append(created.RemoveLocations, draftTarget(target))
		}
		for _, target := range item.AddTracks {
			created.AddTracks = append(created.AddTracks, draftTarget(target))
		}
		for _, target := range item.RemoveTracks {
			created.RemoveTracks = append(created.RemoveTracks, draftTarget(target))
		}
		params.Sessions = append(params.Sessions, created)
	}
	return params
}

func draftTarget(target TargetRef) store.DraftTarget {
	return store.DraftTarget{ID: target.ID, Ref: target.Ref}
}

func editDraftResult(stored store.EditDraftResult) EditDraftResult {
	result := EditDraftResult{DraftRevision: stored.DraftRevision, Changes: make([]DraftChange, 0, len(stored.Changes))}
	for _, change := range stored.Changes {
		result.Changes = append(result.Changes, DraftChange{
			ID: change.ID, Kind: change.Kind, TargetType: change.TargetType, TargetID: change.TargetID,
			FactKey: change.FactKey, Status: change.Status,
			CurrentValueJSON: change.CurrentValueJSON,
		})
	}
	return result
}

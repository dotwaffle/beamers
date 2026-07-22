// Package rundown stages and publishes Event structure through deep command and query APIs.
package rundown

import (
	"context"
	"encoding/json"
	"errors"
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
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

// LaneDraftInput creates one Lane in shared Draft state.
type LaneDraftInput struct {
	Ref      string    `json:"ref"`
	Name     string    `json:"name"`
	Location TargetRef `json:"location"`
}

// TrackDraftInput creates one Track in shared Draft state.
type TrackDraftInput struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
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
	Ref                string             `json:"ref"`
	Title              string             `json:"title"`
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
	ID         int    `json:"id"`
	Kind       string `json:"kind"`
	TargetType string `json:"target_type"`
	TargetID   int    `json:"target_id"`
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
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
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
	transaction, err := commands.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return EditDraftResult{}, err
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	original, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return EditDraftResult{}, commitErr
		}
		return EditDraftResult{}, ErrCommandConflict
	}
	if err != nil {
		return EditDraftResult{}, err
	}
	if retry {
		return decodeEditDraftOutcome(original)
	}
	if !actor.CanProduceEvent(input.EventID) {
		return EditDraftResult{}, commands.rejectEditDraft(ctx, transaction, actor, identity, rejection{
			Code: "event_access_denied", Message: ErrEventAccessDenied.Error(),
		})
	}
	normalized, validationErr := validateEditDraft(input)
	if validationErr != nil {
		var invalid *ValidationError
		_ = errors.As(validationErr, &invalid)
		return EditDraftResult{}, commands.rejectEditDraft(ctx, transaction, actor, identity, rejection{
			Code: "validation", Field: invalid.Field, Message: invalid.Message,
		})
	}
	stored, err := transaction.EditDraft(actor.Context(ctx), editDraftParams(actor.ID, normalized, identity.Now))
	if errors.Is(err, store.ErrDraftReference) {
		return EditDraftResult{}, commands.rejectEditDraft(ctx, transaction, actor, identity, rejection{
			Code: "validation", Field: "references", Message: "must identify Draft structure in this Event",
		})
	}
	if errors.Is(err, ErrDraftRevisionConflict) {
		return EditDraftResult{}, commands.rejectEditDraft(ctx, transaction, actor, identity, rejection{
			Code: "draft_revision_conflict", Message: ErrDraftRevisionConflict.Error(),
		})
	}
	if err != nil {
		return EditDraftResult{}, err
	}
	result := editDraftResult(stored)
	encoded, err := json.Marshal(editDraftOutcome{Result: &result})
	if err != nil {
		return EditDraftResult{}, errors.New("encode Edit Draft outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return EditDraftResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return EditDraftResult{}, err
	}
	return result, nil
}

func (commands *Commands) rejectEditDraft(
	ctx context.Context,
	transaction *store.CommandTx,
	actor auth.Account,
	identity store.CommandIdentity,
	rejected rejection,
) error {
	encoded, err := json.Marshal(editDraftOutcome{Rejection: &rejected})
	if err != nil {
		return errors.New("encode rejected Edit Draft outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), true); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	return rejectionError(rejected)
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
	locationRefs, err := validateNamedRefs("locations", input.Locations, func(item LocationDraftInput) (string, string) {
		return item.Ref, item.Name
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	laneRefs, err := validateNamedRefs("lanes", input.Lanes, func(item LaneDraftInput) (string, string) {
		return item.Ref, item.Name
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	trackRefs, err := validateNamedRefs("tracks", input.Tracks, func(item TrackDraftInput) (string, string) {
		return item.Ref, item.Name
	})
	if err != nil {
		return EditDraftInput{}, err
	}
	for index := range input.Locations {
		input.Locations[index].Name = strings.TrimSpace(input.Locations[index].Name)
	}
	for index := range input.Lanes {
		input.Lanes[index].Name = strings.TrimSpace(input.Lanes[index].Name)
		if err := validateTarget("lanes.location", input.Lanes[index].Location, locationRefs); err != nil {
			return EditDraftInput{}, err
		}
	}
	for index := range input.Tracks {
		input.Tracks[index].Name = strings.TrimSpace(input.Tracks[index].Name)
	}
	sessionRefs := make(map[string]struct{}, len(input.Sessions))
	for index := range input.Sessions {
		item := &input.Sessions[index]
		if err := validateRef("sessions.ref", item.Ref, sessionRefs); err != nil {
			return EditDraftInput{}, err
		}
		item.Title = strings.TrimSpace(item.Title)
		if err := ValidateSessionScalars(*item); err != nil {
			return EditDraftInput{}, err
		}
		if len(item.Lanes) == 0 {
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
	}
	return input, nil
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
	values func(T) (string, string),
) (map[string]struct{}, error) {
	refs := make(map[string]struct{}, len(items))
	for _, item := range items {
		ref, name := values(item)
		if err := validateRef(field+".ref", ref, refs); err != nil {
			return nil, err
		}
		if !validText(strings.TrimSpace(name), 200) {
			return nil, invalid(field+".name", "must be 1 to 200 characters without control characters")
		}
	}
	return refs, nil
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
		params.Locations = append(params.Locations, store.LocationDraftCreate{Ref: item.Ref, Name: item.Name})
	}
	for _, item := range input.Lanes {
		params.Lanes = append(params.Lanes, store.LaneDraftCreate{
			Ref: item.Ref, Name: item.Name, Location: draftTarget(item.Location),
		})
	}
	for _, item := range input.Tracks {
		params.Tracks = append(params.Tracks, store.TrackDraftCreate{Ref: item.Ref, Name: item.Name})
	}
	for _, item := range input.Sessions {
		created := store.SessionDraftCreate{
			Ref: item.Ref, Title: item.Title, Type: string(item.Type),
			AudienceVisibility: string(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails, CrewNotes: item.CrewNotes,
			PlannedStart: item.PlannedStart, PlannedEnd: item.PlannedEnd,
			TimingPolicy:           string(item.TimingPolicy),
			MinimumDurationSeconds: int(item.MinimumDuration / time.Second),
			StartBoundary:          string(item.StartBoundary), EndBoundary: string(item.EndBoundary),
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
		})
	}
	return result
}

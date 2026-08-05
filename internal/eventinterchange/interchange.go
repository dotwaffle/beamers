// Package eventinterchange exports portable Event authoring state and imports it into reviewable Draft state.
package eventinterchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const (
	format          = "beamers.event"
	version         = 2
	minimumVersion  = 1
	maxDocumentSize = 64 << 20
)

var (
	// ErrAdministratorRequired means an import lacked installation-wide authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrEventAccessDenied means an export lacked Producer authority.
	ErrEventAccessDenied = errors.New("event access denied")
	// ErrUnsupportedVersion means the document requires an unknown interchange version.
	ErrUnsupportedVersion = errors.New("unsupported Event interchange version")
	// ErrInvalidDocument means the document is not a complete valid portable Event.
	ErrInvalidDocument = errors.New("invalid Event interchange document")
	// ErrReviewMismatch means Import did not receive the exact reviewed document.
	ErrReviewMismatch = errors.New("event import review does not match document")
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
)

// ValidationError describes one invalid document field.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (err *ValidationError) Error() string {
	return err.Field + ": " + err.Message
}

// Unwrap identifies all field failures as invalid documents.
func (err *ValidationError) Unwrap() error {
	return ErrInvalidDocument
}

// ExportInput identifies one commanded portable snapshot.
type ExportInput struct {
	EventID   int
	CommandID string
}

// Artifact is one deterministic versioned Event document.
type Artifact struct {
	Document []byte `json:"document"`
	SHA256   string `json:"sha256"`
}

// ImportReview summarizes one fully validated side-effect-free plan.
type ImportReview struct {
	Fingerprint   string `json:"fingerprint"`
	EventName     string `json:"event_name"`
	LocationCount int    `json:"location_count"`
	LaneCount     int    `json:"lane_count"`
	TrackCount    int    `json:"track_count"`
	SessionCount  int    `json:"session_count"`
}

// ImportInput confirms one exact reviewed document.
type ImportInput struct {
	Document          []byte
	ReviewFingerprint string
	CommandID         string
}

// ImportResult identifies the new Event and its reviewable Draft changes.
type ImportResult struct {
	EventID       int                   `json:"event_id"`
	DraftRevision int                   `json:"draft_revision"`
	Changes       []rundown.DraftChange `json:"changes"`
}

// Service owns Event interchange authorization, validation, and command evidence.
type Service struct {
	storage *store.SQLite
	now     func() time.Time
}

// New creates an Event Interchange service with explicit local dependencies.
func New(storage *store.SQLite, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("event interchange storage is required")
	}
	if now == nil {
		return nil, errors.New("event interchange clock is required")
	}
	return &Service{storage: storage, now: now}, nil
}

// Export returns one commanded, audited snapshot of current Published authoring state.
func (service *Service) Export(
	ctx context.Context,
	actor auth.Account,
	input ExportInput,
) (Artifact, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Artifact{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(strconv.Itoa(input.EventID)),
		Action:         "ExportEventInterchange",
		TargetType:     "Event",
		TargetID:       strconv.Itoa(input.EventID),
		Now:            service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Artifact]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayExport,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: interchangeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Artifact], error) {
			state, err := transaction.LoadEventInterchange(actor.Context(ctx), input.EventID)
			if err != nil {
				return command.Execution[Artifact]{}, err
			}
			document, err := encodeState(state)
			if err != nil {
				return command.Execution[Artifact]{}, err
			}
			if len(document) > maxDocumentSize {
				return rejectArtifact(invalid("document", "must not exceed 64 MiB"))
			}
			artifact := Artifact{Document: document, SHA256: fingerprint(document)}
			encoded, err := json.Marshal(exportOutcome{Result: &artifact})
			if err != nil {
				return command.Execution[Artifact]{}, errors.New("encode Event interchange Export receipt")
			}
			return command.Success(artifact, string(encoded)), nil
		},
	})
}

// ReviewImport validates the whole document without mutating installation state.
func (service *Service) ReviewImport(
	ctx context.Context,
	actor auth.Account,
	document []byte,
) (ImportReview, error) {
	if !actor.Administrator {
		return ImportReview{}, ErrAdministratorRequired
	}
	plan, err := decodePlan(ctx, document)
	if err != nil {
		return ImportReview{}, err
	}
	return ImportReview{
		Fingerprint:   fingerprint(document),
		EventName:     plan.event.Name,
		LocationCount: len(plan.draft.Locations),
		LaneCount:     len(plan.draft.Lanes),
		TrackCount:    len(plan.draft.Tracks),
		SessionCount:  len(plan.draft.Sessions),
	}, nil
}

// Import creates one Event and complete reviewable Draft in one commanded transaction.
func (service *Service) Import(
	ctx context.Context,
	actor auth.Account,
	input ImportInput,
) (ImportResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return ImportResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	var plan importPlan
	var planErr error
	if actor.Administrator {
		if len(input.Document) > maxDocumentSize {
			planErr = invalid("document", "must not exceed 64 MiB")
		} else {
			plan, planErr = decodePlan(ctx, input.Document)
		}
		if errors.Is(planErr, context.Canceled) || errors.Is(planErr, context.DeadlineExceeded) {
			return ImportResult{}, planErr
		}
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    importPayloadHash(input.Document, input.ReviewFingerprint),
		Action:         "ImportEventInterchange",
		TargetType:     "Event",
		TargetID:       "unidentified",
		Now:            service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[ImportResult]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayImport,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: interchangeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[ImportResult], error) {
			if !actor.Administrator {
				return rejectImport("administrator_required", ErrAdministratorRequired)
			}
			if planErr != nil {
				return rejectImport(importErrorCode(planErr), planErr)
			}
			if input.ReviewFingerprint != fingerprint(input.Document) {
				return rejectImport("review_mismatch", ErrReviewMismatch)
			}
			created, err := transaction.CreateEvent(actor.Context(ctx), store.CreateEventParams{
				ActorAccountID:                 actor.ID,
				Name:                           plan.event.Name,
				PlannedStartDate:               plan.event.PlannedStartDate,
				PlannedEndDate:                 plan.event.PlannedEndDate,
				Timezone:                       plan.event.Timezone,
				EventLocale:                    plan.event.EventLocale,
				ContentLanguage:                plan.event.ContentLanguage,
				EventDayBoundary:               plan.event.EventDayBoundary,
				EntryDefaultDisposition:        plan.event.EntryDefaultDisposition,
				SubmissionEligibility:          plan.event.SubmissionEligibility,
				TargetAdjustmentPresetsSeconds: plan.event.TargetAdjustmentPresetsSeconds,
				Now:                            identity.Now,
				CommandID:                      input.CommandID,
				PayloadHash:                    identity.PayloadHash,
			})
			if err != nil {
				return command.Execution[ImportResult]{}, err
			}
			if _, err = transaction.GrantEventAccess(actor.Context(ctx), store.GrantEventAccessParams{
				ActorAccountID: actor.ID,
				EventID:        created.ID,
				AccountID:      actor.ID,
				Role:           "Producer",
				Now:            identity.Now,
				CommandID:      input.CommandID,
				PayloadHash:    identity.PayloadHash,
			}); err != nil {
				return command.Execution[ImportResult]{}, err
			}
			result := ImportResult{EventID: created.ID, Changes: []rundown.DraftChange{}}
			if hasDraftChanges(plan.draft) {
				plan.draft.EventID = created.ID
				importActor := actor
				importActor.EventRoles = maps.Clone(actor.EventRoles)
				if importActor.EventRoles == nil {
					importActor.EventRoles = make(map[int]viewer.Role)
				}
				importActor.EventRoles[created.ID] = viewer.Producer
				stored, editErr := transaction.EditDraft(
					importActor.Context(ctx),
					rundown.EditDraftParams(actor.ID, plan.draft, identity.Now),
				)
				if editErr != nil {
					return command.Execution[ImportResult]{}, editErr
				}
				result.DraftRevision = stored.DraftRevision
				result.Changes = draftChanges(stored.Changes)
			}
			encoded, err := json.Marshal(importOutcome{Result: &result})
			if err != nil {
				return command.Execution[ImportResult]{}, errors.New("encode Event interchange Import receipt")
			}
			return command.Success(result, string(encoded)).WithTargetID(strconv.Itoa(created.ID)), nil
		},
	})
}

type document struct {
	Format    string            `json:"format"`
	Version   int               `json:"version"`
	Event     eventDocument     `json:"event"`
	Locations []namedDocument   `json:"locations"`
	Lanes     []laneDocument    `json:"lanes"`
	Tracks    []namedDocument   `json:"tracks"`
	Sessions  []sessionDocument `json:"sessions"`
}

type eventDocument struct {
	Name                           string `json:"name"`
	PlannedStartDate               string `json:"planned_start_date"`
	PlannedEndDate                 string `json:"planned_end_date"`
	Timezone                       string `json:"timezone"`
	EventLocale                    string `json:"event_locale"`
	ContentLanguage                string `json:"content_language,omitempty"`
	EventDayBoundary               string `json:"event_day_boundary"`
	EntryDefaultDisposition        string `json:"entry_default_disposition"`
	SubmissionEligibility          string `json:"submission_eligibility,omitempty"`
	TargetAdjustmentPresetsSeconds []int  `json:"target_adjustment_presets_seconds"`
}

type namedDocument struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type laneDocument struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	LocationRef string `json:"location_ref"`
}

type sessionDocument struct {
	Ref                     string     `json:"ref"`
	Title                   string     `json:"title"`
	Speaker                 string     `json:"speaker,omitempty"`
	Type                    string     `json:"type"`
	AudienceVisibility      string     `json:"audience_visibility"`
	PublicDetails           string     `json:"public_details,omitempty"`
	PlannedStart            time.Time  `json:"planned_start"`
	PlannedEnd              time.Time  `json:"planned_end"`
	TimingPolicy            string     `json:"timing_policy"`
	MinimumDurationSeconds  int        `json:"minimum_duration_seconds"`
	StartBoundary           string     `json:"start_boundary"`
	EndBoundary             string     `json:"end_boundary"`
	UploadDeadline          *time.Time `json:"upload_deadline,omitempty"`
	SubmissionDeadline      *time.Time `json:"submission_deadline,omitempty"`
	EntryDefaultDisposition string     `json:"entry_default_disposition,omitempty"`
	LaneRefs                []string   `json:"lane_refs"`
	LocationRefs            []string   `json:"location_refs"`
	TrackRefs               []string   `json:"track_refs"`
}

type importPlan struct {
	event events.CreateInput
	draft rundown.EditDraftInput
}

type importOutcome struct {
	Result    *ImportResult `json:"result,omitempty"`
	Rejection *rejection    `json:"rejection,omitempty"`
}

type exportOutcome struct {
	Result    *Artifact  `json:"result,omitempty"`
	Rejection *rejection `json:"rejection,omitempty"`
}

type rejection struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func encodeState(state store.EventInterchangeState) ([]byte, error) {
	var presets []int
	if err := json.Unmarshal([]byte(state.Event.TargetAdjustmentPresets), &presets); err != nil {
		return nil, errors.New("decode Event Adjust Target presets")
	}
	location, err := time.LoadLocation(state.Event.Timezone)
	if err != nil {
		return nil, errors.New("load Event interchange timezone")
	}
	result := document{
		Format:  format,
		Version: version,
		Event: eventDocument{
			Name:                           state.Event.Name,
			PlannedStartDate:               state.Event.PlannedStartDate,
			PlannedEndDate:                 state.Event.PlannedEndDate,
			Timezone:                       state.Event.Timezone,
			EventLocale:                    state.Event.EventLocale,
			ContentLanguage:                state.Event.ContentLanguage,
			EventDayBoundary:               state.Event.EventDayBoundary,
			EntryDefaultDisposition:        state.Event.EntryDefaultDisposition,
			SubmissionEligibility:          state.Event.SubmissionEligibility,
			TargetAdjustmentPresetsSeconds: presets,
		},
		Locations: []namedDocument{},
		Lanes:     []laneDocument{},
		Tracks:    []namedDocument{},
		Sessions:  []sessionDocument{},
	}
	locations := slices.Clone(state.Rundown.Locations)
	lanes := slices.Clone(state.Rundown.Lanes)
	tracks := slices.Clone(state.Rundown.Tracks)
	sessions := slices.Clone(state.Rundown.Sessions)
	slices.SortFunc(locations, func(left, right store.PublishedLocation) int {
		return left.ID - right.ID
	})
	slices.SortFunc(lanes, func(left, right store.PublishedLane) int {
		return left.ID - right.ID
	})
	slices.SortFunc(tracks, func(left, right store.PublishedTrack) int {
		return left.ID - right.ID
	})
	slices.SortFunc(sessions, func(left, right store.PublishedSession) int {
		return left.ID - right.ID
	})
	locationRefs := make(map[int]string, len(locations))
	laneRefs := make(map[int]string, len(lanes))
	trackRefs := make(map[int]string, len(tracks))
	for index, item := range locations {
		ref := ordinalRef("location", index)
		locationRefs[item.ID] = ref
		result.Locations = append(result.Locations, namedDocument{Ref: ref, Name: item.Name})
	}
	for index, item := range lanes {
		ref := ordinalRef("lane", index)
		laneRefs[item.ID] = ref
		result.Lanes = append(result.Lanes, laneDocument{
			Ref: ref, Name: item.Name, LocationRef: locationRefs[item.LocationID],
		})
	}
	for index, item := range tracks {
		ref := ordinalRef("track", index)
		trackRefs[item.ID] = ref
		result.Tracks = append(result.Tracks, namedDocument{Ref: ref, Name: item.Name})
	}
	for index, item := range sessions {
		session := sessionDocument{
			Ref:                     ordinalRef("session", index),
			Title:                   item.Title,
			Speaker:                 item.Speaker,
			Type:                    item.Type,
			AudienceVisibility:      item.AudienceVisibility,
			PublicDetails:           item.PublicDetails,
			PlannedStart:            item.PlannedStart.In(location),
			PlannedEnd:              item.PlannedEnd.In(location),
			TimingPolicy:            item.TimingPolicy,
			MinimumDurationSeconds:  item.MinimumDurationSeconds,
			StartBoundary:           item.StartBoundary,
			EndBoundary:             item.EndBoundary,
			EntryDefaultDisposition: item.EntryDefaultDisposition,
			LaneRefs:                refs(item.LaneIDs, laneRefs),
			LocationRefs:            refs(item.LocationIDs, locationRefs),
			TrackRefs:               refs(item.TrackIDs, trackRefs),
		}
		session.UploadDeadline = optionalTimeIn(item.UploadDeadline, location)
		session.SubmissionDeadline = optionalTimeIn(item.SubmissionDeadline, location)
		result.Sessions = append(result.Sessions, session)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, errors.New("encode Event interchange document")
	}
	return encoded, nil
}

func decodePlan(ctx context.Context, encoded []byte) (importPlan, error) {
	if len(encoded) == 0 {
		return importPlan{}, invalid("document", "must not be empty")
	}
	if len(encoded) > maxDocumentSize {
		return importPlan{}, invalid("document", "must not exceed 64 MiB")
	}
	if !utf8.Valid(encoded) {
		return importPlan{}, invalid("document", "must be valid UTF-8")
	}
	sourceReader := bytes.NewReader(encoded)
	decoder := json.NewDecoder(readerFunc(func(buffer []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return sourceReader.Read(buffer)
	}))
	decoder.DisallowUnknownFields()
	var source document
	if err := decoder.Decode(&source); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return importPlan{}, contextErr
		}
		return importPlan{}, invalid("document", "must be strict JSON: "+err.Error())
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if contextErr := ctx.Err(); contextErr != nil {
			return importPlan{}, contextErr
		}
		return importPlan{}, invalid("document", "must contain one JSON value")
	}
	if source.Format != format {
		return importPlan{}, invalid("format", "must be "+format)
	}
	if source.Version < minimumVersion || source.Version > version {
		return importPlan{}, ErrUnsupportedVersion
	}
	if source.Version == 1 && source.Event.SubmissionEligibility != "" {
		return importPlan{}, invalid(
			"event.submission_eligibility",
			"is not part of Event interchange version 1",
		)
	}
	if source.Version == 2 && source.Event.SubmissionEligibility == "" {
		return importPlan{}, invalid("event.submission_eligibility", "must be present")
	}
	proposedEvent := events.CreateInput{
		Name:                           source.Event.Name,
		PlannedStartDate:               source.Event.PlannedStartDate,
		PlannedEndDate:                 source.Event.PlannedEndDate,
		Timezone:                       source.Event.Timezone,
		EventLocale:                    source.Event.EventLocale,
		ContentLanguage:                source.Event.ContentLanguage,
		EventDayBoundary:               source.Event.EventDayBoundary,
		EntryDefaultDisposition:        source.Event.EntryDefaultDisposition,
		SubmissionEligibility:          source.Event.SubmissionEligibility,
		TargetAdjustmentPresetsSeconds: source.Event.TargetAdjustmentPresetsSeconds,
	}
	if source.Version == 1 {
		proposedEvent.SubmissionEligibility = "AllAccounts"
	}
	eventInput, err := events.ValidateCreateInput(proposedEvent)
	if err != nil {
		var validation *events.ValidationError
		if errors.As(err, &validation) {
			return importPlan{}, invalid("event."+validation.Field, validation.Message)
		}
		return importPlan{}, err
	}
	if !sameEventInput(proposedEvent, eventInput) {
		return importPlan{}, invalid("event", "must use complete canonical Event values")
	}
	draft, err := documentDraft(source)
	if err != nil {
		return importPlan{}, err
	}
	if hasDraftChanges(draft) {
		draft.EventID = 1
		draft, err = rundown.ValidateEditDraft(draft)
		if err != nil {
			var validation *rundown.ValidationError
			if errors.As(err, &validation) {
				return importPlan{}, invalid(validation.Field, validation.Message)
			}
			return importPlan{}, err
		}
		if err := validatePreservedText(source, draft); err != nil {
			return importPlan{}, err
		}
		if err := validateEventTimes(eventInput.Timezone, draft.Sessions); err != nil {
			return importPlan{}, err
		}
		normalizeDraftTimesUTC(draft.Sessions)
	}
	return importPlan{event: eventInput, draft: draft}, nil
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(buffer []byte) (int, error) {
	return read(buffer)
}

func sameEventInput(left, right events.CreateInput) bool {
	return left.Name == right.Name &&
		left.PlannedStartDate == right.PlannedStartDate &&
		left.PlannedEndDate == right.PlannedEndDate &&
		left.Timezone == right.Timezone &&
		left.EventLocale == right.EventLocale &&
		left.ContentLanguage == right.ContentLanguage &&
		left.EventDayBoundary == right.EventDayBoundary &&
		left.EntryDefaultDisposition == right.EntryDefaultDisposition &&
		left.SubmissionEligibility == right.SubmissionEligibility &&
		slices.Equal(left.TargetAdjustmentPresetsSeconds, right.TargetAdjustmentPresetsSeconds)
}

func validatePreservedText(source document, normalized rundown.EditDraftInput) error {
	for index, item := range source.Locations {
		if normalized.Locations[index].Name != item.Name {
			return invalid(fmt.Sprintf("locations[%d].name", index), "must not need whitespace normalization")
		}
	}
	for index, item := range source.Lanes {
		if normalized.Lanes[index].Name != item.Name {
			return invalid(fmt.Sprintf("lanes[%d].name", index), "must not need whitespace normalization")
		}
	}
	for index, item := range source.Tracks {
		if normalized.Tracks[index].Name != item.Name {
			return invalid(fmt.Sprintf("tracks[%d].name", index), "must not need whitespace normalization")
		}
	}
	for index, item := range source.Sessions {
		if normalized.Sessions[index].Title != item.Title {
			return invalid(fmt.Sprintf("sessions[%d].title", index), "must not need whitespace normalization")
		}
		if normalized.Sessions[index].Speaker != item.Speaker {
			return invalid(fmt.Sprintf("sessions[%d].speaker", index), "must not need whitespace normalization")
		}
	}
	return nil
}

func documentDraft(source document) (rundown.EditDraftInput, error) {
	draft := rundown.EditDraftInput{
		Locations: make([]rundown.LocationDraftInput, 0, len(source.Locations)),
		Lanes:     make([]rundown.LaneDraftInput, 0, len(source.Lanes)),
		Tracks:    make([]rundown.TrackDraftInput, 0, len(source.Tracks)),
		Sessions:  make([]rundown.SessionDraftInput, 0, len(source.Sessions)),
	}
	seen := make(map[string]struct{}, len(source.Locations)+len(source.Lanes)+len(source.Tracks)+len(source.Sessions))
	for _, item := range source.Locations {
		if err := uniqueRef(seen, item.Ref); err != nil {
			return rundown.EditDraftInput{}, err
		}
		draft.Locations = append(draft.Locations, rundown.LocationDraftInput{Ref: item.Ref, Name: item.Name})
	}
	for _, item := range source.Lanes {
		if err := uniqueRef(seen, item.Ref); err != nil {
			return rundown.EditDraftInput{}, err
		}
		draft.Lanes = append(draft.Lanes, rundown.LaneDraftInput{
			Ref: item.Ref, Name: item.Name, Location: rundown.TargetRef{Ref: item.LocationRef},
		})
	}
	for _, item := range source.Tracks {
		if err := uniqueRef(seen, item.Ref); err != nil {
			return rundown.EditDraftInput{}, err
		}
		draft.Tracks = append(draft.Tracks, rundown.TrackDraftInput{Ref: item.Ref, Name: item.Name})
	}
	for _, item := range source.Sessions {
		if err := uniqueRef(seen, item.Ref); err != nil {
			return rundown.EditDraftInput{}, err
		}
		sessionIndex := len(draft.Sessions)
		relationships := []struct {
			field string
			refs  []string
		}{
			{"lane_refs", item.LaneRefs},
			{"location_refs", item.LocationRefs},
			{"track_refs", item.TrackRefs},
		}
		for _, relationship := range relationships {
			if err := validateUniqueRelationshipRefs(
				sessionIndex,
				relationship.field,
				relationship.refs,
			); err != nil {
				return rundown.EditDraftInput{}, err
			}
		}
		if item.MinimumDurationSeconds < 0 ||
			int64(item.MinimumDurationSeconds) > int64(^uint64(0)>>1)/int64(time.Second) {
			return rundown.EditDraftInput{}, invalid(
				"sessions.minimum_duration_seconds",
				"must be non-negative and fit a Go duration",
			)
		}
		draft.Sessions = append(draft.Sessions, rundown.SessionDraftInput{
			Ref:                item.Ref,
			Title:              item.Title,
			Speaker:            item.Speaker,
			Type:               rundown.SessionType(item.Type),
			AudienceVisibility: rundown.AudienceVisibility(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails,
			PlannedStart:       item.PlannedStart,
			PlannedEnd:         item.PlannedEnd,
			TimingPolicy:       rundown.TimingPolicy(item.TimingPolicy),
			MinimumDuration:    time.Duration(item.MinimumDurationSeconds) * time.Second,
			StartBoundary:      rundown.Boundary(item.StartBoundary),
			EndBoundary:        rundown.Boundary(item.EndBoundary),
			UploadDeadline:     dereferenceTime(item.UploadDeadline),
			SubmissionDeadline: dereferenceTime(item.SubmissionDeadline),
			EntryDefault:       rundown.EntryDisposition(item.EntryDefaultDisposition),
			Lanes:              targetRefs(item.LaneRefs),
			Locations:          targetRefs(item.LocationRefs),
			Tracks:             targetRefs(item.TrackRefs),
		})
	}
	return draft, nil
}

func validateUniqueRelationshipRefs(sessionIndex int, field string, refs []string) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref]; exists {
			return invalid(
				fmt.Sprintf("sessions[%d].%s", sessionIndex, field),
				"must not contain duplicate refs",
			)
		}
		seen[ref] = struct{}{}
	}
	return nil
}

func validateEventTimes(timezone string, sessions []rundown.SessionDraftInput) error {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return invalid("event.timezone", "must be a recognized IANA timezone")
	}
	for index, session := range sessions {
		instants := []struct {
			field string
			value time.Time
		}{
			{"planned_start", session.PlannedStart},
			{"planned_end", session.PlannedEnd},
			{"upload_deadline", session.UploadDeadline},
			{"submission_deadline", session.SubmissionDeadline},
		}
		for _, instant := range instants {
			if instant.value.IsZero() {
				continue
			}
			_, documentOffset := instant.value.Zone()
			_, eventOffset := instant.value.In(location).Zone()
			if documentOffset != eventOffset {
				return invalid(
					fmt.Sprintf("sessions[%d].%s", index, instant.field),
					"offset must match the Event timezone at that instant",
				)
			}
		}
	}
	return nil
}

func normalizeDraftTimesUTC(sessions []rundown.SessionDraftInput) {
	for index := range sessions {
		session := &sessions[index]
		session.PlannedStart = session.PlannedStart.UTC()
		session.PlannedEnd = session.PlannedEnd.UTC()
		if !session.UploadDeadline.IsZero() {
			session.UploadDeadline = session.UploadDeadline.UTC()
		}
		if !session.SubmissionDeadline.IsZero() {
			session.SubmissionDeadline = session.SubmissionDeadline.UTC()
		}
	}
}

func rejectArtifact(err error) (command.Execution[Artifact], error) {
	rejected := rejection{Code: importErrorCode(err), Message: err.Error()}
	var validation *ValidationError
	if errors.As(err, &validation) {
		rejected.Field = validation.Field
		rejected.Message = validation.Message
	}
	encoded, encodeErr := json.Marshal(exportOutcome{
		Rejection: &rejected,
	})
	if encodeErr != nil {
		return command.Execution[Artifact]{}, errors.New("encode Event interchange Export rejection")
	}
	return command.RejectEncoded(Artifact{}, string(encoded), err), nil
}

func replayExport(encoded string) (Artifact, error) {
	var outcome exportOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return Artifact{}, errors.New("decode Event interchange Export receipt")
	}
	if outcome.Result != nil {
		return *outcome.Result, nil
	}
	if outcome.Rejection != nil && outcome.Rejection.Code == "event_access_denied" {
		return Artifact{}, ErrEventAccessDenied
	}
	if outcome.Rejection != nil && outcome.Rejection.Code == "invalid_document" {
		return Artifact{}, &ValidationError{
			Field: outcome.Rejection.Field, Message: outcome.Rejection.Message,
		}
	}
	return Artifact{}, errors.New("event interchange Export receipt has no outcome")
}

func rejectImport(code string, err error) (command.Execution[ImportResult], error) {
	rejected := rejection{Code: code, Message: err.Error()}
	var validation *ValidationError
	if errors.As(err, &validation) {
		rejected.Field = validation.Field
		rejected.Message = validation.Message
	}
	encoded, encodeErr := json.Marshal(importOutcome{Rejection: &rejected})
	if encodeErr != nil {
		return command.Execution[ImportResult]{}, errors.New("encode Event interchange Import rejection")
	}
	return command.RejectEncoded(ImportResult{}, string(encoded), err), nil
}

func replayImport(encoded string) (ImportResult, error) {
	var outcome importOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return ImportResult{}, errors.New("decode Event interchange Import receipt")
	}
	if outcome.Result != nil {
		return *outcome.Result, nil
	}
	if outcome.Rejection == nil {
		return ImportResult{}, errors.New("event interchange Import receipt has no outcome")
	}
	switch outcome.Rejection.Code {
	case "administrator_required":
		return ImportResult{}, ErrAdministratorRequired
	case "unsupported_version":
		return ImportResult{}, ErrUnsupportedVersion
	case "review_mismatch":
		return ImportResult{}, ErrReviewMismatch
	case "invalid_document":
		return ImportResult{}, &ValidationError{
			Field: outcome.Rejection.Field, Message: outcome.Rejection.Message,
		}
	default:
		return ImportResult{}, errors.New("event interchange Import was rejected")
	}
}

func importErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrAdministratorRequired):
		return "administrator_required"
	case errors.Is(err, ErrEventAccessDenied):
		return "event_access_denied"
	case errors.Is(err, ErrUnsupportedVersion):
		return "unsupported_version"
	case errors.Is(err, ErrReviewMismatch):
		return "review_mismatch"
	default:
		return "invalid_document"
	}
}

func draftChanges(stored []store.DraftChangeResult) []rundown.DraftChange {
	changes := make([]rundown.DraftChange, 0, len(stored))
	for _, change := range stored {
		changes = append(changes, rundown.DraftChange{
			ID:               change.ID,
			Kind:             change.Kind,
			TargetType:       change.TargetType,
			TargetID:         change.TargetID,
			FactKey:          change.FactKey,
			Status:           change.Status,
			CurrentValueJSON: change.CurrentValueJSON,
		})
	}
	return changes
}

func refs(ids []int, available map[int]string) []string {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	result := make([]string, 0, len(sorted))
	for _, id := range sorted {
		result = append(result, available[id])
	}
	return result
}

func targetRefs(refs []string) []rundown.TargetRef {
	result := make([]rundown.TargetRef, 0, len(refs))
	for _, ref := range refs {
		result = append(result, rundown.TargetRef{Ref: ref})
	}
	return result
}

func ordinalRef(kind string, index int) string {
	return kind + "-" + strconv.Itoa(index+1)
}

func uniqueRef(seen map[string]struct{}, ref string) error {
	if ref == "" {
		return invalid("ref", "must be non-empty")
	}
	if _, exists := seen[ref]; exists {
		return invalid("ref", "must be unique across the document")
	}
	seen[ref] = struct{}{}
	return nil
}

func hasDraftChanges(input rundown.EditDraftInput) bool {
	return len(input.Locations)+len(input.Lanes)+len(input.Tracks)+len(input.Sessions) > 0
}

func optionalTimeIn(value time.Time, location *time.Location) *time.Time {
	if value.IsZero() {
		return nil
	}
	converted := value.In(location)
	return &converted
}

func dereferenceTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func fingerprint(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

func importPayloadHash(document []byte, reviewFingerprint string) string {
	digest := sha256.New()
	_, _ = digest.Write(document)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(reviewFingerprint))
	_, _ = digest.Write([]byte{0})
	return hex.EncodeToString(digest.Sum(nil))
}

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

// interchangeAuthorizationRejections maps the Capability Table's refusals for
// both Import (installation authority) and Export (Event authority) actions
// back to this package's sentinels, so an evaluated refusal returns the
// error the imperative check returns today.
var interchangeAuthorizationRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
		{Err: ErrEventAccessDenied, Code: "event_access_denied"},
	},
}

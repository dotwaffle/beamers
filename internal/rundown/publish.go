package rundown

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrPublishSelection means Preview cannot form a dependency-valid selection.
	ErrPublishSelection = errors.New("Publish selection is invalid")
	// ErrStalePreview means Publish confirmation no longer matches current state.
	ErrStalePreview = errors.New("Publish Preview is stale")
)

// PublishPreviewInput requests dependency closure for effective Draft Changes.
type PublishPreviewInput struct {
	EventID   int   `json:"event_id"`
	ChangeIDs []int `json:"change_ids,omitempty"`
}

// PublishPreview binds one normalized selection to exact Rundown revisions.
type PublishPreview struct {
	DraftRevision         int           `json:"draft_revision"`
	PublishedRevision     int           `json:"published_revision"`
	ChangeIDs             []int         `json:"change_ids"`
	AutoIncludedChangeIDs []int         `json:"auto_included_change_ids,omitempty"`
	Changes               []DraftChange `json:"changes"`
	Fingerprint           string        `json:"fingerprint"`
	ValidationFailures    []string      `json:"validation_failures,omitempty"`
	AffectedStructure     []string      `json:"affected_structure,omitempty"`
}

// PublishConfirmation is the exact normalized Preview approved by a Producer.
type PublishConfirmation struct {
	DraftRevision     int    `json:"draft_revision"`
	PublishedRevision int    `json:"published_revision"`
	ChangeIDs         []int  `json:"change_ids"`
	Fingerprint       string `json:"fingerprint"`
}

// PublishInput contains one confirmed Publish command.
type PublishInput struct {
	EventID      int                 `json:"event_id"`
	CommandID    string              `json:"command_id"`
	Confirmation PublishConfirmation `json:"confirmation"`
	PublishNote  string              `json:"publish_note,omitempty"`
}

// PublishResult is the minimal committed result of Publish.
type PublishResult struct {
	DraftRevision     int   `json:"draft_revision"`
	PublishedRevision int   `json:"published_revision"`
	ChangeIDs         []int `json:"change_ids"`
}

// Queries owns side-effect-free Rundown projections.
type Queries struct {
	storage *store.SQLite
}

// NewQueries creates Rundown Queries with explicit persistence.
func NewQueries(storage *store.SQLite) (*Queries, error) {
	if storage == nil {
		return nil, errors.New("rundown storage is required")
	}
	return &Queries{storage: storage}, nil
}

// PublishPreview forms and fingerprints a dependency-closed effective selection.
func (queries *Queries) PublishPreview(
	ctx context.Context,
	actor auth.Account,
	input PublishPreviewInput,
) (PublishPreview, error) {
	if !canReadEvent(actor, input.EventID) {
		return PublishPreview{}, ErrEventAccessDenied
	}
	state, err := queries.storage.LoadPublishState(actor.Context(ctx), input.EventID)
	if err != nil {
		return PublishPreview{}, err
	}
	return formPublishPreview(state, input.ChangeIDs)
}

type publishOutcome struct {
	Result    *PublishResult `json:"result,omitempty"`
	Rejection *rejection     `json:"rejection,omitempty"`
}

// Publish atomically creates immutable Published versions for one exact Preview.
func (commands *Commands) Publish(
	ctx context.Context,
	actor auth.Account,
	input PublishInput,
) (PublishResult, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return PublishResult{}, &ValidationError{Field: "command_id", Message: err.Error()}
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return PublishResult{}, errors.New("encode Publish command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "Publish",
		TargetType: "Event", TargetID: strconv.Itoa(input.EventID), Now: commands.now().UTC(),
	}
	transaction, err := commands.storage.BeginCommand(actor.Context(ctx))
	if err != nil {
		return PublishResult{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	original, retry, err := transaction.LookupReceipt(ctx, identity)
	if errors.Is(err, ErrCommandConflict) {
		if commitErr := transaction.CommitConflict(actor.Context(ctx), identity); commitErr != nil {
			return PublishResult{}, commitErr
		}
		return PublishResult{}, ErrCommandConflict
	}
	if err != nil {
		return PublishResult{}, err
	}
	if retry {
		return decodePublishOutcome(original)
	}
	if !actor.CanProduceEvent(input.EventID) {
		return PublishResult{}, commands.rejectPublish(ctx, transaction, actor, identity, rejection{
			Code: "event_access_denied", Message: ErrEventAccessDenied.Error(),
		})
	}
	state, err := transaction.LoadPublishState(actor.Context(ctx), input.EventID)
	if err != nil {
		return PublishResult{}, err
	}
	preview, err := formPublishPreview(state, input.Confirmation.ChangeIDs)
	if err != nil || len(preview.ValidationFailures) > 0 || !confirmationMatches(input.Confirmation, preview) {
		return PublishResult{}, commands.rejectPublish(ctx, transaction, actor, identity, rejection{
			Code: "stale_preview", Message: ErrStalePreview.Error(),
		})
	}
	stored, err := transaction.Publish(actor.Context(ctx), store.PublishParams{
		EventID:                   input.EventID,
		ExpectedDraftRevision:     input.Confirmation.DraftRevision,
		ExpectedPublishedRevision: input.Confirmation.PublishedRevision,
		ChangeIDs:                 input.Confirmation.ChangeIDs,
		Now:                       identity.Now,
	})
	if errors.Is(err, store.ErrDraftRevisionConflict) {
		return PublishResult{}, commands.rejectPublish(ctx, transaction, actor, identity, rejection{
			Code: "stale_preview", Message: ErrStalePreview.Error(),
		})
	}
	if err != nil {
		return PublishResult{}, err
	}
	result := PublishResult{
		DraftRevision: stored.DraftRevision, PublishedRevision: stored.PublishedRevision,
		ChangeIDs: stored.ChangeIDs,
	}
	encoded, err := json.Marshal(publishOutcome{Result: &result})
	if err != nil {
		return PublishResult{}, errors.New("encode Publish outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), false); err != nil {
		return PublishResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (commands *Commands) rejectPublish(
	ctx context.Context,
	transaction *store.CommandTx,
	actor auth.Account,
	identity store.CommandIdentity,
	rejected rejection,
) error {
	encoded, err := json.Marshal(publishOutcome{Rejection: &rejected})
	if err != nil {
		return errors.New("encode rejected Publish outcome")
	}
	if err := transaction.RecordOutcome(actor.Context(ctx), identity, string(encoded), true); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	return publishRejectionError(rejected)
}

func decodePublishOutcome(encoded string) (PublishResult, error) {
	var outcome publishOutcome
	if err := json.Unmarshal([]byte(encoded), &outcome); err != nil {
		return PublishResult{}, errors.New("decode Publish Command Receipt")
	}
	if outcome.Rejection != nil {
		return PublishResult{}, publishRejectionError(*outcome.Rejection)
	}
	if outcome.Result == nil {
		return PublishResult{}, errors.New("Publish Command Receipt has no outcome")
	}
	return *outcome.Result, nil
}

func publishRejectionError(rejected rejection) error {
	switch rejected.Code {
	case "event_access_denied":
		return ErrEventAccessDenied
	case "stale_preview":
		return ErrStalePreview
	default:
		return errors.New("Publish command was rejected")
	}
}

func formPublishPreview(state store.PublishState, requested []int) (PublishPreview, error) {
	preview := PublishPreview{
		DraftRevision: state.DraftRevision, PublishedRevision: state.PublishedRevision,
	}
	byID := make(map[int]store.PendingDraftChange, len(state.Changes))
	for _, change := range state.Changes {
		byID[change.ID] = change
	}
	requestedSet := make(map[int]struct{}, len(requested))
	if len(requested) == 0 {
		for _, change := range state.Changes {
			if change.Status == "Effective" {
				requested = append(requested, change.ID)
			}
		}
	}
	for _, id := range requested {
		requestedSet[id] = struct{}{}
	}
	selected := make(map[int]struct{}, len(requested))
	var visit func(int) error
	visit = func(id int) error {
		change, exists := byID[id]
		if !exists {
			return ErrPublishSelection
		}
		if change.Status == "Published" {
			return nil
		}
		if change.Status != "Effective" {
			return ErrPublishSelection
		}
		if _, exists := selected[id]; exists {
			return nil
		}
		selected[id] = struct{}{}
		for _, dependencyID := range change.Dependencies {
			if err := visit(dependencyID); err != nil {
				return err
			}
		}
		return nil
	}
	validationFailure := ""
	for _, id := range requested {
		if visitErr := visit(id); visitErr != nil {
			validationFailure = visitErr.Error()
			break
		}
	}
	if validationFailure != "" {
		preview.ValidationFailures = []string{validationFailure}
		return preview, nil
	}
	if len(selected) == 0 {
		preview.ValidationFailures = []string{ErrPublishSelection.Error()}
		return preview, nil
	}
	changeIDs := make([]int, 0, len(selected))
	for id := range selected {
		changeIDs = append(changeIDs, id)
	}
	sort.Ints(changeIDs)
	preview.ChangeIDs = changeIDs
	preview.Changes = make([]DraftChange, 0, len(changeIDs))
	affected := make(map[string]struct{})
	fingerprintValues := []string{
		strconv.Itoa(state.DraftRevision), strconv.Itoa(state.PublishedRevision),
	}
	for _, id := range changeIDs {
		change := byID[id]
		preview.Changes = append(preview.Changes, DraftChange{
			ID: id, Kind: change.Kind, TargetType: change.TargetType, TargetID: change.TargetID,
		})
		affected[change.TargetType] = struct{}{}
		if _, explicitlyRequested := requestedSet[id]; !explicitlyRequested {
			preview.AutoIncludedChangeIDs = append(preview.AutoIncludedChangeIDs, id)
		}
		fingerprintValues = append(fingerprintValues, strconv.Itoa(id), change.PayloadJSON)
	}
	for targetType := range affected {
		preview.AffectedStructure = append(preview.AffectedStructure, targetType)
	}
	sort.Strings(preview.AffectedStructure)
	preview.Fingerprint = command.PayloadHash(fingerprintValues...)
	return preview, nil
}

func confirmationMatches(confirmation PublishConfirmation, preview PublishPreview) bool {
	return confirmation.DraftRevision == preview.DraftRevision &&
		confirmation.PublishedRevision == preview.PublishedRevision &&
		confirmation.Fingerprint == preview.Fingerprint &&
		slices.Equal(confirmation.ChangeIDs, preview.ChangeIDs)
}

func canReadEvent(actor auth.Account, eventID int) bool {
	_, exists := actor.EventRoles[eventID]
	return exists
}

// CrewRundown is the current Published structural projection for authorized crew.
type CrewRundown struct {
	DraftRevision     int            `json:"draft_revision"`
	PublishedRevision int            `json:"published_revision"`
	Locations         []CrewLocation `json:"locations"`
	Lanes             []CrewLane     `json:"lanes"`
	Tracks            []CrewTrack    `json:"tracks"`
	Sessions          []CrewSession  `json:"sessions"`
}

// CrewLocation is one current Published Location.
type CrewLocation struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// CrewLane is one current Published Lane.
type CrewLane struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	LocationID int    `json:"location_id"`
}

// CrewTrack is one current Published Track.
type CrewTrack struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// CrewSession is one current Published Session with crew-only detail.
type CrewSession struct {
	ID                 int                `json:"id"`
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
	LaneIDs            []int              `json:"lane_ids"`
	LocationIDs        []int              `json:"location_ids"`
	TrackIDs           []int              `json:"track_ids"`
}

// CrewRundown returns current Published structure only through a purpose-built projection.
func (queries *Queries) CrewRundown(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (CrewRundown, error) {
	if !canReadEvent(actor, eventID) {
		return CrewRundown{}, ErrEventAccessDenied
	}
	stored, err := queries.storage.LoadCrewRundown(actor.Context(ctx), eventID)
	if err != nil {
		return CrewRundown{}, err
	}
	result := CrewRundown{
		DraftRevision: stored.DraftRevision, PublishedRevision: stored.PublishedRevision,
		Locations: make([]CrewLocation, 0, len(stored.Locations)),
		Lanes:     make([]CrewLane, 0, len(stored.Lanes)),
		Tracks:    make([]CrewTrack, 0, len(stored.Tracks)),
		Sessions:  make([]CrewSession, 0, len(stored.Sessions)),
	}
	for _, item := range stored.Locations {
		result.Locations = append(result.Locations, CrewLocation{ID: item.ID, Name: item.Name})
	}
	for _, item := range stored.Lanes {
		result.Lanes = append(result.Lanes, CrewLane{ID: item.ID, Name: item.Name, LocationID: item.LocationID})
	}
	for _, item := range stored.Tracks {
		result.Tracks = append(result.Tracks, CrewTrack{ID: item.ID, Name: item.Name})
	}
	for _, item := range stored.Sessions {
		result.Sessions = append(result.Sessions, CrewSession{
			ID: item.ID, Title: item.Title, Type: SessionType(item.Type),
			AudienceVisibility: AudienceVisibility(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails, CrewNotes: item.CrewNotes,
			PlannedStart: item.PlannedStart, PlannedEnd: item.PlannedEnd,
			TimingPolicy:    TimingPolicy(item.TimingPolicy),
			MinimumDuration: time.Duration(item.MinimumDurationSeconds) * time.Second,
			StartBoundary:   Boundary(item.StartBoundary), EndBoundary: Boundary(item.EndBoundary),
			LaneIDs: item.LaneIDs, LocationIDs: item.LocationIDs, TrackIDs: item.TrackIDs,
		})
	}
	return result, nil
}

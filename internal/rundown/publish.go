package rundown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/revisioncache"
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
	AffectedLanes         []string      `json:"affected_lanes,omitempty"`
	AffectedDisplays      []string      `json:"affected_displays,omitempty"`
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

// crewRundownKey identifies one Crew Rundown build exactly. Structural change
// moves a revision; live change moves the stream cursor the Displays already
// resume from.
type crewRundownKey struct {
	draftRevision     int
	publishedRevision int
	streamPosition    uint64
}

// crewRundownCacheEvents bounds how many Events keep a memoized build. Crew
// work concentrates on the live Event and the one being planned next, so a
// small allowance covers real use; an installation that exceeds it starts over
// rather than retaining a projection per Event it has ever served.
const crewRundownCacheEvents = 8

// Queries owns side-effect-free Rundown projections.
type Queries struct {
	storage *store.SQLite
	// streamPosition reports the cursor that advances on every live change
	// visible in the Crew Rundown. A nil streamPosition disables memoization.
	streamPosition func() uint64
	// crewRundowns memoizes one build per Event. Separate Events get separate
	// caches so a Producer planning next year's Event neither evicts nor waits
	// behind the live Event's rebuild.
	crewRundownMutex sync.Mutex
	crewRundowns     map[int]*revisioncache.Cache[crewRundownKey, CrewRundown]
}

// crewRundownCache returns the memo for one Event, creating it on first use.
func (queries *Queries) crewRundownCache(
	eventID int,
) *revisioncache.Cache[crewRundownKey, CrewRundown] {
	queries.crewRundownMutex.Lock()
	defer queries.crewRundownMutex.Unlock()
	if queries.crewRundowns == nil || len(queries.crewRundowns) >= crewRundownCacheEvents {
		queries.crewRundowns = make(
			map[int]*revisioncache.Cache[crewRundownKey, CrewRundown],
			crewRundownCacheEvents,
		)
	}
	found, ok := queries.crewRundowns[eventID]
	if !ok {
		found = &revisioncache.Cache[crewRundownKey, CrewRundown]{}
		queries.crewRundowns[eventID] = found
	}
	return found
}

// NewQueries creates Rundown Queries with explicit persistence.
//
// streamPosition reports the display stream cursor. Crew Rundown builds are
// memoized against it together with the Event's Draft and Published revisions,
// so it must advance whenever live state changes. Pass nil to rebuild the
// projection on every request.
func NewQueries(storage *store.SQLite, streamPosition func() uint64) (*Queries, error) {
	if storage == nil {
		return nil, errors.New("rundown storage is required")
	}
	return &Queries{storage: storage, streamPosition: streamPosition}, nil
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
	preview, err := formPublishPreview(state, input.ChangeIDs)
	if err != nil {
		return PublishPreview{}, err
	}
	if len(preview.ChangeIDs) == 0 {
		return preview, nil
	}
	draft, err := queries.storage.LoadDraftRundown(actor.Context(ctx), input.EventID)
	if err != nil {
		return PublishPreview{}, err
	}
	published, err := queries.storage.LoadCrewRundown(actor.Context(ctx), input.EventID)
	if err != nil {
		return PublishPreview{}, err
	}
	displays, err := queries.storage.LoadPublishImpactDisplays(actor.Context(ctx), input.EventID)
	if err != nil {
		return PublishPreview{}, err
	}
	if err := addPublishImpact(&preview, published, draft, displays); err != nil {
		return PublishPreview{}, err
	}
	return preview, nil
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
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[PublishResult]{
		Storage: commands.storage, Identity: identity, Replay: decodePublishOutcome,
		Notify: commands.notifyPublishedRundown,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: rundownAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[PublishResult], error) {
			state, loadErr := transaction.LoadPublishState(actor.Context(ctx), input.EventID)
			if loadErr != nil {
				return command.Execution[PublishResult]{}, loadErr
			}
			preview, previewErr := formPublishPreview(state, input.Confirmation.ChangeIDs)
			if previewErr != nil || len(preview.ValidationFailures) > 0 || !confirmationMatches(input.Confirmation, preview) {
				return publishRejection(rejection{Code: "stale_preview", Message: ErrStalePreview.Error()})
			}
			stored, publishErr := transaction.Publish(actor.Context(ctx), store.PublishParams{
				EventID:                   input.EventID,
				ExpectedDraftRevision:     input.Confirmation.DraftRevision,
				ExpectedPublishedRevision: input.Confirmation.PublishedRevision,
				ChangeIDs:                 input.Confirmation.ChangeIDs,
				Now:                       identity.Now,
			})
			if errors.Is(publishErr, store.ErrDraftRevisionConflict) {
				return publishRejection(rejection{Code: "stale_preview", Message: ErrStalePreview.Error()})
			}
			if publishErr != nil {
				return command.Execution[PublishResult]{}, publishErr
			}
			result := PublishResult{
				DraftRevision: stored.DraftRevision, PublishedRevision: stored.PublishedRevision,
				ChangeIDs: stored.ChangeIDs,
			}
			encoded, encodeErr := json.Marshal(publishOutcome{Result: &result})
			if encodeErr != nil {
				return command.Execution[PublishResult]{}, errors.New("encode Publish outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func publishRejection(rejected rejection) (command.Execution[PublishResult], error) {
	encoded, err := json.Marshal(publishOutcome{Rejection: &rejected})
	if err != nil {
		return command.Execution[PublishResult]{}, errors.New("encode rejected Publish outcome")
	}
	return command.RejectEncoded(PublishResult{}, string(encoded), publishRejectionError(rejected)), nil
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
	reviewAll := len(requested) == 0
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
		if change.Status == "Conflicted" {
			return errors.New("draft fact conflicts with a live detail correction and requires review")
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
		if reviewAll {
			for _, change := range state.Changes {
				if change.Status == "Conflicted" {
					visible, err := visibleDraftChange(change)
					if err != nil {
						return PublishPreview{}, err
					}
					preview.Changes = append(preview.Changes, visible)
				}
			}
			if len(preview.Changes) != 0 {
				preview.ValidationFailures = []string{"Resolve conflicted Draft facts by editing them before publishing."}
			}
		}
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
		if slices.Contains(state.LiveTargetIDs[change.TargetType], change.TargetID) {
			preview.ValidationFailures = []string{"ordinary Publish cannot alter a currently Live Session"}
			return preview, nil
		}
		visible, err := visibleDraftChange(change)
		if err != nil {
			return PublishPreview{}, err
		}
		preview.Changes = append(preview.Changes, visible)
		affected[change.TargetType] = struct{}{}
		if _, explicitlyRequested := requestedSet[id]; !explicitlyRequested {
			preview.AutoIncludedChangeIDs = append(preview.AutoIncludedChangeIDs, id)
		}
		fingerprintValues = append(fingerprintValues, strconv.Itoa(id), change.PayloadJSON)
	}
	if reviewAll {
		for _, change := range state.Changes {
			if change.Status == "Conflicted" {
				visible, err := visibleDraftChange(change)
				if err != nil {
					return PublishPreview{}, err
				}
				preview.Changes = append(preview.Changes, visible)
			}
		}
	}
	for targetType := range affected {
		preview.AffectedStructure = append(preview.AffectedStructure, targetType)
	}
	sort.Strings(preview.AffectedStructure)
	preview.Fingerprint = command.PayloadHash(fingerprintValues...)
	return preview, nil
}

func visibleDraftChange(change store.PendingDraftChange) (DraftChange, error) {
	var evidence struct {
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal([]byte(change.PayloadJSON), &evidence); err != nil {
		return DraftChange{}, fmt.Errorf("decode Draft Change %d evidence: %w", change.ID, err)
	}
	if len(evidence.After) == 0 {
		return DraftChange{}, fmt.Errorf("decode Draft Change %d evidence: missing after value", change.ID)
	}
	return DraftChange{
		ID: change.ID, Kind: change.Kind, TargetType: change.TargetType,
		TargetID: change.TargetID, FactKey: change.FactKey, Status: change.Status,
		PreviousValueJSON: string(evidence.Before),
		CurrentValueJSON:  string(evidence.After),
	}, nil
}

type publishImpactTarget struct {
	ID  int    `json:"ID"`
	Ref string `json:"Ref"`
}

func addPublishImpact(
	preview *PublishPreview,
	published store.CrewRundownState,
	draft store.DraftRundownState,
	displays []store.PublishImpactDisplay,
) error {
	type sessionCreation struct {
		Lanes     []publishImpactTarget `json:"Lanes"`
		Locations []publishImpactTarget `json:"Locations"`
		Tracks    []publishImpactTarget `json:"Tracks"`
	}
	type laneCreation struct {
		Location publishImpactTarget `json:"Location"`
	}
	selected := make(map[int]struct{}, len(preview.ChangeIDs))
	for _, id := range preview.ChangeIDs {
		selected[id] = struct{}{}
	}
	sessions := make(map[int]store.PublishedSession, len(published.Sessions))
	publishedSessionIDs := make(map[int]struct{}, len(published.Sessions))
	for _, session := range published.Sessions {
		sessions[session.ID] = session
		publishedSessionIDs[session.ID] = struct{}{}
	}
	previousSessions := make(map[int]store.PublishedSession)
	for _, change := range preview.Changes {
		if _, ok := selected[change.ID]; !ok || change.TargetType != "Session" {
			continue
		}
		session, exists := sessions[change.TargetID]
		if !exists {
			var created sessionCreation
			if err := json.Unmarshal([]byte(change.CurrentValueJSON), &created); err != nil {
				return fmt.Errorf("decode Session %d Publish impact: %w", change.TargetID, err)
			}
			session = store.PublishedSession{
				ID: change.TargetID, LaneIDs: draftTargetIDs(created.Lanes),
				LocationIDs: draftTargetIDs(created.Locations),
				TrackIDs:    draftTargetIDs(created.Tracks),
			}
			sessions[session.ID] = session
		}
		_, wasPublished := publishedSessionIDs[session.ID]
		if _, captured := previousSessions[session.ID]; wasPublished && !captured {
			previous := session
			previous.LaneIDs = slices.Clone(session.LaneIDs)
			previous.LocationIDs = slices.Clone(session.LocationIDs)
			previous.TrackIDs = slices.Clone(session.TrackIDs)
			previousSessions[session.ID] = previous
		}
		family, encodedID, membership := strings.Cut(change.FactKey, ":")
		if !membership {
			continue
		}
		id, err := strconv.Atoi(encodedID)
		if err != nil {
			return fmt.Errorf("decode Draft membership %q: %w", change.FactKey, err)
		}
		var present bool
		if err := json.Unmarshal([]byte(change.CurrentValueJSON), &present); err != nil {
			return fmt.Errorf("decode Draft membership %q value: %w", change.FactKey, err)
		}
		switch family {
		case "lanes":
			session.LaneIDs = setMembership(session.LaneIDs, id, present)
		case "locations":
			session.LocationIDs = setMembership(session.LocationIDs, id, present)
		case "tracks":
			session.TrackIDs = setMembership(session.TrackIDs, id, present)
		}
		sessions[session.ID] = session
	}
	laneLocations := make(map[int]int, len(published.Lanes))
	for _, lane := range published.Lanes {
		laneLocations[lane.ID] = lane.LocationID
	}
	for _, lane := range draft.Lanes {
		if _, exists := laneLocations[lane.ID]; !exists {
			laneLocations[lane.ID] = lane.LocationID
		}
	}
	for _, change := range preview.Changes {
		if _, ok := selected[change.ID]; !ok || change.TargetType != "Lane" {
			continue
		}
		if change.FactKey == "entity" {
			var created laneCreation
			if err := json.Unmarshal([]byte(change.CurrentValueJSON), &created); err != nil {
				return fmt.Errorf("decode Lane %d Publish impact: %w", change.TargetID, err)
			}
			laneLocations[change.TargetID] = created.Location.ID
		}
		if change.FactKey == "location" {
			var locationID int
			if err := json.Unmarshal([]byte(change.CurrentValueJSON), &locationID); err != nil {
				return fmt.Errorf("decode Lane %d Location impact: %w", change.TargetID, err)
			}
			laneLocations[change.TargetID] = locationID
		}
	}
	laneIDs := make(map[int]struct{})
	locationIDs := make(map[int]struct{})
	trackIDs := make(map[int]struct{})
	for _, change := range preview.Changes {
		if _, ok := selected[change.ID]; !ok {
			continue
		}
		switch change.TargetType {
		case "Lane":
			laneIDs[change.TargetID] = struct{}{}
			if change.FactKey == "location" {
				var previousLocationID int
				if err := json.Unmarshal(
					[]byte(change.PreviousValueJSON),
					&previousLocationID,
				); err != nil {
					return fmt.Errorf("decode Lane %d previous Location impact: %w", change.TargetID, err)
				}
				locationIDs[previousLocationID] = struct{}{}
			}
		case "Location":
			locationIDs[change.TargetID] = struct{}{}
		case "Track":
			trackIDs[change.TargetID] = struct{}{}
		case "Session":
			session := sessions[change.TargetID]
			for _, id := range session.LaneIDs {
				laneIDs[id] = struct{}{}
			}
			for _, id := range session.LocationIDs {
				locationIDs[id] = struct{}{}
			}
			previous := previousSessions[change.TargetID]
			for _, id := range previous.LaneIDs {
				laneIDs[id] = struct{}{}
			}
			for _, id := range previous.LocationIDs {
				locationIDs[id] = struct{}{}
			}
		}
	}
	for _, session := range sessions {
		if !intersects(session.TrackIDs, trackIDs) {
			continue
		}
		for _, id := range session.LaneIDs {
			laneIDs[id] = struct{}{}
		}
		for _, id := range session.LocationIDs {
			locationIDs[id] = struct{}{}
		}
	}
	for laneID, locationID := range laneLocations {
		if _, selectedLane := laneIDs[laneID]; selectedLane {
			locationIDs[locationID] = struct{}{}
			preview.AffectedLanes = append(preview.AffectedLanes, identityLabel("Lane", laneID, ""))
		}
		if _, selectedLocation := locationIDs[locationID]; selectedLocation {
			if _, alreadySelected := laneIDs[laneID]; !alreadySelected {
				preview.AffectedLanes = append(preview.AffectedLanes, identityLabel("Lane", laneID, ""))
			}
		}
	}
	for _, display := range displays {
		if _, ok := locationIDs[display.LocationID]; ok {
			preview.AffectedDisplays = append(
				preview.AffectedDisplays,
				identityLabel("Display", display.ID, display.Name),
			)
		}
	}
	sort.Strings(preview.AffectedLanes)
	sort.Strings(preview.AffectedDisplays)
	return nil
}

func intersects(ids []int, selected map[int]struct{}) bool {
	for _, id := range ids {
		if _, ok := selected[id]; ok {
			return true
		}
	}
	return false
}

func identityLabel(kind string, id int, name string) string {
	label := kind + " #" + strconv.Itoa(id)
	if name != "" {
		label += " " + name
	}
	return label
}

func setMembership(ids []int, id int, present bool) []int {
	index := slices.Index(ids, id)
	if present && index < 0 {
		return append(ids, id)
	}
	if !present && index >= 0 {
		return slices.Delete(ids, index, index+1)
	}
	return ids
}

func draftTargetIDs(targets []publishImpactTarget) []int {
	ids := make([]int, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.ID)
	}
	return ids
}

func confirmationMatches(confirmation PublishConfirmation, preview PublishPreview) bool {
	return confirmation.DraftRevision == preview.DraftRevision &&
		confirmation.PublishedRevision == preview.PublishedRevision &&
		confirmation.Fingerprint == preview.Fingerprint &&
		slices.Equal(confirmation.ChangeIDs, preview.ChangeIDs)
}

func canReadEvent(actor auth.Account, eventID int) bool {
	return authz.Holds(actor.Identity(), eventID, authz.ViewEventCrew)
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

// DraftRundown is the current materialized editable structural projection.
type DraftRundown CrewRundown

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
	UploadDeadline     time.Time          `json:"upload_deadline,omitzero"`
	SubmissionDeadline time.Time          `json:"submission_deadline,omitzero"`
	EntryDefault       EntryDisposition   `json:"entry_default_disposition,omitempty"`
	LaneIDs            []int              `json:"lane_ids"`
	LocationIDs        []int              `json:"location_ids"`
	TrackIDs           []int              `json:"track_ids"`
	Lifecycle          SessionLifecycle   `json:"lifecycle"`
	LiveStateRevision  int                `json:"live_state_revision"`
	ForecastStart      time.Time          `json:"forecast_start"`
	ForecastEnd        time.Time          `json:"forecast_end"`
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
	scoped := actor.Context(ctx)
	if queries.streamPosition == nil {
		return queries.buildCrewRundown(scoped, eventID)
	}
	revisions, err := queries.storage.LoadRundownRevisions(scoped, eventID)
	if err != nil {
		return CrewRundown{}, err
	}
	key := crewRundownKey{
		draftRevision:     revisions.DraftRevision,
		publishedRevision: revisions.PublishedRevision,
		streamPosition:    queries.streamPosition(),
	}
	cache := queries.crewRundownCache(eventID)
	return cache.Load(scoped, key, func(ctx context.Context) (CrewRundown, error) {
		return queries.buildCrewRundown(ctx, eventID)
	})
}

// buildCrewRundown loads and projects the whole Published structure.
func (queries *Queries) buildCrewRundown(ctx context.Context, eventID int) (CrewRundown, error) {
	stored, err := queries.storage.LoadCrewRundown(ctx, eventID)
	if err != nil {
		return CrewRundown{}, err
	}
	return projectRundown(
		stored.DraftRevision,
		stored.PublishedRevision,
		stored.Locations,
		stored.Lanes,
		stored.Tracks,
		stored.Sessions,
	), nil
}

// DisplayLocations returns the narrow Published Location projection needed for Display routing.
func (queries *Queries) DisplayLocations(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]CrewLocation, error) {
	if !authz.Holds(actor.Identity(), 0, authz.AdministerDisplays) {
		return nil, ErrEventAccessDenied
	}
	stored, err := queries.storage.LoadDisplayLocations(actor.Context(ctx), eventID)
	if err != nil {
		return nil, err
	}
	result := make([]CrewLocation, 0, len(stored))
	for _, location := range stored {
		result = append(result, CrewLocation{ID: location.ID, Name: location.Name})
	}
	return result, nil
}

// AdministrationLanes returns Published Lanes for the Administrator Event
// Grant Lane picker, independent of whether the Administrator also holds an
// Event Grant on eventID: an Administrator routinely grants Lane-scoped
// access to Events they do not themselves work.
func (queries *Queries) AdministrationLanes(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]CrewLane, error) {
	if !authz.Holds(actor.Identity(), 0, authz.AdministerEvents) {
		return nil, ErrEventAccessDenied
	}
	stored, err := queries.storage.LoadAdministrationLanes(actor.Context(ctx), eventID)
	if err != nil {
		return nil, err
	}
	result := make([]CrewLane, 0, len(stored))
	for _, lane := range stored {
		result = append(result, CrewLane{ID: lane.ID, Name: lane.Name, LocationID: lane.LocationID})
	}
	return result, nil
}

// DraftRundown returns current materialized Draft state for an authorized Producer.
func (queries *Queries) DraftRundown(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (DraftRundown, error) {
	if !authz.Holds(actor.Identity(), eventID, authz.ConfigureRundown) {
		return DraftRundown{}, ErrEventAccessDenied
	}
	stored, err := queries.storage.LoadDraftRundown(actor.Context(ctx), eventID)
	if err != nil {
		return DraftRundown{}, err
	}
	return DraftRundown(projectRundown(
		stored.DraftRevision,
		stored.PublishedRevision,
		stored.Locations,
		stored.Lanes,
		stored.Tracks,
		stored.Sessions,
	)), nil
}

func projectRundown(
	draftRevision, publishedRevision int,
	locations []store.PublishedLocation,
	lanes []store.PublishedLane,
	tracks []store.PublishedTrack,
	sessions []store.PublishedSession,
) CrewRundown {
	result := CrewRundown{
		DraftRevision: draftRevision, PublishedRevision: publishedRevision,
		Locations: make([]CrewLocation, 0, len(locations)),
		Lanes:     make([]CrewLane, 0, len(lanes)),
		Tracks:    make([]CrewTrack, 0, len(tracks)),
		Sessions:  make([]CrewSession, 0, len(sessions)),
	}
	for _, item := range locations {
		result.Locations = append(result.Locations, CrewLocation{ID: item.ID, Name: item.Name})
	}
	for _, item := range lanes {
		result.Lanes = append(result.Lanes, CrewLane{ID: item.ID, Name: item.Name, LocationID: item.LocationID})
	}
	for _, item := range tracks {
		result.Tracks = append(result.Tracks, CrewTrack{ID: item.ID, Name: item.Name})
	}
	for _, item := range sessions {
		result.Sessions = append(result.Sessions, CrewSession{
			ID: item.ID, Title: item.Title, Speaker: item.Speaker, Type: SessionType(item.Type),
			AudienceVisibility: AudienceVisibility(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails, CrewNotes: item.CrewNotes,
			PlannedStart: item.PlannedStart, PlannedEnd: item.PlannedEnd,
			TimingPolicy:    TimingPolicy(item.TimingPolicy),
			MinimumDuration: time.Duration(item.MinimumDurationSeconds) * time.Second,
			StartBoundary:   Boundary(item.StartBoundary), EndBoundary: Boundary(item.EndBoundary),
			UploadDeadline: item.UploadDeadline, SubmissionDeadline: item.SubmissionDeadline,
			EntryDefault: EntryDisposition(item.EntryDefaultDisposition),
			LaneIDs:      item.LaneIDs, LocationIDs: item.LocationIDs, TrackIDs: item.TrackIDs,
			Lifecycle: SessionLifecycle(item.Lifecycle), LiveStateRevision: item.LiveStateRevision,
			ForecastStart: item.ForecastStart, ForecastEnd: item.ForecastEnd,
		})
	}
	return result
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"entgo.io/ent/privacy"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/draftchange"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessioncancellation"
	"github.com/dotwaffle/beamers/ent/sessiondraft"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/ent/sessionrunamendment"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrSessionNotFound means the Event has no Published Session with that identity.
	ErrSessionNotFound = errors.New("session not found")
	// ErrLiveStateRevisionConflict means a Session changed after the caller observed it.
	ErrLiveStateRevisionConflict = errors.New("live state revision conflict")
	// ErrSessionLifecycleTransition means the requested progression is not valid now.
	ErrSessionLifecycleTransition = errors.New("invalid Session lifecycle transition")
	// ErrEventNotActive means the command targeted an Event outside live authority.
	ErrEventNotActive = errors.New("event is not active")
	// ErrSessionScopeRequired means the actor cannot control every Session Lane.
	ErrSessionScopeRequired = errors.New("session Lane scope required")
)

// SessionRunSnapshot contains immutable Published context captured by Start and
// the exact Competition Entry order completed once by the first Entry Slide Take.
type SessionRunSnapshot struct {
	PublishedRevision      int       `json:"published_revision"`
	Title                  string    `json:"title"`
	Speaker                string    `json:"speaker,omitempty"`
	Type                   string    `json:"type"`
	PublicDetails          string    `json:"public_details,omitempty"`
	PlannedStart           time.Time `json:"planned_start"`
	PlannedEnd             time.Time `json:"planned_end"`
	TimingPolicy           string    `json:"timing_policy"`
	MinimumDurationSeconds int       `json:"minimum_duration_seconds"`
	StartBoundary          string    `json:"start_boundary"`
	EndBoundary            string    `json:"end_boundary"`
	LaneIDs                []int     `json:"lane_ids"`
	LocationIDs            []int     `json:"location_ids"`
	TrackIDs               []int     `json:"track_ids,omitempty"`
	LockedEntryOrderIDs    []int     `json:"locked_entry_order_ids,omitempty"`
}

// SessionDetails are the correctable descriptive facts for one Session.
type SessionDetails struct {
	Title         string `json:"title"`
	Speaker       string `json:"speaker,omitempty"`
	PublicDetails string `json:"public_details,omitempty"`
}

// LiveDetailCorrectionParams identifies one confirmed descriptive correction.
type LiveDetailCorrectionParams struct {
	EventID          int
	SessionID        int
	ActorAccountID   int
	ExpectedRevision int
	Fields           []string
	Details          SessionDetails
	Now              time.Time
}

// LiveDetailCorrection is one committed correction and its immutable amendment.
type LiveDetailCorrection struct {
	State       LiveSessionState `json:"state"`
	AmendmentID int              `json:"amendment_id"`
	Details     SessionDetails   `json:"details"`
}

// RunAmendment is immutable descriptive correction evidence for one Run.
type RunAmendment struct {
	ID            int
	Details       SessionDetails
	ChangedFields []string
	CreatedAt     time.Time
}

// SessionRunHistory preserves one Run Snapshot and all later amendments.
type SessionRunHistory struct {
	ID          int
	ActualStart time.Time
	ActualEnd   *time.Time
	Snapshot    SessionRunSnapshot
	Outcome     string
	Amendments  []RunAmendment
}

// SessionCancellationHistory preserves one cancellation command.
type SessionCancellationHistory struct {
	ID            int
	SessionRunID  *int
	PublicMessage string
	CrewNotes     string
	ForecastStart time.Time
	CanceledAt    time.Time
}

// SessionHistory contains complete Run and cancellation evidence.
type SessionHistory struct {
	Runs          []SessionRunHistory
	Cancellations []SessionCancellationHistory
}

// LiveSessionState is the durable outcome of one Session progression command.
type LiveSessionState struct {
	SessionID         int        `json:"session_id"`
	SessionRunID      int        `json:"session_run_id"`
	Lifecycle         string     `json:"lifecycle"`
	LiveStateRevision int        `json:"live_state_revision"`
	ActualStart       time.Time  `json:"actual_start"`
	ActualEnd         *time.Time `json:"actual_end,omitempty"`
}

// StartSession creates one Run and advances one Scheduled Session to Live atomically.
func (transaction *CommandTx) StartSession(
	ctx context.Context,
	eventID int,
	sessionID int,
	expectedRevision int,
	now time.Time,
) (LiveSessionState, error) {
	if err := transaction.requireActiveEvent(ctx, eventID); err != nil {
		return LiveSessionState{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return LiveSessionState{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session live state", err)
	}
	snapshot, err := sessionRunSnapshot(ctx, identity)
	if err != nil {
		return LiveSessionState{}, err
	}
	if scopeErr := requireSessionLaneScope(ctx, eventID, snapshot.LaneIDs); scopeErr != nil {
		return LiveSessionState{}, scopeErr
	}
	if identity.LiveStateRevision != expectedRevision {
		return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
	}
	if identity.Lifecycle != session.LifecycleScheduled {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	if snapshot.Type == "Competition" {
		preflight, preflightErr := transaction.PreflightCompetitionStart(ctx, eventID, sessionID)
		if preflightErr != nil {
			return LiveSessionState{}, preflightErr
		}
		if len(preflight.Blockers) > 0 {
			return LiveSessionState{}, &CompetitionPreflightBlockedError{Blockers: preflight.Blockers}
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return LiveSessionState{}, opaqueError("encode Session Run Snapshot", err)
	}
	communicatedStart := identity.ForecastStart
	if communicatedStart.IsZero() {
		communicatedStart = snapshot.PlannedStart
	}
	updated, err := transaction.transaction.Session.UpdateOneID(sessionID).
		Where(
			session.EventIDEQ(eventID),
			session.LiveStateRevisionEQ(expectedRevision),
			session.LifecycleEQ(session.LifecycleScheduled),
		).
		SetLifecycle(session.LifecycleLive).
		SetCommunicatedStart(communicatedStart).
		ClearCommunicatedEnd().
		SetForecastStart(now).
		SetForecastEnd(initialForecastEnd(snapshot, now)).
		AddLiveStateRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return transaction.currentLiveSessionState(ctx, eventID, sessionID)
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("start Session", err)
	}
	run, err := transaction.transaction.SessionRun.Create().
		SetSessionID(sessionID).
		SetActualStart(now).
		SetSnapshotJSON(string(encoded)).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return LiveSessionState{}, opaqueError("create Session Run", err)
	}
	if cueErr := transaction.fireBoundAttachmentReleaseCue(ctx, eventID, sessionID, now); cueErr != nil {
		return LiveSessionState{}, cueErr
	}
	return LiveSessionState{
		SessionID: updated.ID, SessionRunID: run.ID,
		Lifecycle: updated.Lifecycle.String(), LiveStateRevision: updated.LiveStateRevision,
		ActualStart: run.ActualStart,
	}, nil
}

// EndSession records Actual End and advances one Live Session to Ended atomically.
func (transaction *CommandTx) EndSession(
	ctx context.Context,
	eventID int,
	sessionID int,
	expectedRevision int,
	confirmedDeferredEntries bool,
	deferredEntriesFingerprint string,
	now time.Time,
) (LiveSessionState, error) {
	if err := transaction.requireActiveEvent(ctx, eventID); err != nil {
		return LiveSessionState{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return LiveSessionState{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session live state", err)
	}
	if scopeErr := requireSessionControlScope(ctx, identity); scopeErr != nil {
		return LiveSessionState{}, scopeErr
	}
	if identity.LiveStateRevision != expectedRevision {
		return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
	}
	if identity.Lifecycle != session.LifecycleLive {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	version, err := identity.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if err != nil {
		return LiveSessionState{}, opaqueError("load Session type before End", err)
	}
	if version.Type == sessionpublishedversion.TypeCompetition {
		if confirmErr := transaction.confirmCompetitionEnd(
			ctx,
			eventID,
			sessionID,
			confirmedDeferredEntries,
			deferredEntriesFingerprint,
		); confirmErr != nil {
			return LiveSessionState{}, confirmErr
		}
	}
	communicatedEnd := identity.ForecastEnd
	run, err := transaction.transaction.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(sessionID), sessionrun.ActualEndIsNil(),
	).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionLifecycleTransition
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load Live Session Run", err)
	}
	update := transaction.transaction.Session.UpdateOneID(sessionID).
		Where(
			session.EventIDEQ(eventID),
			session.LiveStateRevisionEQ(expectedRevision),
			session.LifecycleEQ(session.LifecycleLive),
		).
		SetLifecycle(session.LifecycleEnded)
	if !communicatedEnd.IsZero() {
		update = update.SetCommunicatedEnd(communicatedEnd)
	}
	updated, err := update.
		AddLiveStateRevision(1).
		Save(ctx)
	if ent.IsNotFound(err) {
		return transaction.currentLiveSessionState(ctx, eventID, sessionID)
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("end Session", err)
	}
	endedRun, err := transaction.transaction.SessionRun.UpdateOneID(run.ID).
		Where(sessionrun.ActualEndIsNil()).
		SetActualEnd(now).
		SetOutcome(sessionrun.OutcomeCompleted).
		Save(ctx)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, opaqueError("end Session Run", errors.New("open session run disappeared"))
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("end Session Run", err)
	}
	actualEnd := endedRun.ActualEnd
	return LiveSessionState{
		SessionID: updated.ID, SessionRunID: endedRun.ID,
		Lifecycle: updated.Lifecycle.String(), LiveStateRevision: updated.LiveStateRevision,
		ActualStart: endedRun.ActualStart, ActualEnd: &actualEnd,
	}, nil
}

type runAmendmentEvidence struct {
	Before        SessionDetails `json:"before"`
	After         SessionDetails `json:"after"`
	ChangedFields []string       `json:"changed_fields"`
}

// CorrectLiveDetails records one immutable amendment without rewriting the Run Snapshot.
func (transaction *CommandTx) CorrectLiveDetails(
	ctx context.Context,
	params LiveDetailCorrectionParams,
) (LiveDetailCorrection, error) {
	if err := transaction.requireActiveEvent(ctx, params.EventID); err != nil {
		return LiveDetailCorrection{}, err
	}
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(params.SessionID), session.EventIDEQ(params.EventID),
	).Only(ctx)
	if errors.Is(err, privacy.Deny) {
		return LiveDetailCorrection{}, ErrSessionScopeRequired
	}
	if ent.IsNotFound(err) {
		return LiveDetailCorrection{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveDetailCorrection{}, opaqueError("load Session for Live Detail Correction", err)
	}
	if scopeErr := requireSessionControlScope(ctx, identity); scopeErr != nil {
		return LiveDetailCorrection{}, scopeErr
	}
	if identity.LiveStateRevision != params.ExpectedRevision {
		current, currentErr := liveSessionState(ctx, transaction.transaction.SessionRun, identity)
		return LiveDetailCorrection{State: current}, currentErr
	}
	if identity.Lifecycle != session.LifecycleLive {
		return LiveDetailCorrection{}, ErrSessionLifecycleTransition
	}
	run, err := transaction.transaction.SessionRun.Query().Where(
		sessionrun.SessionIDEQ(params.SessionID), sessionrun.ActualEndIsNil(),
	).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return LiveDetailCorrection{}, ErrSessionLifecycleTransition
	}
	if err != nil {
		return LiveDetailCorrection{}, opaqueError("load Session Run for Live Detail Correction", err)
	}
	before, err := currentSessionDetails(ctx, identity)
	if err != nil {
		return LiveDetailCorrection{}, err
	}
	after := applySessionDetailFields(before, params.Details, params.Fields)
	update := transaction.transaction.Session.UpdateOneID(params.SessionID).
		Where(session.EventIDEQ(params.EventID), session.LiveStateRevisionEQ(params.ExpectedRevision), session.LifecycleEQ(session.LifecycleLive)).
		AddLiveStateRevision(1)
	for _, field := range params.Fields {
		switch field {
		case draftFactTitle:
			update.SetCorrectedTitle(after.Title)
		case draftFactSpeaker:
			update.SetCorrectedSpeaker(after.Speaker)
		case draftFactPublicDetails:
			update.SetCorrectedPublicDetails(after.PublicDetails)
		}
	}
	updated, err := update.Save(ctx)
	if ent.IsNotFound(err) {
		current, currentErr := transaction.currentLiveSessionState(ctx, params.EventID, params.SessionID)
		return LiveDetailCorrection{State: current}, currentErr
	}
	if err != nil {
		return LiveDetailCorrection{}, opaqueError("correct Live Session details", err)
	}
	if rebaseErr := transaction.rebaseDraftAfterLiveCorrection(ctx, params, before, after); rebaseErr != nil {
		return LiveDetailCorrection{}, rebaseErr
	}
	evidence, err := json.Marshal(runAmendmentEvidence{Before: before, After: after, ChangedFields: params.Fields})
	if err != nil {
		return LiveDetailCorrection{}, opaqueError("encode Run Amendment", err)
	}
	amendment, err := transaction.transaction.SessionRunAmendment.Create().
		SetSessionRunID(run.ID).SetActorAccountID(params.ActorAccountID).
		SetDetailsJSON(string(evidence)).SetCreatedAt(params.Now).Save(systemContext(ctx))
	if err != nil {
		return LiveDetailCorrection{}, opaqueError("create Run Amendment", err)
	}
	state := LiveSessionState{
		SessionID: updated.ID, SessionRunID: run.ID, Lifecycle: updated.Lifecycle.String(),
		LiveStateRevision: updated.LiveStateRevision, ActualStart: run.ActualStart,
	}
	return LiveDetailCorrection{State: state, AmendmentID: amendment.ID, Details: after}, nil
}

func currentSessionDetails(ctx context.Context, identity *ent.Session) (SessionDetails, error) {
	version, err := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
	if ent.IsNotFound(err) {
		return SessionDetails{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionDetails{}, opaqueError("load Published Session details", err)
	}
	details := correctedSessionDetails(identity, SessionDetails{
		Title: version.Title, Speaker: version.Speaker, PublicDetails: version.PublicDetails,
	})
	return details, nil
}

func correctedSessionDetails(identity *ent.Session, details SessionDetails) SessionDetails {
	if identity.CorrectedTitle != nil {
		details.Title = *identity.CorrectedTitle
	}
	if identity.CorrectedSpeaker != nil {
		details.Speaker = *identity.CorrectedSpeaker
	}
	if identity.CorrectedPublicDetails != nil {
		details.PublicDetails = *identity.CorrectedPublicDetails
	}
	return details
}

func applySessionDetailFields(current, proposed SessionDetails, fields []string) SessionDetails {
	for _, field := range fields {
		switch field {
		case draftFactTitle:
			current.Title = proposed.Title
		case draftFactSpeaker:
			current.Speaker = proposed.Speaker
		case draftFactPublicDetails:
			current.PublicDetails = proposed.PublicDetails
		}
	}
	return current
}

func (transaction *CommandTx) rebaseDraftAfterLiveCorrection(
	ctx context.Context,
	params LiveDetailCorrectionParams,
	before SessionDetails,
	after SessionDetails,
) error {
	internalContext := systemContext(ctx)
	current, err := transaction.transaction.Rundown.Query().Where(rundown.EventIDEQ(params.EventID)).Only(internalContext)
	if err != nil {
		return opaqueError("load Rundown for Live Detail Correction", err)
	}
	nextRevision := current.DraftRevision + 1
	if _, updateErr := transaction.transaction.Rundown.UpdateOneID(current.ID).
		Where(rundown.DraftRevisionEQ(current.DraftRevision)).SetDraftRevision(nextRevision).Save(internalContext); updateErr != nil {
		return opaqueError("advance Draft for Live Detail Correction", updateErr)
	}
	edit, err := transaction.transaction.DraftEdit.Create().
		SetEventID(params.EventID).SetActorAccountID(params.ActorAccountID).
		SetRevision(nextRevision).SetCreatedAt(params.Now).Save(internalContext)
	if err != nil {
		return opaqueError("record Live Detail Correction Draft rebase", err)
	}
	if _, updateErr := transaction.transaction.DraftChange.Update().Where(
		draftchange.EventIDEQ(params.EventID),
		draftchange.TargetTypeEQ(draftTargetSession), draftchange.TargetIDEQ(params.SessionID),
		draftchange.FactKeyIn(params.Fields...), draftchange.StatusEQ(draftchange.StatusEffective),
	).SetStatus(draftchange.StatusConflicted).Save(internalContext); updateErr != nil {
		return opaqueError("conflict Draft facts after Live Detail Correction", updateErr)
	}
	for _, field := range params.Fields {
		var previous, corrected string
		switch field {
		case draftFactTitle:
			previous, corrected = before.Title, after.Title
		case draftFactSpeaker:
			previous, corrected = before.Speaker, after.Speaker
		case draftFactPublicDetails:
			previous, corrected = before.PublicDetails, after.PublicDetails
		}
		change, changeErr := transaction.recordNamedFactChange(
			internalContext,
			EditDraftParams{EventID: params.EventID, ActorAccountID: params.ActorAccountID, Now: params.Now},
			edit.ID, nextRevision, "LiveDetailCorrection", draftTargetSession, params.SessionID, field, previous, corrected,
		)
		if changeErr != nil {
			return changeErr
		}
		if _, changeErr = change.Update().SetStatus(draftchange.StatusConflicted).Save(internalContext); changeErr != nil {
			return opaqueError("mark Live Detail Correction Draft evidence", changeErr)
		}
	}
	draft, err := transaction.transaction.SessionDraft.Query().Where(sessiondraft.SessionIDEQ(params.SessionID)).Only(internalContext)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load Session Draft for Live Detail Correction", err)
	}
	update := draft.Update()
	for _, field := range params.Fields {
		switch field {
		case draftFactTitle:
			update.SetTitle(after.Title)
		case draftFactSpeaker:
			update.SetSpeaker(after.Speaker)
		case draftFactPublicDetails:
			update.SetPublicDetails(after.PublicDetails)
		}
	}
	if _, err := update.Save(internalContext); err != nil {
		return opaqueError("rebase Session Draft after Live Detail Correction", err)
	}
	return nil
}

// LoadSessionHistory returns Run Snapshots and amendments for authorized crew.
// Published facts are immutable from Start; Competition Entry order is populated
// once by the first Entry Slide Take and is immutable thereafter.
func (installationStore *SQLite) LoadSessionHistory(
	ctx context.Context,
	eventID int,
	sessionID int,
) (SessionHistory, error) {
	identity, err := installationStore.client.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if ent.IsNotFound(err) || errors.Is(err, privacy.Deny) {
		return SessionHistory{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionHistory{}, opaqueError("load Session history identity", err)
	}
	runs, err := identity.QueryRuns().Order(ent.Asc(sessionrun.FieldID)).All(ctx)
	if err != nil {
		return SessionHistory{}, opaqueError("load Session Run history", err)
	}
	result := SessionHistory{Runs: make([]SessionRunHistory, 0, len(runs))}
	for _, run := range runs {
		var snapshot SessionRunSnapshot
		if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
			return SessionHistory{}, opaqueError("decode Session Run Snapshot", decodeErr)
		}
		snapshot.LockedEntryOrderIDs = slices.Clone(run.LockedEntryOrderIds)
		found := SessionRunHistory{
			ID: run.ID, ActualStart: run.ActualStart, Snapshot: snapshot,
			Outcome: run.Outcome.String(),
		}
		if !run.ActualEnd.IsZero() {
			actualEnd := run.ActualEnd
			found.ActualEnd = &actualEnd
		}
		amendments, queryErr := installationStore.client.SessionRunAmendment.Query().Where(
			sessionrunamendment.SessionRunIDEQ(run.ID),
		).Order(ent.Asc(sessionrunamendment.FieldID)).All(systemContext(ctx))
		if queryErr != nil {
			return SessionHistory{}, opaqueError("load Run Amendments", queryErr)
		}
		for _, amendment := range amendments {
			var evidence runAmendmentEvidence
			if decodeErr := json.Unmarshal([]byte(amendment.DetailsJSON), &evidence); decodeErr != nil {
				return SessionHistory{}, opaqueError("decode Run Amendment", decodeErr)
			}
			found.Amendments = append(found.Amendments, RunAmendment{
				ID: amendment.ID, Details: evidence.After, ChangedFields: evidence.ChangedFields, CreatedAt: amendment.CreatedAt,
			})
		}
		result.Runs = append(result.Runs, found)
	}
	cancellations, err := installationStore.client.SessionCancellation.Query().
		Where(sessioncancellation.SessionIDEQ(sessionID)).
		Order(ent.Asc(sessioncancellation.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return SessionHistory{}, opaqueError("load Session cancellation history", err)
	}
	for _, cancellation := range cancellations {
		result.Cancellations = append(result.Cancellations, SessionCancellationHistory{
			ID: cancellation.ID, SessionRunID: cancellation.SessionRunID,
			PublicMessage: cancellation.PublicMessage, CrewNotes: cancellation.CrewNotes,
			ForecastStart: cancellation.ForecastStart, CanceledAt: cancellation.CreatedAt,
		})
	}
	return result, nil
}

func (transaction *CommandTx) requireActiveEvent(ctx context.Context, eventID int) error {
	ctx = systemContext(ctx)
	active, err := transaction.transaction.Installation.Query().
		Where(installation.ActiveEventIDEQ(eventID)).Exist(ctx)
	if err != nil {
		return opaqueError("load Active Event for Session command", err)
	}
	if !active {
		return ErrEventNotActive
	}
	return nil
}

func requireSessionControlScope(ctx context.Context, identity *ent.Session) error {
	laneIDs, _, err := sessionPlacement(ctx, identity)
	if err != nil {
		return err
	}
	return requireSessionLaneScope(ctx, identity.EventID, laneIDs)
}

func requireSessionLaneScope(ctx context.Context, eventID int, laneIDs []int) error {
	identity, ok := viewer.FromContext(ctx)
	if !ok {
		return ErrSessionScopeRequired
	}
	if identity.CanProduceEvent(eventID) {
		return nil
	}
	if len(laneIDs) == 0 {
		return ErrSessionScopeRequired
	}
	for _, laneID := range laneIDs {
		if !identity.CanOperateLane(eventID, laneID) {
			return ErrSessionScopeRequired
		}
	}
	return nil
}

func (transaction *CommandTx) currentLiveSessionState(
	ctx context.Context,
	eventID int,
	sessionID int,
) (LiveSessionState, error) {
	identity, err := transaction.transaction.Session.Query().Where(
		session.IDEQ(sessionID), session.EventIDEQ(eventID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return LiveSessionState{}, ErrSessionNotFound
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("reload Session after revision conflict", err)
	}
	return liveSessionState(ctx, transaction.transaction.SessionRun, identity)
}

func sessionRunSnapshot(ctx context.Context, identity *ent.Session) (SessionRunSnapshot, error) {
	version, err := identity.QueryPublishedVersions().Order(ent.Desc("published_revision")).First(ctx)
	if ent.IsNotFound(err) {
		return SessionRunSnapshot{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Published Session for Start", err)
	}
	details := correctedSessionDetails(identity, SessionDetails{
		Title: version.Title, Speaker: version.Speaker, PublicDetails: version.PublicDetails,
	})
	lanes, err := version.QueryLanes().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Lanes", err)
	}
	locations, err := version.QueryLocations().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Locations", err)
	}
	tracks, err := version.QueryTracks().IDs(ctx)
	if err != nil {
		return SessionRunSnapshot{}, opaqueError("load Session Run Snapshot Tracks", err)
	}
	if len(identity.ForecastLaneIds) > 0 {
		lanes = slices.Clone(identity.ForecastLaneIds)
	}
	if len(identity.ForecastLocationIds) > 0 {
		locations = slices.Clone(identity.ForecastLocationIds)
	}
	snapshot := SessionRunSnapshot{
		PublishedRevision: version.PublishedRevision,
		Title:             details.Title, Speaker: details.Speaker, Type: version.Type.String(), PublicDetails: details.PublicDetails,
		PlannedStart: version.PlannedStart, PlannedEnd: version.PlannedEnd,
		TimingPolicy: version.TimingPolicy.String(), MinimumDurationSeconds: version.MinimumDurationSeconds,
		StartBoundary: version.StartBoundary.String(), EndBoundary: version.EndBoundary.String(),
		LaneIDs: lanes, LocationIDs: locations, TrackIDs: tracks,
	}
	return snapshot, nil
}

func liveSessionState(
	ctx context.Context,
	runs *ent.SessionRunClient,
	identity *ent.Session,
) (LiveSessionState, error) {
	state, err := loadLiveSessionState(ctx, runs, identity)
	if err != nil {
		return state, err
	}
	return state, ErrLiveStateRevisionConflict
}

func loadLiveSessionState(
	ctx context.Context,
	runs *ent.SessionRunClient,
	identity *ent.Session,
) (LiveSessionState, error) {
	state := LiveSessionState{
		SessionID: identity.ID, Lifecycle: identity.Lifecycle.String(),
		LiveStateRevision: identity.LiveStateRevision,
	}
	run, err := runs.Query().Where(sessionrun.SessionIDEQ(identity.ID)).
		Order(ent.Desc(sessionrun.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return state, nil
	}
	if err != nil {
		return LiveSessionState{}, opaqueError("load current Session Run", err)
	}
	state.SessionRunID = run.ID
	state.ActualStart = run.ActualStart
	if !run.ActualEnd.IsZero() {
		actualEnd := run.ActualEnd
		state.ActualEnd = &actualEnd
	}
	return state, nil
}

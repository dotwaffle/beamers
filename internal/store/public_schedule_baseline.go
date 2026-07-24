package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/publicschedulebaseline"
	"github.com/dotwaffle/beamers/ent/publicschedulebaselineentry"
	"github.com/dotwaffle/beamers/ent/rundown"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

var (
	// ErrPublicScheduleBaselineExists means the Event already has its immutable baseline.
	ErrPublicScheduleBaselineExists = errors.New("public schedule baseline already exists")
	// ErrPublicScheduleBaselineRevision means capture no longer matches the Published Revision.
	ErrPublicScheduleBaselineRevision = errors.New("public schedule baseline published revision conflict")
	// ErrPublicScheduleBaselineInvalid means a Public Session lacks a valid Forecast Start.
	ErrPublicScheduleBaselineInvalid = errors.New("public schedule baseline contains invalid session time")
)

// PublicScheduleBaselineSession is one current Public Session eligible for capture.
type PublicScheduleBaselineSession struct {
	ID            int
	Title         string
	ForecastStart time.Time
}

// PublicScheduleBaselineState is the complete revision-bound input to preview and capture.
type PublicScheduleBaselineState struct {
	EventID           int
	EventName         string
	Active            bool
	PublishedRevision int
	Captured          bool
	CapturedAt        time.Time
	Sessions          []PublicScheduleBaselineSession
}

// PublicScheduleBaselineCaptureParams binds capture to one Published Revision.
type PublicScheduleBaselineCaptureParams struct {
	EventID                   int
	ExpectedPublishedRevision int
	Now                       time.Time
}

// PublicScheduleBaselineCaptureResult is the minimal durable capture outcome.
type PublicScheduleBaselineCaptureResult struct {
	EventID           int
	PublishedRevision int
	SessionCount      int
	CapturedAt        time.Time
}

// LoadPublicScheduleBaselineState returns current capture inputs for one Event.
func (installationStore *SQLite) LoadPublicScheduleBaselineState(
	ctx context.Context,
	eventID int,
) (PublicScheduleBaselineState, error) {
	return loadPublicScheduleBaselineState(ctx, installationStore.client, eventID)
}

// LoadPublicScheduleBaselineState returns transaction-consistent capture inputs.
func (transaction *CommandTx) LoadPublicScheduleBaselineState(
	ctx context.Context,
	eventID int,
) (PublicScheduleBaselineState, error) {
	return loadPublicScheduleBaselineState(ctx, transaction.transaction.Client(), eventID)
}

func loadPublicScheduleBaselineState(
	ctx context.Context,
	client *ent.Client,
	eventID int,
) (PublicScheduleBaselineState, error) {
	internalContext := systemContext(ctx)
	foundEvent, err := client.Event.Get(internalContext, eventID)
	if ent.IsNotFound(err) {
		return PublicScheduleBaselineState{}, ErrEventNotFound
	}
	if err != nil {
		return PublicScheduleBaselineState{}, opaqueError("load Public Schedule Baseline Event", err)
	}
	foundRundown, err := client.Rundown.Query().
		Where(rundown.EventIDEQ(eventID)).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return PublicScheduleBaselineState{}, ErrEventNotFound
	}
	if err != nil {
		return PublicScheduleBaselineState{}, opaqueError("load Public Schedule Baseline revision", err)
	}
	state := PublicScheduleBaselineState{
		EventID: eventID, EventName: foundEvent.Name,
		PublishedRevision: foundRundown.PublishedRevision,
	}
	routing, err := client.Installation.Query().Only(internalContext)
	if err != nil && !ent.IsNotFound(err) {
		return PublicScheduleBaselineState{}, opaqueError("load Public Schedule Baseline routing", err)
	}
	if err == nil && routing.ActiveEventID != nil {
		state.Active = *routing.ActiveEventID == eventID
	}
	baseline, err := client.PublicScheduleBaseline.Query().
		Where(publicschedulebaseline.EventIDEQ(eventID)).
		Only(internalContext)
	if err != nil && !ent.IsNotFound(err) {
		return PublicScheduleBaselineState{}, opaqueError("load existing Public Schedule Baseline", err)
	}
	if err == nil {
		state.Captured = true
		state.CapturedAt = baseline.CapturedAt
	}
	identities, err := client.Session.Query().
		Where(session.EventIDEQ(eventID)).
		All(internalContext)
	if err != nil {
		return PublicScheduleBaselineState{}, opaqueError("load Public Schedule Baseline Sessions", err)
	}
	for _, identity := range identities {
		version, versionErr := identity.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(internalContext)
		if ent.IsNotFound(versionErr) {
			continue
		}
		if versionErr != nil {
			return PublicScheduleBaselineState{}, opaqueError(
				"load Public Schedule Baseline Session version",
				versionErr,
			)
		}
		if version.AudienceVisibility != sessionpublishedversion.AudienceVisibilityPublic {
			continue
		}
		forecastStart := identity.ForecastStart
		if forecastStart.IsZero() {
			forecastStart = version.PlannedStart
		}
		state.Sessions = append(state.Sessions, PublicScheduleBaselineSession{
			ID: identity.ID, Title: version.Title, ForecastStart: forecastStart,
		})
	}
	sort.Slice(state.Sessions, func(first, second int) bool {
		if state.Sessions[first].ForecastStart.Equal(state.Sessions[second].ForecastStart) {
			return state.Sessions[first].ID < state.Sessions[second].ID
		}
		return state.Sessions[first].ForecastStart.Before(state.Sessions[second].ForecastStart)
	})
	return state, nil
}

// CapturePublicScheduleBaseline atomically records every current Public Session or none.
func (transaction *CommandTx) CapturePublicScheduleBaseline(
	ctx context.Context,
	params PublicScheduleBaselineCaptureParams,
) (PublicScheduleBaselineCaptureResult, error) {
	state, err := transaction.LoadPublicScheduleBaselineState(ctx, params.EventID)
	if err != nil {
		return PublicScheduleBaselineCaptureResult{}, err
	}
	if state.Captured {
		return PublicScheduleBaselineCaptureResult{}, ErrPublicScheduleBaselineExists
	}
	if state.PublishedRevision != params.ExpectedPublishedRevision {
		return PublicScheduleBaselineCaptureResult{}, ErrPublicScheduleBaselineRevision
	}
	for _, candidate := range state.Sessions {
		if candidate.ForecastStart.IsZero() {
			return PublicScheduleBaselineCaptureResult{}, ErrPublicScheduleBaselineInvalid
		}
	}
	internalContext := systemContext(ctx)
	baseline, err := transaction.transaction.PublicScheduleBaseline.Create().
		SetEventID(params.EventID).
		SetSourcePublishedRevision(state.PublishedRevision).
		SetCapturedAt(params.Now).
		Save(internalContext)
	if ent.IsConstraintError(err) {
		return PublicScheduleBaselineCaptureResult{}, ErrPublicScheduleBaselineExists
	}
	if err != nil {
		return PublicScheduleBaselineCaptureResult{}, opaqueError("capture Public Schedule Baseline", err)
	}
	if len(state.Sessions) > 0 {
		entries := make([]*ent.PublicScheduleBaselineEntryCreate, 0, len(state.Sessions))
		for _, candidate := range state.Sessions {
			entries = append(entries, transaction.transaction.PublicScheduleBaselineEntry.Create().
				SetBaselineID(baseline.ID).
				SetSessionID(candidate.ID).
				SetForecastStart(candidate.ForecastStart).
				SetSourcePublishedRevision(state.PublishedRevision).
				SetRecordedAt(params.Now))
		}
		if _, err := transaction.transaction.PublicScheduleBaselineEntry.CreateBulk(entries...).
			Save(internalContext); err != nil {
			return PublicScheduleBaselineCaptureResult{}, opaqueError(
				"capture Public Schedule Baseline entries",
				err,
			)
		}
	}
	return PublicScheduleBaselineCaptureResult{
		EventID: params.EventID, PublishedRevision: state.PublishedRevision,
		SessionCount: len(state.Sessions), CapturedAt: params.Now,
	}, nil
}

func (transaction *CommandTx) enrollPublicScheduleBaselineEntries(
	ctx context.Context,
	eventID int,
	publishedRevision int,
	now time.Time,
) error {
	internalContext := systemContext(ctx)
	baseline, err := transaction.transaction.PublicScheduleBaseline.Query().
		Where(publicschedulebaseline.EventIDEQ(eventID)).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load Public Schedule Baseline for Publish", err)
	}
	storedEntries, err := transaction.transaction.PublicScheduleBaselineEntry.Query().
		Where(publicschedulebaselineentry.BaselineIDEQ(baseline.ID)).
		All(internalContext)
	if err != nil {
		return opaqueError("load Public Schedule Baseline entries for Publish", err)
	}
	enrolled := make(map[int]struct{}, len(storedEntries))
	for _, entry := range storedEntries {
		enrolled[entry.SessionID] = struct{}{}
	}
	identities, err := transaction.transaction.Session.Query().
		Where(session.EventIDEQ(eventID)).
		All(internalContext)
	if err != nil {
		return opaqueError("load Sessions for Public Schedule Baseline enrollment", err)
	}
	entries := make([]*ent.PublicScheduleBaselineEntryCreate, 0)
	for _, identity := range identities {
		if _, exists := enrolled[identity.ID]; exists {
			continue
		}
		version, versionErr := identity.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(internalContext)
		if ent.IsNotFound(versionErr) {
			continue
		}
		if versionErr != nil {
			return opaqueError("load Session for Public Schedule Baseline enrollment", versionErr)
		}
		if version.AudienceVisibility != sessionpublishedversion.AudienceVisibilityPublic {
			continue
		}
		forecastStart := identity.ForecastStart
		if forecastStart.IsZero() {
			forecastStart = version.PlannedStart
		}
		if forecastStart.IsZero() {
			return ErrPublicScheduleBaselineInvalid
		}
		entries = append(entries, transaction.transaction.PublicScheduleBaselineEntry.Create().
			SetBaselineID(baseline.ID).
			SetSessionID(identity.ID).
			SetForecastStart(forecastStart).
			SetSourcePublishedRevision(publishedRevision).
			SetRecordedAt(now))
	}
	if len(entries) == 0 {
		return nil
	}
	if _, err := transaction.transaction.PublicScheduleBaselineEntry.CreateBulk(entries...).
		Save(internalContext); err != nil {
		return opaqueError("enroll Published Sessions in Public Schedule Baseline", err)
	}
	return nil
}

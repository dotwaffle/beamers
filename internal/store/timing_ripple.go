package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/internal/timingripple"
)

type timingState struct {
	Sessions   []timingripple.Session
	Revisions  map[int]int
	ActualEnds map[int]time.Time
}

func loadTimingState(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	anchorID int,
) (timingState, error) {
	published, err := loadCrewRundown(ctx, client, eventID)
	if err != nil {
		return timingState{}, err
	}
	identities, err := client.Session.Query().Where(session.EventIDEQ(eventID)).All(ctx)
	if err != nil {
		return timingState{}, opaqueError("load timing Session identities", err)
	}
	byID := make(map[int]*ent.Session, len(identities))
	for _, identity := range identities {
		byID[identity.ID] = identity
	}
	result := timingState{
		Revisions:  make(map[int]int, len(published.Sessions)),
		ActualEnds: make(map[int]time.Time),
	}
	for _, item := range published.Sessions {
		identity := byID[item.ID]
		if identity == nil || identity.Lifecycle == session.LifecycleCanceled {
			continue
		}
		timing := timingripple.Session{
			ID: item.ID, PlannedStart: item.PlannedStart, PlannedEnd: item.PlannedEnd,
			ForecastStart: item.PlannedStart, ForecastEnd: item.PlannedEnd,
			MinimumDuration: time.Duration(item.MinimumDurationSeconds) * time.Second,
			StartBoundary:   timingripple.Boundary(item.StartBoundary),
			EndBoundary:     timingripple.Boundary(item.EndBoundary),
			LaneIDs:         slices.Clone(item.LaneIDs),
			LocationIDs:     slices.Clone(item.LocationIDs),
		}
		if !identity.ForecastStart.IsZero() {
			timing.ForecastStart = identity.ForecastStart
		}
		if !identity.ForecastEnd.IsZero() {
			timing.ForecastEnd = identity.ForecastEnd
		}
		if identity.Lifecycle == session.LifecycleLive ||
			identity.Lifecycle == session.LifecycleEnded {
			run, queryErr := client.SessionRun.Query().Where(
				sessionrun.SessionIDEQ(item.ID),
			).Order(ent.Desc(sessionrun.FieldID)).First(ctx)
			if queryErr != nil {
				return timingState{}, opaqueError("load timing Session Run", queryErr)
			}
			var snapshot SessionRunSnapshot
			if decodeErr := json.Unmarshal([]byte(run.SnapshotJSON), &snapshot); decodeErr != nil {
				return timingState{}, opaqueError("decode timing Session Run Snapshot", decodeErr)
			}
			timing.PlannedStart = snapshot.PlannedStart
			timing.PlannedEnd = snapshot.PlannedEnd
			timing.MinimumDuration = time.Duration(snapshot.MinimumDurationSeconds) * time.Second
			timing.StartBoundary = timingripple.Boundary(snapshot.StartBoundary)
			timing.EndBoundary = timingripple.Boundary(snapshot.EndBoundary)
			timing.LaneIDs = slices.Clone(snapshot.LaneIDs)
			timing.LocationIDs = slices.Clone(snapshot.LocationIDs)
			if identity.ForecastStart.IsZero() {
				timing.ForecastStart = run.ActualStart
			}
			if identity.ForecastEnd.IsZero() {
				timing.ForecastEnd = initialForecastEnd(snapshot, run.ActualStart)
			}
			if !run.ActualEnd.IsZero() {
				result.ActualEnds[item.ID] = run.ActualEnd
				timing.OccupancyEnd = run.ActualEnd
			}
			if identity.Lifecycle == session.LifecycleLive && item.ID != anchorID ||
				identity.Lifecycle == session.LifecycleEnded && item.ID != anchorID {
				timing.StartBoundary = timingripple.Hard
			}
			if identity.Lifecycle == session.LifecycleEnded && item.ID != anchorID {
				timing.EndBoundary = timingripple.Hard
			}
		}
		result.Sessions = append(result.Sessions, timing)
		result.Revisions[item.ID] = identity.LiveStateRevision
	}
	if _, ok := result.Revisions[anchorID]; !ok {
		return timingState{}, errors.New("timing anchor is not in current Rundown state")
	}
	return result, nil
}

func (state timingState) affectedLaneIDs(changes []timingripple.Change) []int {
	lanesBySession := make(map[int][]int, len(state.Sessions))
	for _, item := range state.Sessions {
		lanesBySession[item.ID] = item.LaneIDs
	}
	selected := make(map[int]struct{})
	for _, change := range changes {
		for _, laneID := range lanesBySession[change.SessionID] {
			selected[laneID] = struct{}{}
		}
	}
	result := make([]int, 0, len(selected))
	for laneID := range selected {
		result = append(result, laneID)
	}
	slices.Sort(result)
	return result
}

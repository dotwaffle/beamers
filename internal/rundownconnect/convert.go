package rundownconnect

import (
	"errors"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/internal/rundown"
)

func editDraftInput(message *rundownv1.EditDraftRequest) (rundown.EditDraftInput, error) {
	eventID, err := positiveInt64("event_id", message.GetEventId())
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	expectedRevision, err := nonnegativeInt64("expected_draft_revision", message.GetExpectedDraftRevision())
	if err != nil {
		return rundown.EditDraftInput{}, err
	}
	input := rundown.EditDraftInput{
		EventID: eventID, CommandID: message.GetCommandId(), ExpectedDraftRevision: expectedRevision,
	}
	for _, item := range message.GetLocations() {
		input.Locations = append(input.Locations, rundown.LocationDraftInput{Ref: item.GetRef(), Name: item.GetName()})
	}
	for _, item := range message.GetLanes() {
		location, targetErr := targetRef("lanes.location", item.GetLocation())
		if targetErr != nil {
			return rundown.EditDraftInput{}, targetErr
		}
		input.Lanes = append(input.Lanes, rundown.LaneDraftInput{
			Ref: item.GetRef(), Name: item.GetName(), Location: location,
		})
	}
	for _, item := range message.GetTracks() {
		input.Tracks = append(input.Tracks, rundown.TrackDraftInput{Ref: item.GetRef(), Name: item.GetName()})
	}
	for _, item := range message.GetSessions() {
		converted, sessionErr := sessionDraft(item)
		if sessionErr != nil {
			return rundown.EditDraftInput{}, sessionErr
		}
		input.Sessions = append(input.Sessions, converted)
	}
	return input, nil
}

func sessionDraft(message *rundownv1.SessionDraft) (rundown.SessionDraftInput, error) {
	plannedStart, err := timestamp("sessions.planned_start", message.GetPlannedStart())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	plannedEnd, err := timestamp("sessions.planned_end", message.GetPlannedEnd())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	minimumDuration, err := duration("sessions.minimum_duration", message.GetMinimumDuration())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	lanes, err := targetRefs("sessions.lanes", message.GetLanes())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	locations, err := targetRefs("sessions.locations", message.GetLocations())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	tracks, err := targetRefs("sessions.tracks", message.GetTracks())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	return rundown.SessionDraftInput{
		Ref: message.GetRef(), Title: message.GetTitle(), Type: sessionType(message.GetType()),
		AudienceVisibility: audienceVisibility(message.GetAudienceVisibility()),
		PublicDetails:      message.GetPublicDetails(), CrewNotes: message.GetCrewNotes(),
		PlannedStart: plannedStart, PlannedEnd: plannedEnd,
		TimingPolicy: timingPolicy(message.GetTimingPolicy()), MinimumDuration: minimumDuration,
		StartBoundary: boundary(message.GetStartBoundary()), EndBoundary: boundary(message.GetEndBoundary()),
		Lanes: lanes, Locations: locations, Tracks: tracks,
	}, nil
}

func targetRefs(field string, messages []*rundownv1.TargetRef) ([]rundown.TargetRef, error) {
	result := make([]rundown.TargetRef, 0, len(messages))
	for _, message := range messages {
		converted, err := targetRef(field, message)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func targetRef(field string, message *rundownv1.TargetRef) (rundown.TargetRef, error) {
	if message == nil {
		return rundown.TargetRef{}, errors.New(field + " is required")
	}
	switch target := message.GetTarget().(type) {
	case *rundownv1.TargetRef_Id:
		id, err := positiveInt64(field, target.Id)
		return rundown.TargetRef{ID: id}, err
	case *rundownv1.TargetRef_Ref:
		return rundown.TargetRef{Ref: target.Ref}, nil
	default:
		return rundown.TargetRef{}, errors.New(field + " target is required")
	}
}

func sessionType(value rundownv1.SessionType) rundown.SessionType {
	return map[rundownv1.SessionType]rundown.SessionType{
		rundownv1.SessionType_SESSION_TYPE_PRESENTATION: rundown.SessionPresentation,
		rundownv1.SessionType_SESSION_TYPE_COMPETITION:  rundown.SessionCompetition,
		rundownv1.SessionType_SESSION_TYPE_BREAK:        rundown.SessionBreak,
		rundownv1.SessionType_SESSION_TYPE_ACTIVITY:     rundown.SessionActivity,
		rundownv1.SessionType_SESSION_TYPE_CEREMONY:     rundown.SessionCeremony,
		rundownv1.SessionType_SESSION_TYPE_PERFORMANCE:  rundown.SessionPerformance,
		rundownv1.SessionType_SESSION_TYPE_HOLD:         rundown.SessionHold,
	}[value]
}

func audienceVisibility(value rundownv1.AudienceVisibility) rundown.AudienceVisibility {
	return map[rundownv1.AudienceVisibility]rundown.AudienceVisibility{
		rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC:    rundown.AudiencePublic,
		rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY: rundown.AudienceCrewOnly,
	}[value]
}

func timingPolicy(value rundownv1.TimingPolicy) rundown.TimingPolicy {
	return map[rundownv1.TimingPolicy]rundown.TimingPolicy{
		rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END:      rundown.TimingFixedEnd,
		rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION: rundown.TimingFixedDuration,
		rundownv1.TimingPolicy_TIMING_POLICY_MANUAL_END:     rundown.TimingManualEnd,
	}[value]
}

func boundary(value rundownv1.Boundary) rundown.Boundary {
	return map[rundownv1.Boundary]rundown.Boundary{
		rundownv1.Boundary_BOUNDARY_HARD: rundown.BoundaryHard,
		rundownv1.Boundary_BOUNDARY_SOFT: rundown.BoundarySoft,
	}[value]
}

func crewRundown(projection rundown.CrewRundown) *rundownv1.GetCrewRundownResponse {
	response := &rundownv1.GetCrewRundownResponse{
		DraftRevision: int64(projection.DraftRevision), PublishedRevision: int64(projection.PublishedRevision),
	}
	for _, item := range projection.Locations {
		response.Locations = append(response.Locations, &rundownv1.CrewLocation{Id: int64(item.ID), Name: item.Name})
	}
	for _, item := range projection.Lanes {
		response.Lanes = append(response.Lanes, &rundownv1.CrewLane{
			Id: int64(item.ID), Name: item.Name, LocationId: int64(item.LocationID),
		})
	}
	for _, item := range projection.Tracks {
		response.Tracks = append(response.Tracks, &rundownv1.CrewTrack{Id: int64(item.ID), Name: item.Name})
	}
	for _, item := range projection.Sessions {
		response.Sessions = append(response.Sessions, &rundownv1.CrewSession{
			Id: int64(item.ID), Title: item.Title, Type: protoSessionType(item.Type),
			AudienceVisibility: protoAudienceVisibility(item.AudienceVisibility),
			PublicDetails:      item.PublicDetails, CrewNotes: item.CrewNotes,
			PlannedStart: timestamppb.New(item.PlannedStart), PlannedEnd: timestamppb.New(item.PlannedEnd),
			TimingPolicy: protoTimingPolicy(item.TimingPolicy), MinimumDuration: durationpb.New(item.MinimumDuration),
			StartBoundary: protoBoundary(item.StartBoundary), EndBoundary: protoBoundary(item.EndBoundary),
			LaneIds: ints(item.LaneIDs), LocationIds: ints(item.LocationIDs), TrackIds: ints(item.TrackIDs),
		})
	}
	return response
}

func protoSessionType(value rundown.SessionType) rundownv1.SessionType {
	return map[rundown.SessionType]rundownv1.SessionType{
		rundown.SessionPresentation: rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
		rundown.SessionCompetition:  rundownv1.SessionType_SESSION_TYPE_COMPETITION,
		rundown.SessionBreak:        rundownv1.SessionType_SESSION_TYPE_BREAK,
		rundown.SessionActivity:     rundownv1.SessionType_SESSION_TYPE_ACTIVITY,
		rundown.SessionCeremony:     rundownv1.SessionType_SESSION_TYPE_CEREMONY,
		rundown.SessionPerformance:  rundownv1.SessionType_SESSION_TYPE_PERFORMANCE,
		rundown.SessionHold:         rundownv1.SessionType_SESSION_TYPE_HOLD,
	}[value]
}

func protoAudienceVisibility(value rundown.AudienceVisibility) rundownv1.AudienceVisibility {
	return map[rundown.AudienceVisibility]rundownv1.AudienceVisibility{
		rundown.AudiencePublic:   rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
		rundown.AudienceCrewOnly: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY,
	}[value]
}

func protoTimingPolicy(value rundown.TimingPolicy) rundownv1.TimingPolicy {
	return map[rundown.TimingPolicy]rundownv1.TimingPolicy{
		rundown.TimingFixedEnd:      rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
		rundown.TimingFixedDuration: rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
		rundown.TimingManualEnd:     rundownv1.TimingPolicy_TIMING_POLICY_MANUAL_END,
	}[value]
}

func protoBoundary(value rundown.Boundary) rundownv1.Boundary {
	return map[rundown.Boundary]rundownv1.Boundary{
		rundown.BoundaryHard: rundownv1.Boundary_BOUNDARY_HARD,
		rundown.BoundarySoft: rundownv1.Boundary_BOUNDARY_SOFT,
	}[value]
}

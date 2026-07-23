package rundownconnect

import (
	"errors"
	"slices"
	"time"

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
		input.Locations = append(input.Locations, rundown.LocationDraftInput{
			ID: int(item.GetId()), Ref: item.GetRef(), Name: item.GetName(), UpdateFields: item.GetUpdateMask().GetPaths(),
		})
	}
	for _, item := range message.GetLanes() {
		var location rundown.TargetRef
		if item.GetUpdateMask() == nil || containsPath(item.GetUpdateMask().GetPaths(), "location") {
			var targetErr error
			location, targetErr = targetRef("lanes.location", item.GetLocation())
			if targetErr != nil {
				return rundown.EditDraftInput{}, targetErr
			}
		}
		input.Lanes = append(input.Lanes, rundown.LaneDraftInput{
			ID: int(item.GetId()), Ref: item.GetRef(), Name: item.GetName(), Location: location,
			UpdateFields: item.GetUpdateMask().GetPaths(),
		})
	}
	for _, item := range message.GetTracks() {
		input.Tracks = append(input.Tracks, rundown.TrackDraftInput{
			ID: int(item.GetId()), Ref: item.GetRef(), Name: item.GetName(), UpdateFields: item.GetUpdateMask().GetPaths(),
		})
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

func containsPath(paths []string, wanted string) bool {
	return slices.Contains(paths, wanted)
}

func sessionDraft(message *rundownv1.SessionDraft) (rundown.SessionDraftInput, error) {
	fields := message.GetUpdateMask().GetPaths()
	if len(fields) > 0 {
		return sessionDraftUpdate(message, fields)
	}
	plannedStart, err := timestamp("sessions.planned_start", message.GetPlannedStart())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	plannedEnd, err := timestamp("sessions.planned_end", message.GetPlannedEnd())
	if err != nil {
		return rundown.SessionDraftInput{}, err
	}
	minimumDuration := plannedEnd.Sub(plannedStart)
	if message.GetMinimumDuration() != nil {
		minimumDuration, err = duration("sessions.minimum_duration", message.GetMinimumDuration())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
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
	var submissionDeadline time.Time
	if message.GetSubmissionDeadline() != nil {
		submissionDeadline, err = timestamp("sessions.submission_deadline", message.GetSubmissionDeadline())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	return rundown.SessionDraftInput{
		ID: int(message.GetId()), Ref: message.GetRef(), Title: message.GetTitle(), Speaker: message.GetSpeaker(), Type: sessionType(message.GetType()),
		AudienceVisibility: audienceVisibility(message.GetAudienceVisibility()),
		PublicDetails:      message.GetPublicDetails(), CrewNotes: message.GetCrewNotes(),
		PlannedStart: plannedStart, PlannedEnd: plannedEnd,
		TimingPolicy: timingPolicy(message.GetTimingPolicy()), MinimumDuration: minimumDuration,
		StartBoundary: boundary(message.GetStartBoundary()), EndBoundary: boundary(message.GetEndBoundary()),
		SubmissionDeadline: submissionDeadline, EntryDefault: entryDisposition(message.GetEntryDefaultDisposition()),
		Lanes: lanes, Locations: locations, Tracks: tracks,
	}, nil
}

func sessionDraftUpdate(message *rundownv1.SessionDraft, fields []string) (rundown.SessionDraftInput, error) {
	input := rundown.SessionDraftInput{
		ID: int(message.GetId()), Title: message.GetTitle(), Speaker: message.GetSpeaker(), Type: sessionType(message.GetType()),
		AudienceVisibility: audienceVisibility(message.GetAudienceVisibility()),
		PublicDetails:      message.GetPublicDetails(), CrewNotes: message.GetCrewNotes(),
		TimingPolicy: timingPolicy(message.GetTimingPolicy()), MinimumDuration: message.GetMinimumDuration().AsDuration(),
		StartBoundary: boundary(message.GetStartBoundary()), EndBoundary: boundary(message.GetEndBoundary()),
		UpdateFields: append([]string(nil), fields...),
	}
	selected := make(map[string]bool, len(fields))
	for _, field := range fields {
		selected[field] = true
	}
	var err error
	if selected["planned_start"] {
		input.PlannedStart, err = timestamp("sessions.planned_start", message.GetPlannedStart())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	if selected["planned_end"] {
		input.PlannedEnd, err = timestamp("sessions.planned_end", message.GetPlannedEnd())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	if selected["minimum_duration"] {
		input.MinimumDuration, err = duration("sessions.minimum_duration", message.GetMinimumDuration())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	if selected["submission_deadline"] {
		if message.GetSubmissionDeadline() != nil {
			input.SubmissionDeadline, err = timestamp("sessions.submission_deadline", message.GetSubmissionDeadline())
			if err != nil {
				return rundown.SessionDraftInput{}, err
			}
		}
	}
	if selected["entry_default_disposition"] {
		input.EntryDefault = entryDisposition(message.GetEntryDefaultDisposition())
	}
	if selected["lanes"] {
		input.Lanes, err = targetRefs("sessions.lanes", message.GetLanes())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	if selected["locations"] {
		input.Locations, err = targetRefs("sessions.locations", message.GetLocations())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	if selected["tracks"] {
		input.Tracks, err = targetRefs("sessions.tracks", message.GetTracks())
		if err != nil {
			return rundown.SessionDraftInput{}, err
		}
	}
	for field, messages := range map[string][]*rundownv1.TargetRef{
		"add_lanes": message.GetAddLanes(), "remove_lanes": message.GetRemoveLanes(),
		"add_locations": message.GetAddLocations(), "remove_locations": message.GetRemoveLocations(),
		"add_tracks": message.GetAddTracks(), "remove_tracks": message.GetRemoveTracks(),
	} {
		if !selected[field] {
			continue
		}
		converted, convertErr := targetRefs("sessions."+field, messages)
		if convertErr != nil {
			return rundown.SessionDraftInput{}, convertErr
		}
		switch field {
		case "add_lanes":
			input.AddLanes = converted
		case "remove_lanes":
			input.RemoveLanes = converted
		case "add_locations":
			input.AddLocations = converted
		case "remove_locations":
			input.RemoveLocations = converted
		case "add_tracks":
			input.AddTracks = converted
		case "remove_tracks":
			input.RemoveTracks = converted
		}
	}
	return input, nil
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

func entryDisposition(value rundownv1.EntryDisposition) rundown.EntryDisposition {
	return map[rundownv1.EntryDisposition]rundown.EntryDisposition{
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING:  rundown.EntryPending,
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED: rundown.EntryIncluded,
		rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED: rundown.EntryRejected,
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
			Id: int64(item.ID), Title: item.Title, Speaker: item.Speaker, Type: protoSessionType(item.Type),
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

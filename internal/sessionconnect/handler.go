// Package sessionconnect adapts versioned Connect contracts to Session control services.
package sessionconnect

import (
	"context"
	"errors"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
)

// Handler translates Connect requests without owning Session transitions.
type Handler struct {
	sessionv1connect.UnimplementedSessionControlServiceHandler
	service *sessioncontrol.Service
	notify  func()
}

// NewHandler creates a Session control Connect adapter.
func NewHandler(service *sessioncontrol.Service, notifyDisplays func()) (*Handler, error) {
	if service == nil {
		return nil, errors.New("session control service is required")
	}
	if notifyDisplays == nil {
		return nil, errors.New("display notifier is required")
	}
	return &Handler{service: service, notify: notifyDisplays}, nil
}

// ErrorInterceptor translates Session control failures into stable Connect codes.
func ErrorInterceptor() connect.Interceptor { return errorInterceptor{} }

type errorInterceptor struct{}

func (errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, request)
		if err == nil {
			return response, nil
		}
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return response, err
		}
		return response, connectError(err)
	}
}

func (errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
func ValidationInterceptor() connect.Interceptor { return validationInterceptor{} }

type validationInterceptor struct{}

func (validationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if err := validateRequest(request.Any()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return next(ctx, request)
	}
}

func (validationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (validationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// StartSession starts one Published Session explicitly.
func (handler *Handler) StartSession(
	ctx context.Context,
	request *connect.Request[sessionv1.StartSessionRequest],
) (*connect.Response[sessionv1.StartSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.Start(ctx, actor, sessioncontrol.StartInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	handler.notifyDisplays()
	return connect.NewResponse(&sessionv1.StartSessionResponse{State: sessionState(result)}), nil
}

// EndSession ends one Live Session explicitly.
func (handler *Handler) EndSession(
	ctx context.Context,
	request *connect.Request[sessionv1.EndSessionRequest],
) (*connect.Response[sessionv1.EndSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.End(ctx, actor, sessioncontrol.EndInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	handler.notifyDisplays()
	return connect.NewResponse(&sessionv1.EndSessionResponse{State: sessionState(result)}), nil
}

// CancelSession cancels one Scheduled or Live Session.
func (handler *Handler) CancelSession(
	ctx context.Context,
	request *connect.Request[sessionv1.CancelSessionRequest],
) (*connect.Response[sessionv1.CancelSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.Cancel(ctx, actor, sessioncontrol.CancelInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		Confirmed:                 request.Msg.GetConfirmed(),
		PublicCancellationMessage: request.Msg.GetPublicCancellationMessage(),
		CrewNotes:                 request.Msg.GetCrewNotes(),
	})
	if err != nil {
		return nil, err
	}
	handler.notifyDisplays()
	return connect.NewResponse(&sessionv1.CancelSessionResponse{
		State: sessionState(result),
	}), nil
}

// PreviewReinstateSession returns placement effects without mutation.
func (handler *Handler) PreviewReinstateSession(
	ctx context.Context,
	request *connect.Request[sessionv1.PreviewReinstateSessionRequest],
) (*connect.Response[sessionv1.PreviewReinstateSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PreviewReinstate(ctx, actor, sessioncontrol.PreviewReinstateInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		ForecastStart: request.Msg.GetForecastStart().AsTime(),
		LaneIDs:       ints(request.Msg.GetLaneIds()),
		LocationIDs:   ints(request.Msg.GetLocationIds()),
	})
	if err != nil {
		return nil, err
	}
	response := &sessionv1.PreviewReinstateSessionResponse{
		CurrentLaneIds:                   int64s(result.CurrentLaneIDs),
		ProposedLaneIds:                  int64s(result.ProposedLaneIDs),
		CurrentLocationIds:               int64s(result.CurrentLocationIDs),
		ProposedLocationIds:              int64s(result.ProposedLocationIDs),
		RequiresHardBoundaryConfirmation: result.RequiresHardBoundaryConfirmation,
		PreviewFingerprint:               result.Fingerprint,
	}
	for _, effect := range result.Effects {
		response.Effects = append(response.Effects, targetEffect(effect))
	}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, forecastChange(change))
	}
	return connect.NewResponse(response), nil
}

// ReinstateSession confirms one freshly previewed Session placement.
func (handler *Handler) ReinstateSession(
	ctx context.Context,
	request *connect.Request[sessionv1.ReinstateSessionRequest],
) (*connect.Response[sessionv1.ReinstateSessionResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.Reinstate(ctx, actor, sessioncontrol.ReinstateInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		ForecastStart:             request.Msg.GetForecastStart().AsTime(),
		LaneIDs:                   ints(request.Msg.GetLaneIds()),
		LocationIDs:               ints(request.Msg.GetLocationIds()),
		PreviewFingerprint:        request.Msg.GetPreviewFingerprint(),
		Confirmed:                 request.Msg.GetConfirmed(),
		HardBoundaryConfirmed:     request.Msg.GetHardBoundaryConfirmed(),
	})
	if err != nil {
		return nil, err
	}
	response := &sessionv1.ReinstateSessionResponse{
		State:                 sessionState(result.State),
		PreviousForecastStart: timestamppb.New(result.PreviousForecastStart),
	}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, forecastChange(change))
	}
	handler.notifyDisplays()
	return connect.NewResponse(response), nil
}

// PreviewAdjustTarget returns the current downstream impact without mutation.
func (handler *Handler) PreviewAdjustTarget(
	ctx context.Context,
	request *connect.Request[sessionv1.PreviewAdjustTargetRequest],
) (*connect.Response[sessionv1.PreviewAdjustTargetResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	adjustment, err := targetAdjustment(request.Msg.GetPreset(), request.Msg.GetCustom())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PreviewAdjustTarget(ctx, actor, sessioncontrol.PreviewAdjustTargetInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		Adjustment: adjustment,
	})
	if err != nil {
		return nil, err
	}
	response := &sessionv1.PreviewAdjustTargetResponse{
		CurrentTarget:                    timestamppb.New(result.CurrentTarget),
		ProposedTarget:                   timestamppb.New(result.ProposedTarget),
		Adjustment:                       durationpb.New(result.Adjustment),
		RequiresHardBoundaryConfirmation: result.RequiresHardBoundaryConfirmation,
		PreviewFingerprint:               result.Fingerprint,
	}
	for _, effect := range result.Effects {
		response.Effects = append(response.Effects, targetEffect(effect))
	}
	for _, preset := range result.ConfiguredPresets {
		response.ConfiguredPresets = append(response.ConfiguredPresets, durationpb.New(preset))
	}
	return connect.NewResponse(response), nil
}

// AdjustTarget confirms and commits one freshly previewed target.
func (handler *Handler) AdjustTarget(
	ctx context.Context,
	request *connect.Request[sessionv1.AdjustTargetRequest],
) (*connect.Response[sessionv1.AdjustTargetResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	adjustment, err := targetAdjustment(request.Msg.GetPreset(), request.Msg.GetCustom())
	if err != nil {
		return nil, err
	}
	result, err := handler.service.AdjustTarget(ctx, actor, sessioncontrol.AdjustTargetInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		Adjustment:                adjustment, PreviewFingerprint: request.Msg.GetPreviewFingerprint(),
		Confirmed:             request.Msg.GetConfirmed(),
		HardBoundaryConfirmed: request.Msg.GetHardBoundaryConfirmed(),
	})
	if err != nil {
		return nil, err
	}
	handler.notifyDisplays()
	response := &sessionv1.AdjustTargetResponse{
		State: sessionState(result.State), ForecastEnd: timestamppb.New(result.ForecastEnd),
		Adjustment: durationpb.New(result.Adjustment), AdjustedAt: timestamppb.New(result.AdjustedAt),
	}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, forecastChange(change))
	}
	return connect.NewResponse(response), nil
}

// PreviewPullForward returns eligible later Soft-Boundary movement without mutation.
func (handler *Handler) PreviewPullForward(
	ctx context.Context,
	request *connect.Request[sessionv1.PreviewPullForwardRequest],
) (*connect.Response[sessionv1.PreviewPullForwardResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PreviewPullForward(ctx, actor, sessioncontrol.PreviewPullForwardInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
	})
	if err != nil {
		return nil, err
	}
	response := &sessionv1.PreviewPullForwardResponse{
		PreviewFingerprint: result.Fingerprint,
	}
	for _, effect := range result.Effects {
		response.Effects = append(response.Effects, targetEffect(effect))
	}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, forecastChange(change))
	}
	return connect.NewResponse(response), nil
}

// PullForward confirms and commits one freshly previewed early-finish recalculation.
func (handler *Handler) PullForward(
	ctx context.Context,
	request *connect.Request[sessionv1.PullForwardRequest],
) (*connect.Response[sessionv1.PullForwardResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.PullForward(ctx, actor, sessioncontrol.PullForwardInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID:                 request.Msg.GetCommandId(),
		ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		PreviewFingerprint:        request.Msg.GetPreviewFingerprint(),
		Confirmed:                 request.Msg.GetConfirmed(),
	})
	if err != nil {
		return nil, err
	}
	response := &sessionv1.PullForwardResponse{State: sessionState(result.State)}
	for _, change := range result.Changes {
		response.Changes = append(response.Changes, forecastChange(change))
	}
	handler.notifyDisplays()
	return connect.NewResponse(response), nil
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *sessionv1.StartSessionRequest:
		return validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		)
	case *sessionv1.EndSessionRequest:
		return validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		)
	case *sessionv1.CancelSessionRequest:
		return validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		)
	case *sessionv1.PreviewReinstateSessionRequest:
		if err := validatePlacementRequest(
			request.GetEventId(), request.GetSessionId(), request.GetForecastStart(),
			request.GetLaneIds(), request.GetLocationIds(),
		); err != nil {
			return err
		}
		return nil
	case *sessionv1.ReinstateSessionRequest:
		if err := validatePlacementRequest(
			request.GetEventId(), request.GetSessionId(), request.GetForecastStart(),
			request.GetLaneIds(), request.GetLocationIds(),
		); err != nil {
			return err
		}
		if err := validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		); err != nil {
			return err
		}
		return validatePreviewFingerprint(request.GetPreviewFingerprint())
	case *sessionv1.PreviewAdjustTargetRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		if err := positiveID(request.GetSessionId(), "session_id"); err != nil {
			return err
		}
		return validateTargetAdjustment(request.GetPreset(), request.GetCustom())
	case *sessionv1.AdjustTargetRequest:
		if err := validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		); err != nil {
			return err
		}
		if err := validatePreviewFingerprint(request.GetPreviewFingerprint()); err != nil {
			return err
		}
		return validateTargetAdjustment(request.GetPreset(), request.GetCustom())
	case *sessionv1.PreviewPullForwardRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		return positiveID(request.GetSessionId(), "session_id")
	case *sessionv1.PullForwardRequest:
		if err := validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		); err != nil {
			return err
		}
		return validatePreviewFingerprint(request.GetPreviewFingerprint())
	case *sessionv1.CorrectLiveDetailsRequest:
		if err := validateCommandEnvelope(
			request.GetEventId(), request.GetSessionId(), request.GetCommandId(),
			request.ExpectedLiveStateRevision,
		); err != nil {
			return err
		}
		if request.GetUpdateMask() == nil || len(request.GetUpdateMask().GetPaths()) == 0 {
			return errors.New("update_mask must select corrected details")
		}
		return nil
	case *sessionv1.GetSessionHistoryRequest:
		if err := positiveID(request.GetEventId(), "event_id"); err != nil {
			return err
		}
		return positiveID(request.GetSessionId(), "session_id")
	default:
		return errors.New("unsupported Session control request")
	}
}

func validateTargetAdjustment(preset, custom *durationpb.Duration) error {
	if (preset == nil) == (custom == nil) {
		return errors.New("exactly one target adjustment is required")
	}
	selected := preset
	if selected == nil {
		selected = custom
	}
	if err := selected.CheckValid(); err != nil {
		return errors.New("target adjustment must be a valid duration")
	}
	duration := selected.AsDuration()
	if duration == 0 || duration%time.Second != 0 ||
		duration < -24*time.Hour || duration > 24*time.Hour {
		return errors.New("target adjustment must use whole seconds and be non-zero and no more than 24 hours")
	}
	return nil
}

func validateCommandEnvelope(
	eventID int64,
	sessionID int64,
	commandID string,
	expectedRevision *int64,
) error {
	if err := positiveID(eventID, "event_id"); err != nil {
		return err
	}
	if err := positiveID(sessionID, "session_id"); err != nil {
		return err
	}
	if err := command.ValidateID(commandID); err != nil {
		return err
	}
	if expectedRevision == nil {
		return errors.New("expected_live_state_revision is required")
	}
	if *expectedRevision < 0 || *expectedRevision > math.MaxInt {
		return errors.New("expected_live_state_revision must be a supported non-negative integer")
	}
	return nil
}

func validatePreviewFingerprint(fingerprint string) error {
	if fingerprint == "" {
		return errors.New("preview_fingerprint is required")
	}
	return nil
}

func validatePlacementRequest(
	eventID int64,
	sessionID int64,
	forecastStart *timestamppb.Timestamp,
	laneIDs []int64,
	locationIDs []int64,
) error {
	if err := positiveID(eventID, "event_id"); err != nil {
		return err
	}
	if err := positiveID(sessionID, "session_id"); err != nil {
		return err
	}
	if forecastStart == nil {
		return errors.New("forecast_start is required")
	}
	if err := forecastStart.CheckValid(); err != nil {
		return errors.New("forecast_start must be a valid timestamp")
	}
	if len(laneIDs) == 0 || len(locationIDs) == 0 {
		return errors.New("lane_ids and location_ids are required")
	}
	for _, laneID := range laneIDs {
		if err := positiveID(laneID, "lane_ids"); err != nil {
			return err
		}
	}
	for _, locationID := range locationIDs {
		if err := positiveID(locationID, "location_ids"); err != nil {
			return err
		}
	}
	return nil
}

func targetAdjustment(preset, custom *durationpb.Duration) (sessioncontrol.TargetAdjustment, error) {
	if err := validateTargetAdjustment(preset, custom); err != nil {
		return sessioncontrol.TargetAdjustment{}, err
	}
	if preset != nil {
		return sessioncontrol.TargetAdjustment{Duration: preset.AsDuration(), Preset: true}, nil
	}
	return sessioncontrol.TargetAdjustment{Duration: custom.AsDuration()}, nil
}

// CorrectLiveDetails applies one confirmed descriptive correction.
func (handler *Handler) CorrectLiveDetails(
	ctx context.Context,
	request *connect.Request[sessionv1.CorrectLiveDetailsRequest],
) (*connect.Response[sessionv1.CorrectLiveDetailsResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := handler.service.CorrectLiveDetails(ctx, actor, sessioncontrol.CorrectLiveDetailsInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID: request.Msg.GetCommandId(), ExpectedLiveStateRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		Confirmed: request.Msg.GetConfirmed(), Title: request.Msg.GetTitle(), Speaker: request.Msg.GetSpeaker(),
		PublicDetails: request.Msg.GetPublicDetails(), UpdateFields: request.Msg.GetUpdateMask().GetPaths(),
	})
	if err != nil {
		return nil, err
	}
	handler.notifyDisplays()
	return connect.NewResponse(&sessionv1.CorrectLiveDetailsResponse{
		State: sessionState(result.State), AmendmentId: int64(result.AmendmentID), Details: sessionDetails(result.Details),
	}), nil
}

func (handler *Handler) notifyDisplays() {
	handler.notify()
}

// GetSessionHistory returns immutable Run Snapshots and amendments.
func (handler *Handler) GetSessionHistory(
	ctx context.Context,
	request *connect.Request[sessionv1.GetSessionHistoryRequest],
) (*connect.Response[sessionv1.GetSessionHistoryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	history, err := handler.service.History(ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()))
	if err != nil {
		return nil, err
	}
	response := &sessionv1.GetSessionHistoryResponse{}
	for _, run := range history.Runs {
		found := &sessionv1.SessionRunHistory{
			Id: int64(run.ID), ActualStart: timestamppb.New(run.ActualStart), Snapshot: runSnapshot(run.Snapshot),
			Outcome: sessionRunOutcome(run.Outcome),
		}
		if run.ActualEnd != nil {
			found.ActualEnd = timestamppb.New(*run.ActualEnd)
		}
		for _, amendment := range run.Amendments {
			found.Amendments = append(found.Amendments, &sessionv1.RunAmendment{
				Id: int64(amendment.ID), Details: sessionDetails(amendment.Details),
				ChangedFields: amendment.ChangedFields, CreatedAt: timestamppb.New(amendment.CreatedAt),
			})
		}
		response.Runs = append(response.Runs, found)
	}
	for _, cancellation := range history.Cancellations {
		found := &sessionv1.SessionCancellationHistory{
			Id:                        int64(cancellation.ID),
			PublicCancellationMessage: cancellation.PublicCancellationMessage,
			CrewNotes:                 cancellation.CrewNotes,
			ForecastStart:             timestamppb.New(cancellation.ForecastStart),
			CanceledAt:                timestamppb.New(cancellation.CanceledAt),
		}
		if cancellation.SessionRunID != nil {
			found.SessionRunId = new(int64(*cancellation.SessionRunID))
		}
		response.Cancellations = append(response.Cancellations, found)
	}
	return connect.NewResponse(response), nil
}

func sessionRunOutcome(value string) sessionv1.SessionRunOutcome {
	switch value {
	case "Completed":
		return sessionv1.SessionRunOutcome_SESSION_RUN_OUTCOME_COMPLETED
	case "Canceled":
		return sessionv1.SessionRunOutcome_SESSION_RUN_OUTCOME_CANCELED
	default:
		return sessionv1.SessionRunOutcome_SESSION_RUN_OUTCOME_UNSPECIFIED
	}
}

func runSnapshot(snapshot sessioncontrol.RunSnapshot) *sessionv1.RunSnapshot {
	return &sessionv1.RunSnapshot{
		PublishedRevision: int64(snapshot.PublishedRevision),
		Title:             snapshot.Title, Speaker: snapshot.Speaker, Type: sessionType(snapshot.Type),
		PublicDetails: snapshot.PublicDetails,
		PlannedStart:  timestamppb.New(snapshot.PlannedStart), PlannedEnd: timestamppb.New(snapshot.PlannedEnd),
		TimingPolicy:    timingPolicy(snapshot.TimingPolicy),
		MinimumDuration: durationpb.New(time.Duration(snapshot.MinimumDurationSeconds) * time.Second),
		StartBoundary:   boundary(snapshot.StartBoundary), EndBoundary: boundary(snapshot.EndBoundary),
		LaneIds: int64s(snapshot.LaneIDs), LocationIds: int64s(snapshot.LocationIDs),
		TrackIds: int64s(snapshot.TrackIDs), LockedEntryOrderIds: int64s(snapshot.LockedEntryOrderIDs),
	}
}

func sessionType(value string) rundownv1.SessionType {
	return map[string]rundownv1.SessionType{
		"Presentation": rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
		"Competition":  rundownv1.SessionType_SESSION_TYPE_COMPETITION,
		"Break":        rundownv1.SessionType_SESSION_TYPE_BREAK,
		"Activity":     rundownv1.SessionType_SESSION_TYPE_ACTIVITY,
		"Ceremony":     rundownv1.SessionType_SESSION_TYPE_CEREMONY,
		"Performance":  rundownv1.SessionType_SESSION_TYPE_PERFORMANCE,
		"Hold":         rundownv1.SessionType_SESSION_TYPE_HOLD,
	}[value]
}

func timingPolicy(value string) rundownv1.TimingPolicy {
	return map[string]rundownv1.TimingPolicy{
		"FixedEnd":      rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
		"FixedDuration": rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
		"ManualEnd":     rundownv1.TimingPolicy_TIMING_POLICY_MANUAL_END,
	}[value]
}

func boundary(value string) rundownv1.Boundary {
	return map[string]rundownv1.Boundary{
		"Hard": rundownv1.Boundary_BOUNDARY_HARD,
		"Soft": rundownv1.Boundary_BOUNDARY_SOFT,
	}[value]
}

func int64s(values []int) []int64 {
	result := make([]int64, len(values))
	for index, value := range values {
		result[index] = int64(value)
	}
	return result
}

func ints(values []int64) []int {
	result := make([]int, len(values))
	for index, value := range values {
		result[index] = int(value)
	}
	return result
}

func sessionDetails(details sessioncontrol.Details) *sessionv1.SessionDetails {
	return &sessionv1.SessionDetails{
		Title: details.Title, Speaker: details.Speaker, PublicDetails: details.PublicDetails,
	}
}

func positiveID(value int64, field string) error {
	if value <= 0 || value > math.MaxInt {
		return errors.New(field + " must be a positive supported integer")
	}
	return nil
}

func sessionState(found sessioncontrol.State) *sessionv1.SessionState {
	result := &sessionv1.SessionState{
		SessionId: int64(found.SessionID), SessionRunId: int64(found.SessionRunID),
		Lifecycle: lifecycle(found.Lifecycle), LiveStateRevision: int64(found.LiveStateRevision),
	}
	if !found.ActualStart.IsZero() {
		result.ActualStart = timestamppb.New(found.ActualStart)
	}
	if found.ActualEnd != nil {
		result.ActualEnd = timestamppb.New(*found.ActualEnd)
	}
	return result
}

func targetEffect(found sessioncontrol.TargetEffect) *sessionv1.TargetEffect {
	return &sessionv1.TargetEffect{
		SessionId:             int64(found.SessionID),
		CurrentOverlap:        durationpb.New(found.CurrentOverlap),
		ProposedOverlap:       durationpb.New(found.ProposedOverlap),
		CurrentForecastStart:  timestamppb.New(found.CurrentForecastStart),
		CurrentForecastEnd:    timestamppb.New(found.CurrentForecastEnd),
		ProposedForecastStart: timestamppb.New(found.ProposedForecastStart),
		ProposedForecastEnd:   timestamppb.New(found.ProposedForecastEnd),
	}
}

func forecastChange(found sessioncontrol.ForecastChange) *sessionv1.ForecastChange {
	return &sessionv1.ForecastChange{
		SessionId: int64(found.SessionID), ForecastStart: timestamppb.New(found.ForecastStart),
		ForecastEnd: timestamppb.New(found.ForecastEnd),
	}
}

func lifecycle(value string) sessionv1.SessionLifecycle {
	switch value {
	case "Scheduled":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_SCHEDULED
	case "Live":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE
	case "Ended":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED
	case "Canceled":
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_CANCELED
	default:
		return sessionv1.SessionLifecycle_SESSION_LIFECYCLE_UNSPECIFIED
	}
}

func connectError(err error) error {
	switch {
	case errors.Is(err, sessioncontrol.ErrOperatorRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sessioncontrol.ErrProducerRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sessioncontrol.ErrSessionScopeRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sessioncontrol.ErrSessionNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, sessioncontrol.ErrLiveStateRevisionConflict):
		connectErr := connect.NewError(connect.CodeAborted, err)
		var revisionErr *sessioncontrol.RevisionConflictError
		if errors.As(err, &revisionErr) {
			detail, detailErr := connect.NewErrorDetail(sessionState(revisionErr.Current))
			if detailErr != nil {
				return connect.NewError(connect.CodeInternal, errors.New("session control unavailable"))
			}
			connectErr.AddDetail(detail)
		}
		return connectErr
	case errors.Is(err, sessioncontrol.ErrSessionLifecycleTransition):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrCompetitionPreflightBlocked):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrLiveDetailConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrLiveDetailFields):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, sessioncontrol.ErrCancelConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrCancellationText):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, sessioncontrol.ErrPresetNotConfigured):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, sessioncontrol.ErrTargetBeforeNow):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrNoCountdownTarget):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrTargetPreviewStale):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, sessioncontrol.ErrTargetConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrHardBoundaryConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrPullForwardPreviewStale):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, sessioncontrol.ErrPullForwardConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrReinstatePreviewStale):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, sessioncontrol.ErrReinstateConfirmation):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrReinstatePlacement):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, sessioncontrol.ErrEventNotActive):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, sessioncontrol.ErrCommandConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("session control unavailable"))
	}
}

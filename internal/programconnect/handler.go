// Package programconnect adapts versioned Program control contracts.
package programconnect

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/store"
)

// Handler translates Program control requests without owning domain state.
type Handler struct {
	programv1connect.UnimplementedProgramControlServiceHandler
	service       *programcontrol.Service
	displays      *displays.Service
	displayStream *displaystream.Hub
	programStream *displaystream.Hub
}

// NewHandler creates the Program control Connect adapter.
func NewHandler(
	service *programcontrol.Service,
	displayService *displays.Service,
	displayStream *displaystream.Hub,
	programStream *displaystream.Hub,
) (*Handler, error) {
	if service == nil {
		return nil, errors.New("program control service is required")
	}
	if displayService == nil {
		return nil, errors.New("display service is required")
	}
	if displayStream == nil {
		return nil, errors.New("display stream is required")
	}
	if programStream == nil {
		return nil, errors.New("program stream is required")
	}
	return &Handler{
		service: service, displays: displayService,
		displayStream: displayStream, programStream: programStream,
	}, nil
}

// GetProgramChannel returns monitor-visible durable and volatile state.
func (handler *Handler) GetProgramChannel(
	ctx context.Context,
	request *connect.Request[programv1.GetProgramChannelRequest],
) (*connect.Response[programv1.GetProgramChannelResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	state, err := handler.service.ReconcileAndCurrent(
		ctx, actor, int(request.Msg.GetEventId()), int(request.Msg.GetSessionId()),
	)
	if err != nil {
		return nil, err
	}
	channel, err := handler.channel(ctx, state, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&programv1.GetProgramChannelResponse{Channel: channel}), nil
}

// ChangeControl claims, requests, hands over, takes over, or disconnects ownership.
func (handler *Handler) ChangeControl(
	ctx context.Context,
	request *connect.Request[programv1.ChangeControlRequest],
) (*connect.Response[programv1.ChangeControlResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	state, err := handler.service.Control(ctx, actor, programcontrol.ControlInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		Action: controlAction(request.Msg.GetAction()), Confirmed: request.Msg.GetConfirmed(),
		CommandID:        request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedControlStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	handler.programStream.Notify()
	channel, err := handler.channel(ctx, state, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&programv1.ChangeControlResponse{Channel: channel}), nil
}

// SelectPreview changes only process-local Preview.
func (handler *Handler) SelectPreview(
	ctx context.Context,
	request *connect.Request[programv1.SelectPreviewRequest],
) (*connect.Response[programv1.SelectPreviewResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	state, err := handler.service.SelectPreview(ctx, actor, programcontrol.SelectPreviewInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		Item:             programItemFromMessage(request.Msg.GetItem()),
		CommandID:        request.Msg.GetCommandId(),
		ExpectedRevision: int(request.Msg.GetExpectedControlStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	handler.programStream.Notify()
	channel, err := handler.channel(ctx, state, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&programv1.SelectPreviewResponse{Channel: channel}), nil
}

// Take commits Program Output before notifying Displays.
func (handler *Handler) Take(
	ctx context.Context,
	request *connect.Request[programv1.TakeRequest],
) (*connect.Response[programv1.TakeResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	taken, err := handler.service.Take(ctx, actor, programcontrol.TakeInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		CommandID: request.Msg.GetCommandId(), ExpectedRevision: int(request.Msg.GetExpectedLiveStateRevision()),
		ExpectedControlRevision:    int(request.Msg.GetExpectedControlStateRevision()),
		Item:                       programItemFromMessage(request.Msg.GetPreview()),
		ExpectedEntryOrderRevision: int(request.Msg.GetExpectedEntryOrderRevision()),
		EntryOrderFingerprint:      request.Msg.GetEntryOrderFingerprint(),
	})
	if err != nil {
		return nil, err
	}
	if taken.Committed {
		handler.displayStream.Notify()
		handler.programStream.Notify()
	}
	channel, err := handler.channel(ctx, taken.State, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&programv1.TakeResponse{Channel: channel}), nil
}

// DeferEntry advances past one canonical Entry through the current Control Owner.
func (handler *Handler) DeferEntry(
	ctx context.Context,
	request *connect.Request[programv1.DeferEntryRequest],
) (*connect.Response[programv1.DeferEntryResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	deferred, err := handler.service.DeferEntry(ctx, actor, programcontrol.DeferEntryInput{
		EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
		EntryID: int(request.Msg.GetEntryId()), CommandID: request.Msg.GetCommandId(),
		ExpectedEntryRevision:   int(request.Msg.GetExpectedEntryRevision()),
		ExpectedProgramRevision: int(request.Msg.GetExpectedProgramRevision()),
		ExpectedControlRevision: int(request.Msg.GetExpectedControlStateRevision()),
	})
	if err != nil {
		return nil, err
	}
	if deferred.Committed {
		handler.programStream.Notify()
	}
	channel, err := handler.channel(ctx, deferred.State, false)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&programv1.DeferEntryResponse{Channel: channel}), nil
}

// ActOnResult reveals, completes, replays, or skips one locked Result.
func (handler *Handler) ActOnResult(
	ctx context.Context,
	request *connect.Request[programv1.ActOnResultRequest],
) (*connect.Response[programv1.ActOnResultResponse], error) {
	actor, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	acted, err := handler.service.ActOnResult(
		ctx,
		actor,
		programcontrol.ResultActionInput{
			EventID: int(request.Msg.GetEventId()), SessionID: int(request.Msg.GetSessionId()),
			CommandID: request.Msg.GetCommandId(),
			Action:    resultAction(request.Msg.GetAction()),
			Item:      programItemFromMessage(request.Msg.GetItem()),
			ExpectedProgramRevision: int(
				request.Msg.GetExpectedProgramRevision(),
			),
			ExpectedControlRevision: int(
				request.Msg.GetExpectedControlStateRevision(),
			),
		},
	)
	if err != nil {
		return nil, err
	}
	if acted.Committed {
		handler.displayStream.Notify()
		handler.programStream.Notify()
	}
	channel, err := handler.channel(ctx, acted.State, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(
		&programv1.ActOnResultResponse{Channel: channel},
	), nil
}

func (handler *Handler) channel(
	ctx context.Context,
	state programcontrol.State,
	bestEffortDisplayStatus bool,
) (*programv1.ProgramChannel, error) {
	account, err := connectapi.ActorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := handler.displays.List(ctx, account, handler.displayStream.Cursor())
	if err != nil {
		if !bestEffortDisplayStatus {
			return nil, err
		}
		statuses = nil
	}
	result := &programv1.ProgramChannel{
		EventId: int64(state.Channel.EventID), SessionId: int64(state.Channel.SessionID),
		Name: state.Channel.Name, LiveStateRevision: int64(state.Channel.Revision),
		ControlStateRevision: int64(state.ControlRevision),
		Previous:             programItemMessage(state.Channel.Previous),
		Current:              programItemMessage(state.Channel.Current),
		Next:                 programItemMessage(state.Channel.Next),
		Preview:              programItemMessage(state.Preview),
		ProgramOutput:        programItemMessage(state.Channel.Output),
	}
	if !state.Channel.TakenAt.IsZero() {
		result.TakenAt = timestamppb.New(state.Channel.TakenAt)
	}
	if state.Owner != nil {
		result.ControlOwner = ownerMessage(*state.Owner)
	}
	if state.HandoverRequester != nil {
		result.HandoverRequester = ownerMessage(*state.HandoverRequester)
	}
	for _, item := range state.Channel.Items {
		result.Items = append(result.Items, programItemMessage(item))
	}
	for _, status := range statuses {
		if status.ViewKey != displayviews.CompetitionOutput ||
			status.ProgramChannelID != state.Channel.SessionID {
			continue
		}
		delivery := status.DeliveryState
		if delivery != "applied" && delivery != "offline" {
			delivery = "lagging"
		}
		result.ConsumingDisplays = append(result.ConsumingDisplays, &programv1.ConsumingDisplay{
			DisplayId: int64(status.ID), Name: status.Name, DeliveryState: delivery,
		})
	}
	return result, nil
}

func controlAction(value programv1.ControlAction) programcontrol.ControlAction {
	return map[programv1.ControlAction]programcontrol.ControlAction{
		programv1.ControlAction_CONTROL_ACTION_CLAIM:            programcontrol.ControlClaim,
		programv1.ControlAction_CONTROL_ACTION_REQUEST_HANDOVER: programcontrol.ControlRequestHandover,
		programv1.ControlAction_CONTROL_ACTION_HANDOVER:         programcontrol.ControlHandover,
		programv1.ControlAction_CONTROL_ACTION_TAKEOVER:         programcontrol.ControlTakeover,
		programv1.ControlAction_CONTROL_ACTION_DISCONNECT:       programcontrol.ControlDisconnect,
	}[value]
}

func resultAction(value programv1.ResultAction) programcontrol.ResultAction {
	return map[programv1.ResultAction]programcontrol.ResultAction{
		programv1.ResultAction_RESULT_ACTION_REVEAL:          programcontrol.ResultReveal,
		programv1.ResultAction_RESULT_ACTION_REPLAY_REVEAL:   programcontrol.ResultReplayReveal,
		programv1.ResultAction_RESULT_ACTION_SKIP_TO_FINAL:   programcontrol.ResultSkipToFinal,
		programv1.ResultAction_RESULT_ACTION_SKIP_FROM_STAGE: programcontrol.ResultSkipFromStage,
	}[value]
}

func programItemFromMessage(found *programv1.ProgramItem) store.ProgramItem {
	if found == nil {
		return store.ProgramItem{}
	}
	return store.ProgramItem{
		Kind: map[programv1.ProgramItemKind]store.ProgramItemKind{
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY:  store.ProgramItemStandby,
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING: store.ProgramItemUpcoming,
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STARTING: store.ProgramItemStarting,
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENTRY:    store.ProgramItemEntry,
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENDING:   store.ProgramItemEnding,
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_RESULT:   store.ProgramItemResult,
		}[found.GetKind()],
		EntryID: int(found.GetEntryId()),
		Retry:   found.GetRetry(),
		Result:  programResultFromMessage(found.GetResult()),
	}
}

func programItemMessage(found store.ProgramItem) *programv1.ProgramItem {
	if found.Kind == "" {
		return nil
	}
	return &programv1.ProgramItem{
		Kind: map[store.ProgramItemKind]programv1.ProgramItemKind{
			store.ProgramItemStandby:  programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY,
			store.ProgramItemUpcoming: programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING,
			store.ProgramItemStarting: programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STARTING,
			store.ProgramItemEntry:    programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENTRY,
			store.ProgramItemEnding:   programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENDING,
			store.ProgramItemResult:   programv1.ProgramItemKind_PROGRAM_ITEM_KIND_RESULT,
		}[found.Kind],
		EntryId: int64(found.EntryID), Title: found.Title, Retry: found.Retry,
		Result: ProgramResultMessage(found.Result),
	}
}

func ownerMessage(found programcontrol.Owner) *programv1.ControlOwner {
	return &programv1.ControlOwner{
		AccountId: int64(found.AccountID), Name: found.Name, Connected: found.Connected,
	}
}

// ErrorInterceptor translates Program control errors into stable codes.
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

func connectError(err error) error {
	switch {
	case errors.Is(err, programcontrol.ErrOperatorRequired),
		errors.Is(err, programcontrol.ErrControlOwnerRequired):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, programcontrol.ErrControlOwned),
		errors.Is(err, programcontrol.ErrHandoverUnavailable),
		errors.Is(err, programcontrol.ErrTakeoverConfirmation),
		errors.Is(err, programcontrol.ErrResultTransition),
		errors.Is(err, programcontrol.ErrResultRevealRunning):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, programcontrol.ErrProgramRevision),
		errors.Is(err, programcontrol.ErrControlRevision),
		errors.Is(err, programcontrol.ErrEntryRevision),
		errors.Is(err, store.ErrEntryOrderRevision),
		errors.Is(err, store.ErrEntryOrderPreviewStale),
		errors.Is(err, programcontrol.ErrCommandConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, programcontrol.ErrPreviewItem),
		errors.Is(err, programcontrol.ErrProgramItem),
		errors.Is(err, programcontrol.ErrEntryDefer):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("program control request failed"))
	}
}

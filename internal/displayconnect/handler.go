// Package displayconnect adapts versioned Connect contracts to Display services.
package displayconnect

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	displayv1 "github.com/dotwaffle/beamers/gen/beamers/display/v1"
	"github.com/dotwaffle/beamers/gen/beamers/display/v1/displayv1connect"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
)

type snapshotContextKey struct{}

type authorizedSnapshot struct {
	snapshot      displays.Snapshot
	cursor        displaystream.Cursor
	credential    string
	snapshotToken string
}

// Handler translates Display Connect requests without owning projection rules.
type Handler struct {
	displayv1connect.UnimplementedDisplayServiceHandler
	service *displays.Service
	stream  *displaystream.Hub
}

// NewHandler creates a Display Connect adapter.
func NewHandler(service *displays.Service, stream *displaystream.Hub) (*Handler, error) {
	if service == nil {
		return nil, errors.New("display service is required")
	}
	if stream == nil {
		return nil, errors.New("display stream is required")
	}
	return &Handler{service: service, stream: stream}, nil
}

// AuthenticationInterceptor authenticates and scopes each request through its Display credential.
func AuthenticationInterceptor(
	service *displays.Service,
	stream *displaystream.Hub,
	cookieName string,
) (connect.Interceptor, error) {
	if service == nil {
		return nil, errors.New("display service is required")
	}
	if stream == nil {
		return nil, errors.New("display stream is required")
	}
	if cookieName == "" {
		return nil, errors.New("display credential cookie name is required")
	}
	return &authenticationInterceptor{service: service, stream: stream, cookieName: cookieName}, nil
}

type authenticationInterceptor struct {
	service    *displays.Service
	stream     *displaystream.Hub
	cookieName string
}

func (interceptor *authenticationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		cookie, err := (&http.Request{Header: request.Header()}).Cookie(interceptor.cookieName)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
		}
		cursor := interceptor.stream.Cursor()
		snapshot, err := interceptor.service.Current(ctx, cookie.Value)
		if errors.Is(err, displays.ErrDisplayAuthentication) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("display Snapshot unavailable"))
		}
		token := interceptor.stream.SnapshotToken(snapshotState(snapshot, cursor))
		return next(context.WithValue(ctx, snapshotContextKey{}, authorizedSnapshot{
			snapshot:      snapshot,
			cursor:        cursor,
			credential:    cookie.Value,
			snapshotToken: token,
		}), request)
	}
}

// Acknowledge durably records state after the Display has applied it.
func (handler *Handler) Acknowledge(
	ctx context.Context,
	request *connect.Request[displayv1.AcknowledgeRequest],
) (*connect.Response[displayv1.AcknowledgeResponse], error) {
	authorized, ok := ctx.Value(snapshotContextKey{}).(authorizedSnapshot)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
	}
	if err := handler.validateAcknowledgment(request.Msg, authorized); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	applied, err := handler.service.Acknowledge(ctx, authorized.credential, displays.AcknowledgmentInput{
		ProtocolVersion: request.Msg.GetProtocolVersion(),
		StreamID:        request.Msg.GetStreamId(), StreamPosition: request.Msg.GetStreamPosition(),
		ActiveEventID:        request.Msg.GetActiveEventId(),
		ActivationGeneration: request.Msg.GetActivationGeneration(),
		PublishedRevision:    request.Msg.GetPublishedRevision(),
	})
	switch {
	case errors.Is(err, displays.ErrDisplayAuthentication):
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
	case errors.Is(err, displays.ErrInvalidAcknowledgment):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, displays.ErrAcknowledgmentRegression),
		errors.Is(err, displays.ErrAcknowledgmentConflict):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("display acknowledgment unavailable"))
	}
	return connect.NewResponse(&displayv1.AcknowledgeResponse{
		Acknowledgment: acknowledgmentMessage(applied),
	}), nil
}

func (handler *Handler) validateAcknowledgment(
	request *displayv1.AcknowledgeRequest,
	authorized authorizedSnapshot,
) error {
	if request.GetStreamId() != authorized.cursor.StreamID ||
		request.GetStreamPosition() > authorized.cursor.Position {
		return errors.New("invalid Display stream cursor")
	}
	state := displaystream.SnapshotState{
		Cursor: displaystream.Cursor{
			StreamID: request.GetStreamId(), Position: request.GetStreamPosition(),
		},
		DisplayID:            int64(authorized.snapshot.Display.ID),
		ProtocolVersion:      request.GetProtocolVersion(),
		ActiveEventID:        request.GetActiveEventId(),
		ActivationGeneration: request.GetActivationGeneration(),
		PublishedRevision:    request.GetPublishedRevision(),
	}
	if !handler.stream.ValidSnapshotToken(request.GetSnapshotToken(), state) {
		return errors.New("invalid Display snapshot token")
	}
	return nil
}

func (interceptor *authenticationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (interceptor *authenticationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// GetSnapshot returns the complete projection established by Display authentication.
func (*Handler) GetSnapshot(
	ctx context.Context,
	_ *connect.Request[displayv1.GetSnapshotRequest],
) (*connect.Response[displayv1.GetSnapshotResponse], error) {
	authorized, ok := ctx.Value(snapshotContextKey{}).(authorizedSnapshot)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
	}
	return connect.NewResponse(&displayv1.GetSnapshotResponse{
		Snapshot: snapshotMessage(authorized.snapshot, authorized.cursor, authorized.snapshotToken),
	}), nil
}

func snapshotMessage(
	found displays.Snapshot,
	cursor displaystream.Cursor,
	snapshotToken string,
) *displayv1.DisplaySnapshot {
	result := &displayv1.DisplaySnapshot{
		ProtocolVersion:      found.ProtocolVersion,
		ServerTime:           timestamppb.New(found.ServerTime),
		DisplayId:            int64(found.Display.ID),
		DisplayName:          found.Display.Name,
		ActiveEventId:        int64(found.ActiveEventID),
		EventName:            found.EventName,
		EventTimezone:        found.EventTimezone,
		ActivationGeneration: int64(found.ActivationGeneration),
		PublishedRevision:    int64(found.PublishedRevision),
		LocationId:           int64(found.LocationID),
		LocationName:         found.LocationName,
		ViewKey:              found.ViewKey,
		Standby:              found.Standby,
		StreamId:             cursor.StreamID,
		StreamPosition:       &cursor.Position,
		SnapshotToken:        snapshotToken,
	}
	for _, item := range found.Sessions {
		result.Sessions = append(result.Sessions, sessionMessage(item))
	}
	return result
}

func snapshotState(found displays.Snapshot, cursor displaystream.Cursor) displaystream.SnapshotState {
	return displaystream.SnapshotState{
		Cursor: cursor, DisplayID: int64(found.Display.ID),
		ProtocolVersion: found.ProtocolVersion, ActiveEventID: int64(found.ActiveEventID),
		ActivationGeneration: int64(found.ActivationGeneration),
		PublishedRevision:    int64(found.PublishedRevision),
	}
}

func sessionMessage(found displays.Session) *displayv1.DisplaySession {
	result := &displayv1.DisplaySession{
		Id: int64(found.ID), Title: found.Title, Speaker: found.Speaker,
		PublicDetails: found.PublicDetails,
		Lifecycle:     found.Lifecycle, LiveStateRevision: int64(found.LiveStateRevision),
		LocationIds: ints64(found.LocationIDs), LaneIds: ints64(found.LaneIDs),
		TrackIds: ints64(found.TrackIDs), Unavailable: found.Unavailable,
		AvailabilityMessage: found.AvailabilityMessage,
	}
	if !found.ForecastStart.IsZero() {
		result.ForecastStart = timestamppb.New(found.ForecastStart)
	}
	if !found.ForecastEnd.IsZero() {
		result.ForecastEnd = timestamppb.New(found.ForecastEnd)
	}
	if !found.ActualStart.IsZero() {
		result.ActualStart = timestamppb.New(found.ActualStart)
	}
	if found.ActualEnd != nil {
		result.ActualEnd = timestamppb.New(*found.ActualEnd)
	}
	return result
}

func acknowledgmentMessage(found displays.Acknowledgment) *displayv1.DisplayAcknowledgment {
	return &displayv1.DisplayAcknowledgment{
		DisplayId: int64(found.DisplayID), ProtocolVersion: found.ProtocolVersion,
		StreamId: found.StreamID, StreamPosition: found.StreamPosition,
		ActiveEventId:        int64(found.ActiveEventID),
		ActivationGeneration: int64(found.ActivationGeneration),
		PublishedRevision:    int64(found.PublishedRevision),
		AppliedAt:            timestamppb.New(found.AppliedAt),
	}
}

func ints64(values []int) []int64 {
	result := make([]int64, 0, len(values))
	for _, value := range values {
		result = append(result, int64(value))
	}
	return result
}

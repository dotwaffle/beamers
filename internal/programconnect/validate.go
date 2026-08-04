package programconnect

import (
	"errors"

	"connectrpc.com/connect"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
func ValidationInterceptor() connect.Interceptor {
	return connectapi.ValidationInterceptor(validateRequest)
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *programv1.GetProgramChannelRequest:
		return channelScope(request.GetEventId(), request.GetSessionId())
	case *programv1.ChangeControlRequest:
		return connectapi.FirstInvalid(
			channelCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision(
				"expected_control_state_revision", request.GetExpectedControlStateRevision(),
			),
		)
	case *programv1.SelectPreviewRequest:
		return connectapi.FirstInvalid(
			channelCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision(
				"expected_control_state_revision", request.GetExpectedControlStateRevision(),
			),
		)
	case *programv1.TakeRequest:
		return connectapi.FirstInvalid(
			channelCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision(
				"expected_live_state_revision", request.GetExpectedLiveStateRevision(),
			),
			connectapi.NonNegativeRevision(
				"expected_entry_order_revision", request.GetExpectedEntryOrderRevision(),
			),
			connectapi.NonNegativeRevision(
				"expected_control_state_revision", request.GetExpectedControlStateRevision(),
			),
		)
	case *programv1.DeferEntryRequest:
		return connectapi.FirstInvalid(
			channelCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.PositiveID("entry_id", request.GetEntryId()),
			connectapi.NonNegativeRevision("expected_entry_revision", request.GetExpectedEntryRevision()),
			connectapi.NonNegativeRevision("expected_program_revision", request.GetExpectedProgramRevision()),
			connectapi.NonNegativeRevision(
				"expected_control_state_revision", request.GetExpectedControlStateRevision(),
			),
		)
	case *programv1.ActOnResultRequest:
		return connectapi.FirstInvalid(
			channelCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision("expected_program_revision", request.GetExpectedProgramRevision()),
			connectapi.NonNegativeRevision(
				"expected_control_state_revision", request.GetExpectedControlStateRevision(),
			),
		)
	default:
		return errors.New("unsupported Program control request")
	}
}

type commandFields struct {
	EventID   int64
	SessionID int64
	CommandID string
}

func channelCommand(fields commandFields) error {
	return connectapi.FirstInvalid(
		channelScope(fields.EventID, fields.SessionID),
		command.ValidateID(fields.CommandID),
	)
}

func channelScope(eventID, sessionID int64) error {
	return connectapi.FirstInvalid(
		connectapi.PositiveID("event_id", eventID),
		connectapi.PositiveID("session_id", sessionID),
	)
}

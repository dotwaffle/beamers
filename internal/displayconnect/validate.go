package displayconnect

import (
	"errors"

	"connectrpc.com/connect"

	displayv1 "github.com/dotwaffle/beamers/gen/beamers/display/v1"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/displays"
)

// ErrorInterceptor translates Display failures into stable Connect codes.
func ErrorInterceptor() connect.Interceptor {
	return connectapi.ErrorInterceptor(connectError)
}

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
// It checks only request shape; whether an acknowledgment matches the Display's
// own authenticated Snapshot stays with the handler, which alone holds it.
func ValidationInterceptor() connect.Interceptor {
	return connectapi.ValidationInterceptor(validateRequest)
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *displayv1.GetSnapshotRequest:
		return nil
	case *displayv1.AcknowledgeRequest:
		return connectapi.FirstInvalid(
			required("protocol_version", request.GetProtocolVersion()),
			required("stream_id", request.GetStreamId()),
			required("snapshot_token", request.GetSnapshotToken()),
			connectapi.NonNegativeRevision("active_event_id", request.GetActiveEventId()),
			connectapi.NonNegativeRevision("activation_generation", request.GetActivationGeneration()),
			connectapi.NonNegativeRevision("published_revision", request.GetPublishedRevision()),
			connectapi.NonNegativeRevision("stage_message_id", request.GetStageMessageId()),
			connectapi.NonNegativeRevision("stage_message_revision", request.GetStageMessageRevision()),
			connectapi.NonNegativeRevision(
				"technical_difficulties_id", request.GetTechnicalDifficultiesId(),
			),
			connectapi.NonNegativeRevision(
				"technical_difficulties_revision", request.GetTechnicalDifficultiesRevision(),
			),
			connectapi.NonNegativeRevision("urgent_notice_id", request.GetUrgentNoticeId()),
			connectapi.NonNegativeRevision("urgent_notice_revision", request.GetUrgentNoticeRevision()),
			connectapi.NonNegativeRevision("emergency_alert_id", request.GetEmergencyAlertId()),
			connectapi.NonNegativeRevision("emergency_alert_revision", request.GetEmergencyAlertRevision()),
		)
	default:
		return errors.New("unsupported Display request")
	}
}

func required(field, value string) error {
	if value == "" {
		return errors.New(field + " is required")
	}
	return nil
}

func connectError(err error) error {
	switch {
	case errors.Is(err, displays.ErrDisplayAuthentication):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("display authentication required"))
	case errors.Is(err, displays.ErrInvalidAcknowledgment):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, displays.ErrAcknowledgmentRegression),
		errors.Is(err, displays.ErrAcknowledgmentConflict):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("display request failed"))
	}
}

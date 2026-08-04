package competitionconnect

import (
	"errors"

	"connectrpc.com/connect"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
func ValidationInterceptor() connect.Interceptor {
	return connectapi.ValidationInterceptor(validateRequest)
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *competitionv1.GetCompetitionRequest:
		return competitionScope(request.GetEventId(), request.GetSessionId())
	case *competitionv1.PreflightStartRequest:
		return competitionScope(request.GetEventId(), request.GetSessionId())
	case *competitionv1.PreviewEntryOrderRequest:
		return competitionScope(request.GetEventId(), request.GetSessionId())
	case *competitionv1.PreflightEndRequest:
		return competitionScope(request.GetEventId(), request.GetSessionId())
	case *competitionv1.ConfigureReadinessRequest:
		return connectapi.FirstInvalid(
			competitionCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision("expected_readiness_revision", request.GetExpectedReadinessRevision()),
		)
	case *competitionv1.CreateEntryRequest:
		return competitionCommand(commandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			CommandID: request.GetCommandId(),
		})
	case *competitionv1.UpdateEntryRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.ChangeEntryDispositionRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.ReviewEntryRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.RecordTechnicalFailureRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.ResolveEntryRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.SetEntryReleaseHoldRequest:
		return entryCommand(entryCommandFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
			ExpectedRevision: request.GetExpectedRevision(),
		})
	case *competitionv1.SetEntryAttachmentReadinessRequest:
		return connectapi.FirstInvalid(
			entryCommand(entryCommandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				EntryID: request.GetEntryId(), CommandID: request.GetCommandId(),
				ExpectedRevision: request.GetExpectedRevision(),
			}),
			connectapi.PositiveID("attachment_version_id", request.GetAttachmentVersionId()),
		)
	case *competitionv1.ConfigureEntryOrderRequest:
		if err := connectapi.FirstInvalid(
			competitionCommand(commandFields{
				EventID: request.GetEventId(), SessionID: request.GetSessionId(),
				CommandID: request.GetCommandId(),
			}),
			connectapi.NonNegativeRevision("expected_revision", request.GetExpectedRevision()),
		); err != nil {
			return err
		}
		_, err := connectapi.PositiveInts("manual_entry_ids", request.GetManualEntryIds())
		return err
	default:
		return errors.New("unsupported Competition request")
	}
}

type entryCommandFields struct {
	EventID          int64
	SessionID        int64
	EntryID          int64
	CommandID        string
	ExpectedRevision int64
}

func entryCommand(fields entryCommandFields) error {
	return connectapi.FirstInvalid(
		competitionCommand(commandFields{
			EventID: fields.EventID, SessionID: fields.SessionID, CommandID: fields.CommandID,
		}),
		connectapi.PositiveID("entry_id", fields.EntryID),
		connectapi.NonNegativeRevision("expected_revision", fields.ExpectedRevision),
	)
}

type commandFields struct {
	EventID   int64
	SessionID int64
	CommandID string
}

func competitionCommand(fields commandFields) error {
	return connectapi.FirstInvalid(
		competitionScope(fields.EventID, fields.SessionID),
		command.ValidateID(fields.CommandID),
	)
}

func competitionScope(eventID, sessionID int64) error {
	return connectapi.FirstInvalid(
		connectapi.PositiveID("event_id", eventID),
		connectapi.PositiveID("session_id", sessionID),
	)
}

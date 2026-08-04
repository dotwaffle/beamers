package resultsconnect

import (
	"errors"

	"connectrpc.com/connect"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/connectapi"
)

// ValidationInterceptor rejects malformed protobuf requests before dispatch.
func ValidationInterceptor() connect.Interceptor {
	return connectapi.ValidationInterceptor(validateRequest)
}

func validateRequest(message any) error {
	switch request := message.(type) {
	case *resultsv1.GetCompetitionResultsDraftRequest:
		return competitionScope(request.GetEventId(), request.GetSessionId())
	case *resultsv1.SaveCompetitionResultsDraftRequest:
		return competitionResultsCommand(competitionResultsFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.SaveCompetitionAwardsRequest:
		return competitionResultsCommand(competitionResultsFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.MarkCompetitionResultsReadyRequest:
		return competitionResultsCommand(competitionResultsFields{
			EventID: request.GetEventId(), SessionID: request.GetSessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.DesignatePrizegivingRequest:
		return connectapi.FirstInvalid(
			connectapi.PositiveID("event_id", request.GetEventId()),
			connectapi.PositiveID("ceremony_session_id", request.GetCeremonySessionId()),
			command.ValidateID(request.GetCommandId()),
		)
	case *resultsv1.GetEventAwardsDraftRequest:
		return connectapi.PositiveID("event_id", request.GetEventId())
	case *resultsv1.SaveEventAwardsDraftRequest:
		return connectapi.FirstInvalid(
			connectapi.PositiveID("event_id", request.GetEventId()),
			command.ValidateID(request.GetCommandId()),
			connectapi.NonNegativeRevision("expected_revision", request.GetExpectedRevision()),
		)
	case *resultsv1.MarkEventAwardsReadyRequest:
		return connectapi.FirstInvalid(
			connectapi.PositiveID("event_id", request.GetEventId()),
			command.ValidateID(request.GetCommandId()),
			connectapi.NonNegativeRevision("expected_revision", request.GetExpectedRevision()),
			connectapi.NonNegativeRevision("expected_path_revision", request.GetExpectedPathRevision()),
		)
	case *resultsv1.GetPrizegivingPlanRequest:
		return prizegivingScope(request.GetEventId(), request.GetCeremonySessionId())
	case *resultsv1.SavePrizegivingPlanRequest:
		if err := prizegivingCommand(prizegivingFields{
			EventID: request.GetEventId(), CeremonySessionID: request.GetCeremonySessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		}); err != nil {
			return err
		}
		_, err := connectapi.PositiveInts("competition_session_ids", request.GetCompetitionSessionIds())
		return err
	case *resultsv1.RunPrizegivingPreflightRequest:
		return prizegivingCommand(prizegivingFields{
			EventID: request.GetEventId(), CeremonySessionID: request.GetCeremonySessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.PreviewPrizegivingRequest:
		return prizegivingScope(request.GetEventId(), request.GetCeremonySessionId())
	case *resultsv1.FirePrizegivingResultsCueRequest:
		return connectapi.FirstInvalid(
			prizegivingScope(request.GetEventId(), request.GetCeremonySessionId()),
			command.ValidateID(request.GetCommandId()),
		)
	case *resultsv1.ReleaseStandaloneResultsRequest:
		return connectapi.FirstInvalid(
			connectapi.PositiveID("event_id", request.GetEventId()),
			connectapi.PositiveID("competition_session_id", request.GetCompetitionSessionId()),
			command.ValidateID(request.GetCommandId()),
		)
	case *resultsv1.PreflightStandaloneEventAwardsRequest:
		return connectapi.PositiveID("event_id", request.GetEventId())
	case *resultsv1.ReleaseStandaloneEventAwardsRequest:
		return connectapi.FirstInvalid(
			connectapi.PositiveID("event_id", request.GetEventId()),
			command.ValidateID(request.GetCommandId()),
			connectapi.NonNegativeRevision("expected_draft_revision", request.GetExpectedDraftRevision()),
			connectapi.NonNegativeRevision("expected_path_revision", request.GetExpectedPathRevision()),
		)
	case *resultsv1.GetResultsCorrectionRequest:
		return correctionScope(request.GetEventId(), request.GetScopeSessionId())
	case *resultsv1.SaveResultsCorrectionRequest:
		return connectapi.FirstInvalid(
			correctionCommand(correctionFields{
				EventID: request.GetEventId(), ScopeSessionID: request.GetScopeSessionId(),
				CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
			}),
			connectapi.NonNegativeRevision(
				"base_publication_revision", request.GetBasePublicationRevision(),
			),
		)
	case *resultsv1.ReviewResultsCorrectionRequest:
		return correctionCommand(correctionFields{
			EventID: request.GetEventId(), ScopeSessionID: request.GetScopeSessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.PublishResultsCorrectionRequest:
		return correctionCommand(correctionFields{
			EventID: request.GetEventId(), ScopeSessionID: request.GetScopeSessionId(),
			CommandID: request.GetCommandId(), ExpectedRevision: request.GetExpectedRevision(),
		})
	case *resultsv1.GetResultsCorrectionHistoryRequest:
		return correctionScope(request.GetEventId(), request.GetScopeSessionId())
	default:
		return errors.New("unsupported Results request")
	}
}

type competitionResultsFields struct {
	EventID          int64
	SessionID        int64
	CommandID        string
	ExpectedRevision int64
}

func competitionResultsCommand(fields competitionResultsFields) error {
	return connectapi.FirstInvalid(
		competitionScope(fields.EventID, fields.SessionID),
		command.ValidateID(fields.CommandID),
		connectapi.NonNegativeRevision("expected_revision", fields.ExpectedRevision),
	)
}

type prizegivingFields struct {
	EventID           int64
	CeremonySessionID int64
	CommandID         string
	ExpectedRevision  int64
}

func prizegivingCommand(fields prizegivingFields) error {
	return connectapi.FirstInvalid(
		prizegivingScope(fields.EventID, fields.CeremonySessionID),
		command.ValidateID(fields.CommandID),
		connectapi.NonNegativeRevision("expected_revision", fields.ExpectedRevision),
	)
}

type correctionFields struct {
	EventID          int64
	ScopeSessionID   int64
	CommandID        string
	ExpectedRevision int64
}

func correctionCommand(fields correctionFields) error {
	return connectapi.FirstInvalid(
		correctionScope(fields.EventID, fields.ScopeSessionID),
		command.ValidateID(fields.CommandID),
		connectapi.NonNegativeRevision("expected_revision", fields.ExpectedRevision),
	)
}

func competitionScope(eventID, sessionID int64) error {
	return connectapi.FirstInvalid(
		connectapi.PositiveID("event_id", eventID),
		connectapi.PositiveID("session_id", sessionID),
	)
}

func prizegivingScope(eventID, ceremonySessionID int64) error {
	return connectapi.FirstInvalid(
		connectapi.PositiveID("event_id", eventID),
		connectapi.PositiveID("ceremony_session_id", ceremonySessionID),
	)
}

// correctionScope accepts a zero scope_session_id, which addresses the
// Event-wide correction rather than one Competition Session.
func correctionScope(eventID, scopeSessionID int64) error {
	return connectapi.FirstInvalid(
		connectapi.PositiveID("event_id", eventID),
		connectapi.NonNegativeRevision("scope_session_id", scopeSessionID),
	)
}

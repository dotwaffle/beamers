package resultsconnect

import (
	"testing"

	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
)

func TestValidateRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		request     any
		expectedErr string
	}{
		{
			name:    "draft read accepts a Competition Session",
			request: &resultsv1.GetCompetitionResultsDraftRequest{EventId: 1, SessionId: 2},
		},
		{
			name:        "draft read rejects a missing Session",
			request:     &resultsv1.GetCompetitionResultsDraftRequest{EventId: 1},
			expectedErr: "session_id must be a positive supported integer",
		},
		{
			name: "draft save accepts an initial revision",
			request: &resultsv1.SaveCompetitionResultsDraftRequest{
				EventId: 1, SessionId: 2, CommandId: "save-1",
			},
		},
		{
			name: "draft save rejects a missing identity",
			request: &resultsv1.SaveCompetitionResultsDraftRequest{
				EventId: 1, SessionId: 2,
			},
			expectedErr: "command_id must be 1 to 200 visible characters",
		},
		{
			name:    "Event awards read needs only an Event",
			request: &resultsv1.GetEventAwardsDraftRequest{EventId: 1},
		},
		{
			name: "prizegiving plan accepts Competition Sessions",
			request: &resultsv1.SavePrizegivingPlanRequest{
				EventId: 1, CeremonySessionId: 2, CommandId: "plan-1",
				CompetitionSessionIds: []int64{3, 4},
			},
		},
		{
			name: "prizegiving plan rejects an unusable Competition Session",
			request: &resultsv1.SavePrizegivingPlanRequest{
				EventId: 1, CeremonySessionId: 2, CommandId: "plan-1",
				CompetitionSessionIds: []int64{3, -4},
			},
			expectedErr: "competition_session_ids must be a positive supported integer",
		},
		{
			name:    "Event-wide correction accepts an absent scope Session",
			request: &resultsv1.GetResultsCorrectionRequest{EventId: 1},
		},
		{
			name:        "correction rejects a negative scope Session",
			request:     &resultsv1.GetResultsCorrectionRequest{EventId: 1, ScopeSessionId: -1},
			expectedErr: "scope_session_id must be a nonnegative supported integer",
		},
		{
			name:        "unknown message is refused",
			request:     &resultsv1.GetEventAwardsDraftResponse{},
			expectedErr: "unsupported Results request",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateRequest(test.request)
			if test.expectedErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != test.expectedErr {
				t.Fatalf("expected %q, got %v", test.expectedErr, err)
			}
		})
	}
}

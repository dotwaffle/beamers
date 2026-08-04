package competitionconnect

import (
	"testing"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
)

func TestValidateRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		request     any
		expectedErr string
	}{
		{
			name:    "read accepts a complete scope",
			request: &competitionv1.GetCompetitionRequest{EventId: 1, SessionId: 2},
		},
		{
			name:        "read rejects a missing Event",
			request:     &competitionv1.GetCompetitionRequest{SessionId: 2},
			expectedErr: "event_id must be a positive supported integer",
		},
		{
			name:        "read rejects a missing Session",
			request:     &competitionv1.GetCompetitionRequest{EventId: 1},
			expectedErr: "session_id must be a positive supported integer",
		},
		{
			name: "command accepts a stable identity",
			request: &competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: 2, CommandId: "create-entry-1",
			},
		},
		{
			name:        "command rejects a missing identity",
			request:     &competitionv1.CreateEntryRequest{EventId: 1, SessionId: 2},
			expectedErr: "command_id must be 1 to 200 visible characters",
		},
		{
			name: "entry command accepts an initial revision",
			request: &competitionv1.ReviewEntryRequest{
				EventId: 1, SessionId: 2, EntryId: 3, CommandId: "review-1",
			},
		},
		{
			name: "entry command rejects a missing Entry",
			request: &competitionv1.ReviewEntryRequest{
				EventId: 1, SessionId: 2, CommandId: "review-1",
			},
			expectedErr: "entry_id must be a positive supported integer",
		},
		{
			name: "entry command rejects a negative expected revision",
			request: &competitionv1.ReviewEntryRequest{
				EventId: 1, SessionId: 2, EntryId: 3, CommandId: "review-1", ExpectedRevision: -1,
			},
			expectedErr: "expected_revision must be a nonnegative supported integer",
		},
		{
			name: "entry order accepts manual identifiers",
			request: &competitionv1.ConfigureEntryOrderRequest{
				EventId: 1, SessionId: 2, CommandId: "order-1", ManualEntryIds: []int64{4, 5},
			},
		},
		{
			name: "entry order rejects an unusable manual identifier",
			request: &competitionv1.ConfigureEntryOrderRequest{
				EventId: 1, SessionId: 2, CommandId: "order-1", ManualEntryIds: []int64{4, 0},
			},
			expectedErr: "manual_entry_ids must be a positive supported integer",
		},
		{
			name:        "unknown message is refused",
			request:     &competitionv1.GetCompetitionResponse{},
			expectedErr: "unsupported Competition request",
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

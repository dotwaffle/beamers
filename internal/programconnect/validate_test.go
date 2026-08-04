package programconnect

import (
	"testing"

	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
)

func TestValidateRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		request     any
		expectedErr string
	}{
		{
			name:    "channel read accepts a complete scope",
			request: &programv1.GetProgramChannelRequest{EventId: 1, SessionId: 2},
		},
		{
			name:        "channel read rejects a missing Session",
			request:     &programv1.GetProgramChannelRequest{EventId: 1},
			expectedErr: "session_id must be a positive supported integer",
		},
		{
			name: "control change accepts an initial control revision",
			request: &programv1.ChangeControlRequest{
				EventId: 1, SessionId: 2, CommandId: "claim-1",
			},
		},
		{
			name: "control change rejects a missing identity",
			request: &programv1.ChangeControlRequest{
				EventId: 1, SessionId: 2,
			},
			expectedErr: "command_id must be 1 to 200 visible characters",
		},
		{
			name: "take rejects a negative live state revision",
			request: &programv1.TakeRequest{
				EventId: 1, SessionId: 2, CommandId: "take-1", ExpectedLiveStateRevision: -1,
			},
			expectedErr: "expected_live_state_revision must be a nonnegative supported integer",
		},
		{
			name: "defer rejects a missing Entry",
			request: &programv1.DeferEntryRequest{
				EventId: 1, SessionId: 2, CommandId: "defer-1",
			},
			expectedErr: "entry_id must be a positive supported integer",
		},
		{
			name: "act on result accepts a complete command",
			request: &programv1.ActOnResultRequest{
				EventId: 1, SessionId: 2, CommandId: "reveal-1",
			},
		},
		{
			name:        "unknown message is refused",
			request:     &programv1.GetProgramChannelResponse{},
			expectedErr: "unsupported Program control request",
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

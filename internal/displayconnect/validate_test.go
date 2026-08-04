package displayconnect

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	displayv1 "github.com/dotwaffle/beamers/gen/beamers/display/v1"
	"github.com/dotwaffle/beamers/internal/displays"
)

func acknowledgment() *displayv1.AcknowledgeRequest {
	return &displayv1.AcknowledgeRequest{
		ProtocolVersion: "1", StreamId: "stream-1", SnapshotToken: "token-1",
		ActiveEventId: 1, ActivationGeneration: 1, PublishedRevision: 2,
	}
}

func TestValidateRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		request     any
		expectedErr string
	}{
		{name: "snapshot read carries no fields", request: &displayv1.GetSnapshotRequest{}},
		{name: "acknowledgment accepts a complete cursor", request: acknowledgment()},
		{
			name: "acknowledgment rejects a missing protocol version",
			request: func() *displayv1.AcknowledgeRequest {
				request := acknowledgment()
				request.ProtocolVersion = ""
				return request
			}(),
			expectedErr: "protocol_version is required",
		},
		{
			name: "acknowledgment rejects a missing stream",
			request: func() *displayv1.AcknowledgeRequest {
				request := acknowledgment()
				request.StreamId = ""
				return request
			}(),
			expectedErr: "stream_id is required",
		},
		{
			name: "acknowledgment rejects a missing snapshot token",
			request: func() *displayv1.AcknowledgeRequest {
				request := acknowledgment()
				request.SnapshotToken = ""
				return request
			}(),
			expectedErr: "snapshot_token is required",
		},
		{
			name: "acknowledgment rejects a negative Emergency Alert revision",
			request: func() *displayv1.AcknowledgeRequest {
				request := acknowledgment()
				request.EmergencyAlertRevision = -1
				return request
			}(),
			expectedErr: "emergency_alert_revision must be a nonnegative supported integer",
		},
		{
			name:        "unknown message is refused",
			request:     &displayv1.AcknowledgeResponse{},
			expectedErr: "unsupported Display request",
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

func TestConnectError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		expectedCode connect.Code
	}{
		{
			name:         "authentication",
			err:          displays.ErrDisplayAuthentication,
			expectedCode: connect.CodeUnauthenticated,
		},
		{
			name:         "malformed acknowledgment",
			err:          displays.ErrInvalidAcknowledgment,
			expectedCode: connect.CodeInvalidArgument,
		},
		{
			name:         "regressed acknowledgment",
			err:          displays.ErrAcknowledgmentRegression,
			expectedCode: connect.CodeFailedPrecondition,
		},
		{
			name:         "conflicting acknowledgment",
			err:          displays.ErrAcknowledgmentConflict,
			expectedCode: connect.CodeFailedPrecondition,
		},
		{name: "unclassified", err: errors.New("boom"), expectedCode: connect.CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var connectErr *connect.Error
			if !errors.As(connectError(test.err), &connectErr) {
				t.Fatal("expected a Connect error")
			}
			if connectErr.Code() != test.expectedCode {
				t.Fatalf("expected %v, got %v", test.expectedCode, connectErr.Code())
			}
		})
	}
}

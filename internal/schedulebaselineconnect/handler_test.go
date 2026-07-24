package schedulebaselineconnect

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	schedulebaselinev1 "github.com/dotwaffle/beamers/gen/beamers/schedulebaseline/v1"
	"github.com/dotwaffle/beamers/internal/schedulebaseline"
)

func TestValidationRejectsMalformedBaselineRequests(t *testing.T) {
	tests := []struct {
		name    string
		request any
	}{
		{name: "missing Event ID", request: &schedulebaselinev1.PreviewRequest{}},
		{name: "missing command ID", request: &schedulebaselinev1.CaptureRequest{
			EventId: 1, Confirmation: &schedulebaselinev1.Confirmation{Fingerprint: "preview"},
		}},
		{name: "missing confirmation", request: &schedulebaselinev1.CaptureRequest{
			EventId: 1, CommandId: "capture",
		}},
		{name: "negative revision", request: &schedulebaselinev1.CaptureRequest{
			EventId: 1, CommandId: "capture",
			Confirmation: &schedulebaselinev1.Confirmation{
				PublishedRevision: -1,
				Fingerprint:       "preview",
			},
		}},
		{name: "missing fingerprint", request: &schedulebaselinev1.CaptureRequest{
			EventId: 1, CommandId: "capture",
			Confirmation: &schedulebaselinev1.Confirmation{},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRequest(test.request); err == nil {
				t.Fatalf("validate request %+v succeeded", test.request)
			}
		})
	}
}

func TestConnectErrorClassifiesBaselineFailures(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: schedulebaseline.ErrProducerRequired, code: connect.CodePermissionDenied},
		{err: schedulebaseline.ErrEventNotFound, code: connect.CodeNotFound},
		{err: schedulebaseline.ErrAlreadyCaptured, code: connect.CodeFailedPrecondition},
		{err: schedulebaseline.ErrStalePreview, code: connect.CodeAborted},
		{err: schedulebaseline.ErrInvalidBaseline, code: connect.CodeFailedPrecondition},
		{err: schedulebaseline.ErrNonActiveAcknowledgment, code: connect.CodeFailedPrecondition},
		{err: schedulebaseline.ErrCommandConflict, code: connect.CodeAlreadyExists},
	}
	for _, test := range tests {
		var converted *connect.Error
		if !errors.As(connectError(test.err), &converted) || converted.Code() != test.code {
			t.Errorf("connect error for %v = %v, want %v", test.err, converted, test.code)
		}
	}
}

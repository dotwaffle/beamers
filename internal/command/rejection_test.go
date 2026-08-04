package command_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	errProducerRequired = errors.New("producer required")
	errRevisionConflict = errors.New("revision conflict")
)

func exampleTable(recordMessage bool) command.RejectionTable {
	return command.RejectionTable{
		Rejections: []command.Rejection{
			{Err: errProducerRequired, Code: "producer_required"},
			{Err: errRevisionConflict, Code: "revision_conflict"},
		},
		RecordMessage: recordMessage,
	}
}

func TestRejectionTableCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		err          error
		expectedCode string
		expectedOK   bool
	}{
		{name: "sentinel", err: errProducerRequired, expectedCode: "producer_required", expectedOK: true},
		{
			name:         "wrapped sentinel",
			err:          fmt.Errorf("save draft: %w", errRevisionConflict),
			expectedCode: "revision_conflict",
			expectedOK:   true,
		},
		{name: "unclassified", err: errors.New("boom")},
		{name: "no error", err: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			code, ok := exampleTable(false).Code(test.err)
			if ok != test.expectedOK || code != test.expectedCode {
				t.Fatalf("expected (%q, %t), got (%q, %t)", test.expectedCode, test.expectedOK, code, ok)
			}
		})
	}
}

func TestRejectionTableSentinel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		code        string
		expectedErr error
	}{
		{name: "known code", code: "revision_conflict", expectedErr: errRevisionConflict},
		{name: "unknown code", code: "invented"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			found := exampleTable(false).Sentinel(test.code)
			if !errors.Is(found, test.expectedErr) {
				t.Fatalf("expected %v, got %v", test.expectedErr, found)
			}
		})
	}
}

// TestRejectionTableRestoresPublicSentinel covers the codes a package
// classifies from a storage sentinel but reports to callers as its own, where
// classifying and restoring must not agree.
func TestRejectionTableRestoresPublicSentinel(t *testing.T) {
	t.Parallel()
	storageErr := errors.New("invalid account session")
	publicErr := errors.New("authentication required")
	table := command.RejectionTable{
		Rejections: []command.Rejection{
			{Err: storageErr, Code: "credential_not_found", Restored: publicErr},
		},
	}
	code, known := table.Code(fmt.Errorf("revoke: %w", storageErr))
	if !known || code != "credential_not_found" {
		t.Fatalf("expected credential_not_found, got %q (%t)", code, known)
	}
	rejected := &store.RejectedCommandError{
		Rejection: store.CommandRejection{Code: "credential_not_found"},
	}
	if restored := table.Restore(rejected); !errors.Is(restored, publicErr) {
		t.Fatalf("expected %v, got %v", publicErr, restored)
	}
}

func TestRejectionTableRoundTripsEveryCode(t *testing.T) {
	t.Parallel()
	table := exampleTable(false)
	for _, known := range table.Rejections {
		code, ok := table.Code(known.Err)
		if !ok || code != known.Code {
			t.Fatalf("expected %q, got %q", known.Code, code)
		}
		found := table.Sentinel(code)
		if !errors.Is(found, known.Err) {
			t.Fatalf("expected %v for %q, got %v", known.Err, code, found)
		}
	}
}

func TestRejectionTableRejection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		recordMessage   bool
		err             error
		expectedCode    string
		expectedMessage string
		expectedOK      bool
	}{
		{
			name: "code only", err: errProducerRequired,
			expectedCode: "producer_required", expectedOK: true,
		},
		{
			name: "code and message", recordMessage: true, err: errProducerRequired,
			expectedCode: "producer_required", expectedMessage: "producer required", expectedOK: true,
		},
		{name: "unclassified", err: errors.New("boom")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rejection, ok := exampleTable(test.recordMessage).Rejection(test.err)
			if ok != test.expectedOK {
				t.Fatalf("expected classified %t, got %t", test.expectedOK, ok)
			}
			if rejection.Code != test.expectedCode || rejection.Message != test.expectedMessage {
				t.Fatalf("expected %q/%q, got %q/%q",
					test.expectedCode, test.expectedMessage, rejection.Code, rejection.Message)
			}
		})
	}
}

func TestRejectionTableRestore(t *testing.T) {
	t.Parallel()
	unknown := &store.RejectedCommandError{Rejection: store.CommandRejection{Code: "invented"}}
	plain := errors.New("decode failed")
	tests := []struct {
		name        string
		err         error
		expectedErr error
	}{
		{
			name:        "known rejection",
			err:         &store.RejectedCommandError{Rejection: store.CommandRejection{Code: "producer_required"}},
			expectedErr: errProducerRequired,
		},
		{name: "unknown rejection", err: unknown, expectedErr: unknown},
		{name: "unrelated error", err: plain, expectedErr: plain},
		{name: "no error", err: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := exampleTable(false).Restore(test.err)
			if !errors.Is(got, test.expectedErr) {
				t.Fatalf("expected %v, got %v", test.expectedErr, got)
			}
		})
	}
}

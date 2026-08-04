package sessioncontrol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/store"
)

func TestValidateLiveDetailFieldsGuardsEveryCorrectableFact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input CorrectLiveDetailsInput
		valid bool
	}{
		{name: "no fields selected"},
		{
			name:  "unsupported field",
			input: CorrectLiveDetailsInput{UpdateFields: []string{"location"}},
		},
		{
			name: "duplicate field",
			input: CorrectLiveDetailsInput{
				Title: "Keynote", UpdateFields: []string{"title", "title"},
			},
		},
		{
			name:  "empty title",
			input: CorrectLiveDetailsInput{UpdateFields: []string{"title"}},
		},
		{
			name: "overlong title",
			input: CorrectLiveDetailsInput{
				Title: strings.Repeat("t", 201), UpdateFields: []string{"title"},
			},
		},
		{
			name: "overlong speaker",
			input: CorrectLiveDetailsInput{
				Speaker: strings.Repeat("s", 201), UpdateFields: []string{"speaker"},
			},
		},
		{
			name: "overlong public details",
			input: CorrectLiveDetailsInput{
				PublicDetails: strings.Repeat("p", 10001),
				UpdateFields:  []string{"public_details"},
			},
		},
		{
			name: "title at its bound",
			input: CorrectLiveDetailsInput{
				Title: strings.Repeat("t", 200), UpdateFields: []string{"title"},
			},
			valid: true,
		},
		{
			name:  "cleared speaker",
			input: CorrectLiveDetailsInput{UpdateFields: []string{"speaker"}},
			valid: true,
		},
		{
			name: "every correctable fact",
			input: CorrectLiveDetailsInput{
				Title: "Keynote", Speaker: "Ada", PublicDetails: "Opening",
				UpdateFields: []string{"title", "speaker", "public_details"},
			},
			valid: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := validateLiveDetailFields(testCase.input)
			if testCase.valid && err != nil {
				t.Fatalf("validate Live Detail fields = %v, want accepted", err)
			}
			if !testCase.valid && !errors.Is(err, ErrLiveDetailFields) {
				t.Fatalf("validate Live Detail fields = %v, want %v", err, ErrLiveDetailFields)
			}
		})
	}
}

func TestSessionTransitionRejectionsSurviveReplay(t *testing.T) {
	t.Parallel()
	for _, rejection := range sessionTransitionRejections.Rejections {
		t.Run(rejection.Code, func(t *testing.T) {
			t.Parallel()
			code, classified := rejectionCode(rejection.Err)
			if !classified || code != rejection.Code {
				t.Fatalf("classify %v = %q, %t, want %q", rejection.Err, code, classified, rejection.Code)
			}
			_, restored := restoreRejected(replayedRejection(t, store.CommandRejection{Code: code}))
			if !errors.Is(restored, rejection.Err) {
				t.Fatalf("replayed %q = %v, want %v", code, restored, rejection.Err)
			}
		})
	}
}

func TestSessionGuardRejectionsSurviveReplay(t *testing.T) {
	t.Parallel()
	for _, rejection := range sessionGuardRejections.Rejections {
		t.Run(rejection.Code, func(t *testing.T) {
			t.Parallel()
			if _, classified := rejectionCode(rejection.Err); classified {
				t.Fatalf("guard failure %v classified as a transition rejection", rejection.Err)
			}
			_, restored := restoreRejected(
				replayedRejection(t, store.CommandRejection{Code: rejection.Code}),
			)
			if !errors.Is(restored, rejection.Err) {
				t.Fatalf("replayed %q = %v, want %v", rejection.Code, restored, rejection.Err)
			}
		})
	}
}

func TestStaleSessionCommandCarriesCurrentStateThroughReplay(t *testing.T) {
	t.Parallel()
	stored := store.LiveSessionState{
		SessionID: 12, SessionRunID: 3, Lifecycle: "Live", LiveStateRevision: 7,
		ActualStart: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}
	execution, err := sessionRejection(
		state(stored), stored, "live_state_revision_conflict", ErrLiveStateRevisionConflict,
	)
	if err != nil {
		t.Fatalf("build stale Session rejection: %v", err)
	}
	var conflict *RevisionConflictError
	if !errors.As(execution.ReturnError(), &conflict) {
		t.Fatalf("stale Session rejection error = %v, want a revision conflict", execution.ReturnError())
	}
	if conflict.Current.LiveStateRevision != stored.LiveStateRevision {
		t.Fatalf("stale Session rejection state = %+v, want revision %d", conflict.Current, stored.LiveStateRevision)
	}
	rejection, rejected := execution.Rejection()
	if !rejected {
		t.Fatal("stale Session rejection committed no durable rejection")
	}
	current, restored := restoreRejected(replayedRejection(t, rejection))
	if !errors.As(restored, &conflict) || !errors.Is(restored, ErrLiveStateRevisionConflict) {
		t.Fatalf("replayed stale Session error = %v, want a revision conflict", restored)
	}
	if current != state(stored) || conflict.Current != state(stored) {
		t.Fatalf("replayed stale Session state = %+v, want %+v", current, state(stored))
	}
}

func TestUnknownSessionRejectionCodeStaysOpaque(t *testing.T) {
	t.Parallel()
	_, restored := restoreRejected(replayedRejection(t, store.CommandRejection{Code: "invented"}))
	if restored == nil || restored.Error() != "session command unavailable" {
		t.Fatalf("replayed unknown Session rejection = %v", restored)
	}
	if _, classified := rejectionCode(errors.New("unclassified")); classified {
		t.Fatal("unclassified Session failure was recorded as a rejection")
	}
}

func TestTimingRejectionsSurviveReplay(t *testing.T) {
	t.Parallel()
	for _, rejection := range timingRejections.Rejections {
		t.Run(rejection.Code, func(t *testing.T) {
			t.Parallel()
			code, classified := timingRejectionCode(rejection.Err)
			if !classified || code != rejection.Code {
				t.Fatalf("classify %v = %q, %t, want %q", rejection.Err, code, classified, rejection.Code)
			}
			restored := restoreTimingRejection(
				replayedRejection(t, store.CommandRejection{Code: code}),
				"adjust target unavailable",
			)
			if !errors.Is(restored, rejection.Err) {
				t.Fatalf("replayed %q = %v, want %v", code, restored, rejection.Err)
			}
		})
	}
}

func TestStaleTimingCommandCarriesCurrentStateThroughReplay(t *testing.T) {
	t.Parallel()
	stored := store.LiveSessionState{
		SessionID: 4, Lifecycle: "Live", LiveStateRevision: 11,
	}
	current := TargetAdjustmentResult{State: state(stored)}
	execution, err := timingCommandRejection(
		current, current.State, stored, "live_state_revision_conflict", ErrLiveStateRevisionConflict,
	)
	if err != nil {
		t.Fatalf("build stale timing rejection: %v", err)
	}
	rejection, rejected := execution.Rejection()
	if !rejected {
		t.Fatal("stale timing rejection committed no durable rejection")
	}
	restored := restoreTimingRejection(
		replayedRejection(t, rejection), "adjust target unavailable",
	)
	var conflict *RevisionConflictError
	if !errors.As(restored, &conflict) || conflict.Current != state(stored) {
		t.Fatalf("replayed stale timing error = %v, want current state %+v", restored, state(stored))
	}
}

func TestUnknownTimingRejectionCodeStaysOpaque(t *testing.T) {
	t.Parallel()
	restored := restoreTimingRejection(
		replayedRejection(t, store.CommandRejection{Code: "invented"}), "pull forward unavailable",
	)
	if restored == nil || restored.Error() != "pull forward unavailable" {
		t.Fatalf("replayed unknown timing rejection = %v", restored)
	}
}

// replayedRejection returns the error a stored rejection receipt decodes to, so
// tests exercise the same restoration path a replayed command takes.
func replayedRejection(t *testing.T, rejection store.CommandRejection) error {
	t.Helper()
	outcome, err := json.Marshal(struct {
		Rejected store.CommandRejection `json:"rejected"`
	}{Rejected: rejection})
	if err != nil {
		t.Fatalf("encode stored rejection: %v", err)
	}
	var stored store.LiveSessionState
	decoded := store.DecodeCommandReceipt(string(outcome), &stored)
	var rejected *store.RejectedCommandError
	if !errors.As(decoded, &rejected) {
		t.Fatalf("decode stored rejection = %v, want a rejected command", decoded)
	}
	return decoded
}

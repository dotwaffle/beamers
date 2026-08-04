package server

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/frontend"
	"github.com/dotwaffle/beamers/internal/presentation"
	"github.com/dotwaffle/beamers/internal/results"
)

// TestResolveFormErrorFieldPicksFirstMatch exercises the shared
// classify-error-to-field core directly: the first matching row wins, a
// dynamic row overrides the static field/label/message, and no match
// leaves every result empty so callers fall back to a message-only
// FormErrors.
func TestResolveFormErrorFieldPicksFirstMatch(t *testing.T) {
	sentinelA := errors.New("sentinel a")
	sentinelB := errors.New("sentinel b")
	rules := []formErrorRule{
		rule(matchErr(sentinelA), "field_a", "Field A", "message a"),
		ruleErrorMessage(matchAll(matchErr(sentinelB), matchAction("wanted")),
			"other-action", "field_b", "Field B"),
	}

	t.Run("first row matches", func(t *testing.T) {
		field, label, message, fieldAction := resolveFormErrorField(rules, sentinelA, "any")
		if field != "field_a" || label != "Field A" || message != "message a" || fieldAction != "any" {
			t.Fatalf("got %q %q %q %q", field, label, message, fieldAction)
		}
	})

	t.Run("second row requires matching action", func(t *testing.T) {
		field, _, message, fieldAction := resolveFormErrorField(rules, sentinelB, "wanted")
		if field != "field_b" || fieldAction != "other-action" || message != sentinelB.Error() {
			t.Fatalf("got field %q fieldAction %q message %q", field, fieldAction, message)
		}
		if field, _, _, _ := resolveFormErrorField(rules, sentinelB, "unwanted"); field != "" {
			t.Fatalf("expected no match, got field %q", field)
		}
	})

	t.Run("no rule matches", func(t *testing.T) {
		field, label, message, _ := resolveFormErrorField(rules, errors.New("unrelated"), "any")
		if field != "" || label != "" || message != "" {
			t.Fatalf("expected empty result, got %q %q %q", field, label, message)
		}
	})
}

// TestFormErrorResultFallsBackToMessageOnly checks the two edge cases every
// per-feature wrapper depends on: an empty field yields a message-only
// FormErrors, and an empty label/message fall back to the field name and
// the caller's default message respectively.
func TestFormErrorResultFallsBackToMessageOnly(t *testing.T) {
	buildFieldID := func(field, action string) string { return action + ":" + field }

	noMatch := formErrorResult("", "", "", "action", "default message", buildFieldID)
	if len(noMatch) != 1 || noMatch[0].FieldID != "" || noMatch[0].Message != "default message" {
		t.Fatalf("unexpected no-match result: %+v", noMatch)
	}

	autoLabel := formErrorResult("crew_notes", "", "", "create-entry", "default", buildFieldID)
	if autoLabel[0].Label != "crew notes" {
		t.Fatalf("expected auto-generated label, got %q", autoLabel[0].Label)
	}
	if autoLabel[0].Message != "default" {
		t.Fatalf("expected fallback message, got %q", autoLabel[0].Message)
	}
	if autoLabel[0].FieldID != "create-entry:crew_notes" {
		t.Fatalf("expected buildFieldID output, got %q", autoLabel[0].FieldID)
	}

	explicit := formErrorResult("field", "Label", "Message", "action", "default", buildFieldID)
	if explicit[0].Label != "Label" || explicit[0].Message != "Message" {
		t.Fatalf("expected explicit label/message preserved, got %+v", explicit[0])
	}
}

// TestEntryFormErrorsClassifiesTailCases pins the classify-error-to-field
// table in entries.go against representative errors, independent of the
// bespoke create-entry/update-entry and resolve-entry multi-field checks
// that run before the table is consulted.
func TestEntryFormErrorsClassifiesTailCases(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		values  url.Values
		field   string
		label   string
		message string
	}{
		{
			name:    "crew reason required",
			err:     competition.ErrCrewReasonRequired,
			values:  url.Values{"action": {"claim-control"}},
			field:   "crew_reason",
			label:   "Crew Reason",
			message: "The Entry cannot take that transition.",
		},
		{
			name:    "invalid input on assign-submitter",
			err:     competition.ErrInvalidInput,
			values:  url.Values{"action": {"assign-submitter"}},
			field:   "account_id",
			label:   "Submitter Account",
			message: "Check the Entry details and try again.",
		},
		{
			name:    "reopen window extension",
			err:     attachments.ErrReopenWindowExtension,
			values:  url.Values{"action": {"extend-reopen-window"}},
			field:   "expires_at",
			label:   "Expiry",
			message: "Choose an expiry later than the current expiry.",
		},
		{
			name:    "attachment invalid input on close-reopen-window",
			err:     attachments.ErrInvalidInput,
			values:  url.Values{"action": {"close-reopen-window"}},
			field:   "confirm_close",
			label:   "Early closure confirmation",
			message: "Check the Attachment details and try again.",
		},
		{
			name:    "unrecognized error falls back to message-only",
			err:     competition.ErrEntryResolution,
			values:  url.Values{"action": {"resolve-entry"}},
			field:   "",
			message: "The Entry cannot take that transition.",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.values.Set("entry_id", "0")
			_, formErrors := entryFormErrors(testCase.err, testCase.values)
			if len(formErrors) != 1 {
				t.Fatalf("expected exactly one FormError, got %d", len(formErrors))
			}
			got := formErrors[0]
			if testCase.field == "" {
				if got.FieldID != "" {
					t.Fatalf("expected no FieldID, got %q", got.FieldID)
				}
				if got.Message != testCase.message {
					t.Fatalf("message: got %q want %q", got.Message, testCase.message)
				}
				return
			}
			wantFieldID := frontend.WorkflowFieldID(testCase.values.Get("action"), 0, 0, 0, testCase.field)
			if got.FieldID != wantFieldID {
				t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
			}
			if got.Label != testCase.label {
				t.Fatalf("Label: got %q want %q", got.Label, testCase.label)
			}
			if got.Message != testCase.message {
				t.Fatalf("Message: got %q want %q", got.Message, testCase.message)
			}
		})
	}
}

// TestEventSettingsErrorsClassifiesTailCases pins the classify-error-to-field
// table in planning.go's eventSettingsErrors.
func TestEventSettingsErrorsClassifiesTailCases(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		action  string
		field   string
		label   string
		message string
	}{
		{
			name:    "event validation error",
			err:     &events.ValidationError{Field: "name", Message: "must not be empty"},
			action:  "",
			field:   "event-name",
			label:   "Event name",
			message: "must not be empty",
		},
		{
			name:    "slug unavailable",
			err:     events.ErrEventSlugUnavailable,
			action:  "",
			field:   "public-slug",
			label:   "Current Event Slug",
			message: "Event Slug is already in use.",
		},
		{
			name:    "attachment invalid input during fire-attachment-release-cue",
			err:     attachments.ErrInvalidInput,
			action:  "fire-attachment-release-cue",
			field:   "release-cue-confirmed",
			label:   "Release confirmation",
			message: "Check the Attachment release settings and confirmation.",
		},
		{
			name:    "unrecognized error yields empty FieldID",
			err:     events.ErrCommandConflict,
			action:  "",
			field:   "",
			message: "Command identity was already used for different work.",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, formErrors := eventSettingsErrors(testCase.err, testCase.action)
			if len(formErrors) != 1 {
				t.Fatalf("expected exactly one FormError, got %d", len(formErrors))
			}
			got := formErrors[0]
			if got.FieldID != testCase.field {
				t.Fatalf("FieldID: got %q want %q", got.FieldID, testCase.field)
			}
			if got.Label != testCase.label {
				t.Fatalf("Label: got %q want %q", got.Label, testCase.label)
			}
			if got.Message != testCase.message {
				t.Fatalf("Message: got %q want %q", got.Message, testCase.message)
			}
		})
	}
}

// TestPresentationSubmissionFormErrorsClassifiesTailCases pins the
// classify-error-to-field table in presentations.go.
func TestPresentationSubmissionFormErrorsClassifiesTailCases(t *testing.T) {
	values := url.Values{"action": {"assign-submitter"}}
	_, formErrors := presentationSubmissionFormErrors(presentation.ErrInvalidInput, values, 5)
	if len(formErrors) != 1 {
		t.Fatalf("expected exactly one FormError, got %d", len(formErrors))
	}
	got := formErrors[0]
	wantFieldID := frontend.WorkflowFieldID("assign-submitter", 0, 0, 0, "account_id")
	if got.FieldID != wantFieldID {
		t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
	}
	if got.Label != "Submitter Account" {
		t.Fatalf("Label: got %q", got.Label)
	}
}

// TestAdministrationFormErrorsClassifiesTailCases pins the
// classify-error-to-field table in administration.go, including its
// bespoke activate/errInvalidAdministrationInput post-processing.
func TestAdministrationFormErrorsClassifiesTailCases(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		values url.Values
		field  string
		label  string
	}{
		{
			name:   "account exists",
			err:    auth.ErrAccountExists,
			values: url.Values{"action": {"create-account"}},
			field:  "account_handle", label: "Account Handle",
		},
		{
			name:   "invalid administration input on grant falls back to account_id",
			err:    errInvalidAdministrationInput,
			values: url.Values{"action": {"grant"}},
			field:  "account_id", label: "Account",
		},
		{
			name:   "invalid administration input during activate uses confirmation fallback",
			err:    errInvalidAdministrationInput,
			values: url.Values{"action": {"activate"}},
			field:  "confirmation", label: "Activation confirmation",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			status, message, known := administrationError(testCase.err)
			if status == 0 || !known {
				message = "fallback"
			}
			formErrors := administrationFormErrors(testCase.err, testCase.values, message)
			if len(formErrors) != 1 {
				t.Fatalf("expected exactly one FormError, got %d", len(formErrors))
			}
			got := formErrors[0]
			targetID := administrationTargetID(testCase.values.Get("action"), testCase.values)
			wantFieldID := frontend.AdministrationFieldID(testCase.values.Get("action"), targetID, testCase.field)
			if got.FieldID != wantFieldID {
				t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
			}
			if got.Label != testCase.label {
				t.Fatalf("Label: got %q want %q", got.Label, testCase.label)
			}
		})
	}
}

// TestOperationFormErrorsClassifiesTailCases pins the
// classify-error-to-field table in operations.go, including the
// enroll-display id-zero special case and the assign-display session ID
// reassignment.
func TestOperationFormErrorsClassifiesTailCases(t *testing.T) {
	values := url.Values{}
	formErrors := operationFormErrors(
		displays.ErrInvalidDisplay, "enroll-display", 7, values, "default",
	)
	got := formErrors[0]
	wantFieldID := frontend.OperationFieldID("enroll-display", 0, "code")
	if got.FieldID != wantFieldID {
		t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
	}
	if got.Message != "Enter a valid Enrollment code." {
		t.Fatalf("Message: got %q", got.Message)
	}

	values = url.Values{"display_id": {"42"}}
	formErrors = operationFormErrors(
		displays.ErrAssignmentReference, "assign-display", 0, values, "default",
	)
	got = formErrors[0]
	wantFieldID = frontend.OperationFieldID("assign-display", 42, "location_id")
	if got.FieldID != wantFieldID {
		t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
	}

	formErrors = operationFormErrors(
		errors.New("unrelated"), "start-session", 3, url.Values{}, "default message",
	)
	if formErrors[0].FieldID != "" || formErrors[0].Message != "default message" {
		t.Fatalf("expected message-only fallback, got %+v", formErrors[0])
	}
}

// TestResultsFormErrorsClassifiesTailCases pins the
// classify-error-to-field table in results.go, including the fixed
// "save-results-draft" action override and the err.Error()-derived message.
func TestResultsFormErrorsClassifiesTailCases(t *testing.T) {
	values := url.Values{
		"action": {"mark-results-ready"}, "competition_session_id": {"9"},
	}
	formErrors := resultsFormErrors(results.ErrDisposition, values, "default message")
	got := formErrors[0]
	wantFieldID := frontend.ResultsFieldID("save-results-draft", 9, "disposition")
	if got.FieldID != wantFieldID {
		t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
	}
	if got.Message != results.ErrDisposition.Error() {
		t.Fatalf("Message: got %q want err.Error()", got.Message)
	}

	formErrors = resultsFormErrors(results.ErrInvalidAward, values, "default message")
	got = formErrors[0]
	wantFieldID = frontend.ResultsFieldID("mark-results-ready", 9, "award_details")
	if got.FieldID != wantFieldID {
		t.Fatalf("FieldID: got %q want %q", got.FieldID, wantFieldID)
	}

	formErrors = resultsFormErrors(errors.New("unrelated"), values, "default message")
	if formErrors[0].FieldID != "" || formErrors[0].Message != "default message" {
		t.Fatalf("expected message-only fallback, got %+v", formErrors[0])
	}
}

// TestOperationErrorPresent guards against operationError silently losing
// its "known" bool wiring if the shared core refactor ever touches it.
func TestOperationErrorPresent(t *testing.T) {
	status, _, known := operationError(errInvalidOperationInput)
	if !known || status != http.StatusBadRequest {
		t.Fatalf("got status %d known %v", status, known)
	}
}

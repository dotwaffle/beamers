package server

import (
	"errors"
	"slices"
	"strings"

	"github.com/dotwaffle/beamers/internal/frontend"
)

// formErrorRule is one row of a per-feature declarative table used by
// resolveFormErrorField: "when this error/action combination applies, use
// this FieldID name, Label, and Message" (or, for validation errors that
// carry their own field, derive them dynamically from the error). Adding a
// new validation rule to a feature means adding one row to its table, not
// a new case in a bespoke switch.
type formErrorRule struct {
	match       func(err error, action string) bool
	fieldAction string
	field       string
	label       string
	message     string
	dynamic     func(err error) (field, label, message string)
}

// rule builds a static formErrorRule: match decides whether the row
// applies, and field/label/message are used verbatim. An empty label is
// filled in by formErrorResult from the field name; an empty message
// falls back to the caller's default status message. The FieldID is built
// against the submitted action.
func rule(match func(error, string) bool, field, label, message string) formErrorRule {
	return formErrorRule{match: match, field: field, label: label, message: message}
}

// ruleErrorMessage is like rule, but the FieldID is built against
// fieldAction rather than the submitted action (some features attribute an
// error to a fixed action's field regardless of which submitted action
// produced it), and the Message is always err.Error() rather than a fixed
// string or the caller's default status message.
func ruleErrorMessage(match func(error, string) bool, fieldAction, field, label string) formErrorRule {
	return formErrorRule{
		match: match, fieldAction: fieldAction, field: field, label: label,
		dynamic: func(err error) (string, string, string) { return field, label, err.Error() },
	}
}

// ruleValidation matches whenever errors.As finds a T in the error chain,
// deriving the field/label/message from that validation error itself
// rather than from a static row. T is normally a pointer type such as
// *rundown.ValidationError or *events.ValidationError.
func ruleValidation[T error](fieldOf func(T) (field, label, message string)) formErrorRule {
	return formErrorRule{
		match: func(err error, _ string) bool {
			var target T
			return errors.As(err, &target)
		},
		dynamic: func(err error) (string, string, string) {
			var target T
			errors.As(err, &target)
			return fieldOf(target)
		},
	}
}

// matchErr reports whether err wraps target.
func matchErr(target error) func(error, string) bool {
	return func(err error, _ string) bool { return errors.Is(err, target) }
}

// matchErrAny reports whether err wraps any of targets.
func matchErrAny(targets ...error) func(error, string) bool {
	return func(err error, _ string) bool {
		for _, target := range targets {
			if errors.Is(err, target) {
				return true
			}
		}
		return false
	}
}

// matchAction reports whether the submitted action is one of actions.
func matchAction(actions ...string) func(error, string) bool {
	return func(_ error, action string) bool {
		return slices.Contains(actions, action)
	}
}

// matchAll reports whether every matcher applies.
func matchAll(matchers ...func(error, string) bool) func(error, string) bool {
	return func(err error, action string) bool {
		for _, matcher := range matchers {
			if !matcher(err, action) {
				return false
			}
		}
		return true
	}
}

// resolveFormErrorField walks rules in order and returns the first
// matching row's field, label, message, and the action its FieldID should
// be built against. All four are empty when no row matches, meaning the
// caller should fall back to a message-only FormErrors via formErrorResult.
func resolveFormErrorField(
	rules []formErrorRule,
	err error,
	action string,
) (field, label, message, fieldAction string) {
	for _, candidate := range rules {
		if !candidate.match(err, action) {
			continue
		}
		fieldAction = candidate.fieldAction
		if fieldAction == "" {
			fieldAction = action
		}
		if candidate.dynamic != nil {
			field, label, message = candidate.dynamic(err)
			return field, label, message, fieldAction
		}
		return candidate.field, candidate.label, candidate.message, fieldAction
	}
	return "", "", "", action
}

// formErrorResult finishes what resolveFormErrorField started: an empty
// field means no rule matched, so the caller gets a message-only
// FormErrors; otherwise a blank label is derived from the field name and a
// blank message falls back to defaultMessage before buildFieldID turns the
// field name into the frontend FieldID string.
func formErrorResult(
	field, label, message, fieldAction, defaultMessage string,
	buildFieldID func(field, action string) string,
) frontend.FormErrors {
	if field == "" {
		return frontend.FormErrors{{Message: defaultMessage}}
	}
	if label == "" {
		label = strings.ReplaceAll(field, "_", " ")
	}
	if message == "" {
		message = defaultMessage
	}
	return frontend.FormErrors{{
		FieldID: buildFieldID(field, fieldAction), Label: label, Message: message,
	}}
}

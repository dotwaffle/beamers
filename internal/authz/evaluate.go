package authz

import (
	"errors"
	"fmt"
	"slices"

	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrUnknownAction means no Capability Table row declares this action.
	// It is a programming error, not a refusal: the completeness test exists
	// so that a new action without a row fails the build rather than reaching
	// production, and the evaluator fails closed if one ever does.
	ErrUnknownAction = errors.New("no Capability Table row for command action")
	// ErrScopeFactsMismatch means the plan supplied facts of a different
	// scope dimension than its row declares, or supplied none at all. A
	// scoped row never evaluates factless.
	ErrScopeFactsMismatch = errors.New("command scope facts do not match its Capability Table row")
	// ErrUndeclaredDemand means the plan demanded a Capability its row does
	// not list in TargetCapabilities.
	ErrUndeclaredDemand = errors.New("command demanded a Capability its Capability Table row does not declare")
)

// RefusalError is one authorization refusal the Capability Table produced. Code is
// the durable rejection code recorded in the Command Receipt.
type RefusalError struct {
	Action     string
	Capability Capability
	Code       string
}

// Error describes the refusal for logs and for callers that do not translate it
// into a domain sentinel.
func (refusal *RefusalError) Error() string {
	if refusal.Capability == "" {
		return fmt.Sprintf("%s refused: out of scope", refusal.Action)
	}
	return fmt.Sprintf("%s refused: %s required", refusal.Action, refusal.Capability)
}

// Request is one authorization question: may this identity perform this action
// against these scope facts?
type Request struct {
	// Identity is the authenticated Crew Member acting, when there is one.
	Identity viewer.Identity
	// Authenticated distinguishes an absent identity from a zero one, so an
	// unauthenticated caller cannot be mistaken for an Account with no grants.
	Authenticated bool
	// Action is the name recorded in the Command Receipt.
	Action string
	// Facts are the scope facts the command's plan phase loaded.
	Facts Facts
}

// Evaluate applies the Capability Table row for request.Action.
//
// It returns nil when the action is admitted, a *RefusalError when the table refuses
// it, and one of the package sentinels when the declaration itself is wrong. It
// never returns nil for a question it could not answer.
func Evaluate(request Request) error {
	row, found := Lookup(request.Action)
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownAction, request.Action)
	}
	if request.Facts.replayed {
		return nil
	}
	if !request.Facts.supplied || request.Facts.scope != row.Scope {
		return fmt.Errorf(
			"%w: %s declares %s", ErrScopeFactsMismatch, request.Action, row.Scope,
		)
	}
	for _, demanded := range request.Facts.demanded {
		if !slices.Contains(row.TargetCapabilities, demanded) {
			return fmt.Errorf("%w: %s demanded %s", ErrUndeclaredDemand, request.Action, demanded)
		}
	}

	required := append(append([]Capability{}, row.Capabilities...), request.Facts.demanded...)
	if len(required) == 0 && row.Scope == ScopeNone {
		return nil
	}
	if !request.Authenticated {
		if len(required) == 0 {
			return nil
		}
		return &RefusalError{Action: request.Action, Capability: required[0], Code: row.Code}
	}
	for _, capability := range required {
		if !holds(request.Identity, request.Facts.eventID, capability) {
			return &RefusalError{Action: request.Action, Capability: capability, Code: row.Code}
		}
	}
	if inScope(request.Identity, request.Facts) {
		return nil
	}
	return &RefusalError{Action: request.Action, Code: row.ScopeCode}
}

// inScope applies the row's scope dimension to the facts the plan loaded. Each
// branch mirrors the store predicate it replaces exactly, including the
// Producer shortcut and the empty-fact refusal.
func inScope(identity viewer.Identity, facts Facts) bool {
	switch facts.scope {
	case ScopeNone:
		return true
	case ScopeEvent:
		return facts.eventID > 0
	case ScopeLanes:
		if facts.eventID <= 0 {
			return false
		}
		if identity.CanProduceEvent(facts.eventID) {
			return true
		}
		if len(facts.laneIDs) == 0 {
			return false
		}
		for _, laneID := range facts.laneIDs {
			if !identity.CanOperateLane(facts.eventID, laneID) {
				return false
			}
		}
		return true
	case ScopeDisplayGroups:
		if facts.eventID <= 0 {
			return false
		}
		if identity.CanProduceEvent(facts.eventID) {
			return true
		}
		if len(facts.laneIDs) > 0 {
			for _, laneID := range facts.laneIDs {
				if !identity.CanOperateLane(facts.eventID, laneID) {
					return false
				}
			}
			return true
		}
		if len(facts.groupKeys) == 0 {
			return false
		}
		for _, key := range facts.groupKeys {
			if !identity.CanOperateDisplayGroup(facts.eventID, key) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

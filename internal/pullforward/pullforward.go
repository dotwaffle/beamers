// Package pullforward previews explicit early-finish timing changes.
package pullforward

import (
	"errors"
	"time"

	"github.com/dotwaffle/beamers/internal/timingripple"
)

// State is the authoritative input to one Pull Forward preview.
type State struct {
	SessionID int
	Revision  int
	ActualEnd time.Time
	Timing    []timingripple.Session
}

// Result is the complete deterministic Pull Forward preview.
type Result struct {
	Changes     []timingripple.Change
	Effects     []timingripple.Effect
	Fingerprint string
}

// Preview calculates eligible later Soft-Boundary movement without mutation.
func Preview(state State) (Result, error) {
	if state.SessionID <= 0 || state.Revision < 0 || state.ActualEnd.IsZero() ||
		len(state.Timing) == 0 {
		return Result{}, errors.New("pull-forward state is invalid")
	}
	action := timingripple.PullForward{SessionID: state.SessionID, ActualEnd: state.ActualEnd}
	plan, err := timingripple.Calculate(state.Timing, action)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Changes: plan.Changes, Effects: plan.Effects,
		Fingerprint: timingripple.Fingerprint(state.Timing, action, state.Revision),
	}, nil
}

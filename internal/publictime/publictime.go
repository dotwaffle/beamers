// Package publictime chooses attendee-facing time facts and semantic labels.
package publictime

import (
	"errors"
	"fmt"
	"time"
)

// ErrImpossibleState means lifecycle and time facts cannot form a valid presentation.
var ErrImpossibleState = errors.New("impossible public time state")

// Lifecycle is the current progression state of a Session.
type Lifecycle string

// Labels identify the selected attendee-facing time facts.
const (
	// Scheduled presents current Forecast Time.
	Scheduled Lifecycle = "Scheduled"
	// Live presents normalized Actual Start and Forecast End.
	Live Lifecycle = "Live"
	// Ended presents normalized Actual Time.
	Ended Lifecycle = "Ended"
	// Canceled presents the last Forecast Time.
	Canceled Lifecycle = "Canceled"
)

// Label identifies the meaning of one attendee-facing time.
type Label string

const (
	// LabelForecastStart identifies a forecast start.
	LabelForecastStart Label = "Forecast Start"
	// LabelForecastEnd identifies a forecast end.
	LabelForecastEnd Label = "Forecast End"
	// LabelActualStart identifies an actual start.
	LabelActualStart Label = "Actual Start"
	// LabelActualEnd identifies an actual end.
	LabelActualEnd Label = "Actual End"
	// LabelLastForecastStart identifies a canceled Session's final forecast start.
	LabelLastForecastStart Label = "Last Forecast Start"
	// LabelLastForecastEnd identifies a canceled Session's final forecast end.
	LabelLastForecastEnd Label = "Last Forecast End"
	// LabelWas identifies the immutable baseline start.
	LabelWas Label = "Was"
)

// Range contains required start and end instants.
type Range struct {
	Start time.Time
	End   time.Time
}

// OptionalRange contains independently optional start and end instants.
type OptionalRange struct {
	Start *time.Time
	End   *time.Time
}

// Facts contains the semantic time facts for one public Session.
type Facts struct {
	Lifecycle     Lifecycle
	Forecast      Range
	Actual        OptionalRange
	Communicated  OptionalRange
	RunDuration   time.Duration
	BaselineStart *time.Time
}

// Point is one labeled attendee-facing instant.
type Point struct {
	Time  time.Time
	Label Label
}

// Presentation contains the lifecycle-specific public time range and optional history.
type Presentation struct {
	Start Point
	End   Point
	Was   *Point
}

// StateError identifies the lifecycle fact that made presentation impossible.
type StateError struct {
	Lifecycle Lifecycle
	Fact      string
}

func (failure *StateError) Error() string {
	return fmt.Sprintf("%s: %s %s", ErrImpossibleState, failure.Lifecycle, failure.Fact)
}

// Unwrap classifies every StateError as ErrImpossibleState.
func (failure *StateError) Unwrap() error {
	return ErrImpossibleState
}

// Present selects and validates one Session's attendee-facing time facts.
func Present(facts Facts) (Presentation, error) {
	if err := validateForecast(facts); err != nil {
		return Presentation{}, err
	}

	var result Presentation
	switch facts.Lifecycle {
	case Scheduled:
		result.Start = Point{Time: facts.Forecast.Start, Label: LabelForecastStart}
		result.End = Point{Time: facts.Forecast.End, Label: LabelForecastEnd}
	case Live:
		if err := validateLive(facts); err != nil {
			return Presentation{}, err
		}
		result.Start = Point{
			Time: normalizeActual(
				*facts.Actual.Start,
				optionalTime(facts.Communicated.Start),
				facts.RunDuration,
			),
			Label: LabelActualStart,
		}
		result.End = Point{Time: facts.Forecast.End, Label: LabelForecastEnd}
	case Ended:
		if err := validateEnded(facts); err != nil {
			return Presentation{}, err
		}
		result.Start = Point{
			Time: normalizeActual(
				*facts.Actual.Start,
				optionalTime(facts.Communicated.Start),
				facts.RunDuration,
			),
			Label: LabelActualStart,
		}
		result.End = Point{
			Time: normalizeActual(
				*facts.Actual.End,
				optionalTime(facts.Communicated.End),
				facts.RunDuration,
			),
			Label: LabelActualEnd,
		}
	case Canceled:
		result.Start = Point{Time: facts.Forecast.Start, Label: LabelLastForecastStart}
		result.End = Point{Time: facts.Forecast.End, Label: LabelLastForecastEnd}
	default:
		return Presentation{}, stateError(facts, "lifecycle")
	}

	if facts.BaselineStart != nil && !facts.BaselineStart.Equal(result.Start.Time) {
		result.Was = &Point{Time: *facts.BaselineStart, Label: LabelWas}
	}
	return result, nil
}

func validateForecast(facts Facts) error {
	if facts.Forecast.Start.IsZero() {
		return stateError(facts, "Forecast Start")
	}
	if facts.Forecast.End.IsZero() {
		return stateError(facts, "Forecast End")
	}
	if !facts.Forecast.End.After(facts.Forecast.Start) {
		return stateError(facts, "Forecast Time range")
	}
	return nil
}

func validateLive(facts Facts) error {
	if facts.Actual.Start == nil || facts.Actual.Start.IsZero() {
		return stateError(facts, "Actual Start")
	}
	if facts.Actual.End != nil {
		return stateError(facts, "Actual End")
	}
	if facts.RunDuration <= 0 {
		return stateError(facts, "Run Snapshot duration")
	}
	return nil
}

func validateEnded(facts Facts) error {
	if facts.Actual.Start == nil || facts.Actual.Start.IsZero() {
		return stateError(facts, "Actual Start")
	}
	if facts.Actual.End == nil || facts.Actual.End.IsZero() {
		return stateError(facts, "Actual End")
	}
	if facts.Actual.End.Before(*facts.Actual.Start) {
		return stateError(facts, "Actual Time range")
	}
	if facts.RunDuration <= 0 {
		return stateError(facts, "Run Snapshot duration")
	}
	return nil
}

func normalizeActual(actual, communicated time.Time, runDuration time.Duration) time.Time {
	if communicated.IsZero() || runDuration <= 10*time.Minute {
		return actual
	}
	if difference := actual.Sub(communicated).Abs(); difference <= 2*time.Minute {
		return communicated
	}
	return actual
}

func optionalTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func stateError(facts Facts, fact string) error {
	return &StateError{Lifecycle: facts.Lifecycle, Fact: fact}
}

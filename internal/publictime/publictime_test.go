package publictime

import (
	"errors"
	"testing"
	"time"
)

func TestPresentSelectsLifecycleTimes(t *testing.T) {
	forecast := Range{
		Start: time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 2, 7, 11, 0, 0, 0, time.UTC),
	}
	actualStart := forecast.Start.Add(5 * time.Minute)
	actualEnd := forecast.End.Add(3 * time.Minute)
	communicatedStart := forecast.Start.Add(4 * time.Minute)
	communicatedEnd := forecast.End.Add(2 * time.Minute)

	tests := []struct {
		name      string
		facts     Facts
		wantStart Point
		wantEnd   Point
	}{
		{
			name:      "scheduled forecast",
			facts:     Facts{Lifecycle: Scheduled, Forecast: forecast},
			wantStart: Point{Time: forecast.Start, Label: LabelForecastStart},
			wantEnd:   Point{Time: forecast.End, Label: LabelForecastEnd},
		},
		{
			name: "live normalized actual start and forecast end",
			facts: Facts{
				Lifecycle: Live, Forecast: forecast,
				Actual:       OptionalRange{Start: &actualStart},
				Communicated: OptionalRange{Start: &communicatedStart},
				RunDuration:  time.Hour,
			},
			wantStart: Point{Time: communicatedStart, Label: LabelActualStart},
			wantEnd:   Point{Time: forecast.End, Label: LabelForecastEnd},
		},
		{
			name: "ended normalized actual range",
			facts: Facts{
				Lifecycle: Ended, Forecast: forecast,
				Actual:       OptionalRange{Start: &actualStart, End: &actualEnd},
				Communicated: OptionalRange{Start: &communicatedStart, End: &communicatedEnd},
				RunDuration:  time.Hour,
			},
			wantStart: Point{Time: communicatedStart, Label: LabelActualStart},
			wantEnd:   Point{Time: communicatedEnd, Label: LabelActualEnd},
		},
		{
			name: "canceled last forecast ignores partial actual",
			facts: Facts{
				Lifecycle: Canceled, Forecast: forecast,
				Actual: OptionalRange{Start: &actualStart},
			},
			wantStart: Point{Time: forecast.Start, Label: LabelLastForecastStart},
			wantEnd:   Point{Time: forecast.End, Label: LabelLastForecastEnd},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation, err := Present(test.facts)
			if err != nil {
				t.Fatalf("Present: %v", err)
			}
			if presentation.Start != test.wantStart || presentation.End != test.wantEnd {
				t.Fatalf(
					"Present = (%+v, %+v), want (%+v, %+v)",
					presentation.Start,
					presentation.End,
					test.wantStart,
					test.wantEnd,
				)
			}
		})
	}
}

func TestPresentAppliesPublicTimeTolerance(t *testing.T) {
	forecast := Range{
		Start: time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 2, 7, 11, 0, 0, 0, time.UTC),
	}
	communicated := forecast.Start.Add(5 * time.Minute)

	tests := []struct {
		name         string
		actual       time.Time
		communicated time.Time
		duration     time.Duration
		want         time.Time
	}{
		{
			name: "inclusive positive boundary", actual: communicated.Add(2 * time.Minute),
			communicated: communicated, duration: 11 * time.Minute, want: communicated,
		},
		{
			name: "inclusive negative boundary", actual: communicated.Add(-2 * time.Minute),
			communicated: communicated, duration: 11 * time.Minute, want: communicated,
		},
		{
			name: "outside boundary", actual: communicated.Add(2*time.Minute + time.Nanosecond),
			communicated: communicated, duration: 11 * time.Minute,
			want: communicated.Add(2*time.Minute + time.Nanosecond),
		},
		{
			name: "ten minute run", actual: communicated.Add(time.Minute),
			communicated: communicated, duration: 10 * time.Minute,
			want: communicated.Add(time.Minute),
		},
		{
			name: "missing communicated time", actual: communicated.Add(time.Minute),
			duration: 11 * time.Minute, want: communicated.Add(time.Minute),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := test.actual
			var communicated *time.Time
			if !test.communicated.IsZero() {
				captured := test.communicated
				communicated = &captured
			}
			presentation, err := Present(Facts{
				Lifecycle: Live, Forecast: forecast,
				Actual:       OptionalRange{Start: &actual},
				Communicated: OptionalRange{Start: communicated},
				RunDuration:  test.duration,
			})
			if err != nil {
				t.Fatalf("Present: %v", err)
			}
			if !presentation.Start.Time.Equal(test.want) {
				t.Fatalf("public start = %v, want %v", presentation.Start.Time, test.want)
			}
		})
	}
}

func TestPresentOffersImmutableBaselineAsWas(t *testing.T) {
	forecast := Range{
		Start: time.Date(2026, 2, 7, 10, 30, 0, 0, time.UTC),
		End:   time.Date(2026, 2, 7, 11, 30, 0, 0, time.UTC),
	}
	baseline := forecast.Start.Add(-30 * time.Minute)

	presentation, err := Present(Facts{
		Lifecycle: Scheduled, Forecast: forecast, BaselineStart: &baseline,
	})
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	if presentation.Was == nil ||
		presentation.Was.Label != LabelWas ||
		!presentation.Was.Time.Equal(baseline) {
		t.Fatalf("Was = %+v, want baseline %v", presentation.Was, baseline)
	}

	presentation, err = Present(Facts{
		Lifecycle: Scheduled, Forecast: forecast, BaselineStart: &forecast.Start,
	})
	if err != nil {
		t.Fatalf("Present equal baseline: %v", err)
	}
	if presentation.Was != nil {
		t.Fatalf("equal baseline Was = %+v, want nil", presentation.Was)
	}
}

func TestPresentRejectsImpossibleState(t *testing.T) {
	forecast := Range{
		Start: time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 2, 7, 11, 0, 0, 0, time.UTC),
	}
	actualStart := forecast.Start
	actualEnd := actualStart.Add(-time.Minute)

	tests := []struct {
		name  string
		facts Facts
	}{
		{name: "unknown lifecycle", facts: Facts{Lifecycle: "Paused", Forecast: forecast}},
		{name: "missing forecast start", facts: Facts{Lifecycle: Scheduled}},
		{
			name: "reversed forecast",
			facts: Facts{
				Lifecycle: Scheduled,
				Forecast:  Range{Start: forecast.End, End: forecast.Start},
			},
		},
		{name: "live missing actual start", facts: Facts{
			Lifecycle: Live, Forecast: forecast, RunDuration: time.Hour,
		}},
		{name: "live missing run duration", facts: Facts{
			Lifecycle: Live, Forecast: forecast, Actual: OptionalRange{Start: &actualStart},
		}},
		{name: "ended missing actual end", facts: Facts{
			Lifecycle: Ended, Forecast: forecast,
			Actual: OptionalRange{Start: &actualStart}, RunDuration: time.Hour,
		}},
		{name: "ended reversed actual range", facts: Facts{
			Lifecycle: Ended, Forecast: forecast,
			Actual: OptionalRange{Start: &actualStart, End: &actualEnd}, RunDuration: time.Hour,
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation, err := Present(test.facts)
			if !errors.Is(err, ErrImpossibleState) {
				t.Fatalf("Present error = %v, want %v", err, ErrImpossibleState)
			}
			if presentation != (Presentation{}) {
				t.Fatalf("rejected Presentation = %+v, want zero value", presentation)
			}
		})
	}
}

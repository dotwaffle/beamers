package store

import (
	"context"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/publicschedulebaselineentry"
	"github.com/dotwaffle/beamers/internal/publictime"
)

type publicTimeFactsParams struct {
	Session     *ent.Session
	Lifecycle   publictime.Lifecycle
	Forecast    publictime.Range
	ActualStart time.Time
	ActualEnd   *time.Time
	RunDuration time.Duration
}

func loadPublicTimeFacts(
	ctx context.Context,
	client *ent.Client,
	params publicTimeFactsParams,
) (publictime.Facts, error) {
	baseline, err := client.PublicScheduleBaselineEntry.Query().
		Where(publicschedulebaselineentry.SessionIDEQ(params.Session.ID)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return publictime.Facts{}, opaqueError("load Public Schedule Baseline entry", err)
	}
	var baselineStart *time.Time
	if err == nil {
		baselineStart = instantPointer(baseline.ForecastStart)
	}
	return publictime.Facts{
		Lifecycle: params.Lifecycle,
		Forecast:  params.Forecast,
		Actual: publictime.OptionalRange{
			Start: instantPointer(params.ActualStart),
			End:   params.ActualEnd,
		},
		Communicated: publictime.OptionalRange{
			Start: instantPointer(params.Session.CommunicatedStart),
			End:   instantPointer(params.Session.CommunicatedEnd),
		},
		RunDuration:   params.RunDuration,
		BaselineStart: baselineStart,
	}, nil
}

func instantPointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

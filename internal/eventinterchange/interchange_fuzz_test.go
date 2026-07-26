package eventinterchange

import (
	"context"
	"testing"
)

func FuzzDecodePlan(f *testing.F) {
	f.Add([]byte(`{
		"format":"beamers.event",
		"version":1,
		"event":{
			"name":"Revision 2026",
			"planned_start_date":"2026-08-21",
			"planned_end_date":"2026-08-23",
			"timezone":"Europe/Berlin",
			"event_locale":"de-DE",
			"event_day_boundary":"06:00",
			"entry_default_disposition":"Pending",
			"target_adjustment_presets_seconds":[-300,300,600]
		},
		"locations":[],
		"lanes":[],
		"tracks":[],
		"sessions":[]
	}`))
	f.Add([]byte(`{"format":"beamers.event","version":2}`))
	f.Add([]byte{0xff})

	f.Fuzz(func(_ *testing.T, document []byte) {
		_, _ = decodePlan(context.Background(), document)
	})
}

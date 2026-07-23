package displays

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/displayviews"
)

func TestDisplayPageRendersEveryConfiguredBuiltInRegion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		viewKey string
		standby bool
	}{
		{name: "Standby", standby: true},
		{name: "Event Overview", viewKey: displayviews.EventOverview},
		{name: "Location Signage", viewKey: displayviews.LocationSignage},
		{name: "Stage Timer", viewKey: displayviews.StageTimer},
		{name: "Competition Output", viewKey: displayviews.CompetitionOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			composition, err := displayviews.Compose(
				test.viewKey,
				test.standby,
				displayviews.DefaultConfiguration(),
			)
			if err != nil {
				t.Fatalf("compose test Display: %v", err)
			}
			snapshot := Snapshot{
				ProtocolVersion: "beamers.display.v1",
				AssetVersion:    "test-asset",
				ServerTime:      time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC),
				Display:         Display{Name: "Test Display"},
				EventName:       "Test Event",
				LocationName:    "Main Hall",
				ViewKey:         test.viewKey,
				Standby:         test.standby,
				Composition:     composition,
			}
			var rendered strings.Builder
			if err := DisplayPage(snapshot).Render(context.Background(), &rendered); err != nil {
				t.Fatalf("render Display page: %v", err)
			}
			for _, region := range composition.Layout.Regions {
				want := fmt.Sprintf(
					`data-region=%q data-widget=%q data-persistent="%t"`,
					region.Name,
					region.Widget,
					region.Persistent,
				)
				if !strings.Contains(rendered.String(), want) {
					t.Errorf("Display page missing configured Region %q: %s", want, rendered.String())
				}
			}
		})
	}
}

func TestDisplayNowNextExcludesCanceledSessions(t *testing.T) {
	t.Parallel()

	sessions := []Session{
		{Title: "Canceled", Lifecycle: "Canceled"},
		{Title: "Current", Lifecycle: "Live"},
		{Title: "Next", Lifecycle: "Scheduled"},
	}
	got := displayNowNext(sessions)
	if len(got) != 2 || got[0].Title != "Current" || got[1].Title != "Next" {
		t.Errorf("Now/Next Sessions = %+v, want Current and Next", got)
	}
	if len(sessions) != 3 {
		t.Errorf("Now/Next filtering changed the full Display rotation: %+v", sessions)
	}
}

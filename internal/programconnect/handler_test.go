package programconnect

import (
	"testing"

	"github.com/dotwaffle/beamers/internal/displays"
)

func TestConsumingDeliveryStateNarrowsToOperatorActions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state string
		want  string
	}{
		{name: "applied", state: displays.DeliveryApplied, want: displays.DeliveryApplied},
		{name: "offline", state: displays.DeliveryOffline, want: displays.DeliveryOffline},
		{name: "lagging", state: displays.DeliveryLagging, want: displays.DeliveryLagging},
		{name: "unstable", state: displays.DeliveryUnstable, want: displays.DeliveryLagging},
		{
			name:  "excessively skewed",
			state: displays.DeliveryExcessivelySkewed,
			want:  displays.DeliveryLagging,
		},
		{name: "unrecorded", state: "", want: displays.DeliveryLagging},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := consumingDeliveryState(testCase.state); got != testCase.want {
				t.Fatalf("consumingDeliveryState(%q) = %q, want %q", testCase.state, got, testCase.want)
			}
		})
	}
}

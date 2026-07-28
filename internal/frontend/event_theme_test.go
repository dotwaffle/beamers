package frontend

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/events"
)

func TestPublicEventLoadsItsResolvedTheme(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := PublicEvent(
		events.PublicEvent{ID: 41, Name: "Revision", EventLocale: "en-GB"},
		"csrf",
		true,
	).Render(t.Context(), &output)
	if err != nil {
		t.Fatalf("render public Event: %v", err)
	}
	for _, want := range []string{
		`href="/assets/events/41/theme.css"`,
		`data-reduced-effects="true"`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("public Event missing %q: %s", want, output.String())
		}
	}
}

package themes

import (
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

func TestRestoredActiveThemeMustRemainAccessible(t *testing.T) {
	config := themevalue.DefaultConfig()
	config.TextColor = config.BackgroundColor

	_, err := validateActiveRevision(Revision{ID: 1, Config: config})
	if err == nil || !strings.Contains(err.Error(), "accessibility") {
		t.Fatalf("validate restored active Theme = %v, want accessibility failure", err)
	}
}

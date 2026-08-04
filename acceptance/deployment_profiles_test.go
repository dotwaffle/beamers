package acceptance_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestShippedProfilesUseAlignedShutdownBudgets pins the compose and systemd
// profiles to the values ticket #198 requires: a 30-second application
// shutdown budget with 35 seconds of platform grace. Without that five
// second margin, the platform's kill deadline lands before
// internal/server/server.go finishes its drain and finalize phases, so the
// final replication sync can be SIGKILLed at the boundary.
func TestShippedProfilesUseAlignedShutdownBudgets(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		path       []string
		budgetExpr *regexp.Regexp
		graceExpr  *regexp.Regexp
	}{
		{
			name:       "compose",
			path:       []string{"..", "compose.yaml"},
			budgetExpr: regexp.MustCompile(`--shutdown-timeout=(\S+)`),
			graceExpr:  regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\S+)\s*$`),
		},
		{
			name:       "systemd",
			path:       []string{"..", "deploy", "systemd", "beamers.service"},
			budgetExpr: regexp.MustCompile(`--shutdown-timeout=(\S+)`),
			graceExpr:  regexp.MustCompile(`(?m)^TimeoutStopSec=(\S+)\s*$`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			content := readShippedProfile(t, test.path...)
			budget := matchOneShippedValue(t, test.budgetExpr, content, "shutdown budget")
			grace := matchOneShippedValue(t, test.graceExpr, content, "platform grace")

			if budget != "30s" {
				t.Errorf("%s application shutdown budget = %s, want 30s", test.name, budget)
			}
			if grace != "35s" {
				t.Errorf("%s platform grace period = %s, want 35s", test.name, grace)
			}
		})
	}
}

// TestSystemdUnitDoesNotSilentlySkipStartOnMissingDatabase pins the removal
// of ConditionPathExists on the database file. That condition made a
// missing database a silent no-op start (systemd reports the unit inactive,
// not failed) instead of letting serve enter its documented local recovery
// mode where /readyz and diagnostics report the problem loudly.
func TestSystemdUnitDoesNotSilentlySkipStartOnMissingDatabase(t *testing.T) {
	t.Parallel()

	unit := readShippedProfile(t, "..", "deploy", "systemd", "beamers.service")
	if strings.Contains(unit, "ConditionPathExists=") &&
		strings.Contains(unit, "beamers.db") {
		t.Error("beamers.service still gates startup on the database file existing; " +
			"remove the condition so serve can enter recovery mode instead of " +
			"silently skipping start")
	}
}

func readShippedProfile(t *testing.T, elem ...string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(elem...))
	if err != nil {
		t.Fatalf("read shipped profile %s: %v", filepath.Join(elem...), err)
	}
	return string(content)
}

func matchOneShippedValue(
	t *testing.T,
	expr *regexp.Regexp,
	content string,
	what string,
) string {
	t.Helper()

	match := expr.FindStringSubmatch(content)
	if match == nil {
		t.Fatalf("did not find %s in shipped profile", what)
	}
	return match[1]
}

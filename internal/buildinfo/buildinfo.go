// Package buildinfo identifies the running Beamers executable.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// version may be set at link time for release builds.
var version string

// Version returns a stable identifier for the running executable.
func Version() string {
	if version != "" {
		return version
	}
	information, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	var revision string
	modified := false
	for _, setting := range information.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision != "" {
		revision = revision[:min(len(revision), 12)]
		if modified {
			revision += "-modified"
		}
		return revision
	}
	if information.Main.Version != "" &&
		information.Main.Version != "(devel)" &&
		!strings.Contains(information.Main.Version, "+dirty") {
		return information.Main.Version
	}
	return "development"
}

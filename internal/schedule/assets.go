package schedule

import "embed"

//go:embed schedule.css
var assets embed.FS

// Stylesheet returns the embedded public Schedule stylesheet.
func Stylesheet() ([]byte, error) {
	return assets.ReadFile("schedule.css")
}

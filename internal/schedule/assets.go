package schedule

import "embed"

//go:embed schedule.css schedule.js
var assets embed.FS

// Stylesheet returns the embedded public Schedule stylesheet.
func Stylesheet() ([]byte, error) {
	return assets.ReadFile("schedule.css")
}

// Script returns the embedded public Schedule behavior.
func Script() ([]byte, error) {
	return assets.ReadFile("schedule.js")
}

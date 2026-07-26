// Package frontend owns the shared browser shell and its embedded assets.
package frontend

import (
	"embed"
	"errors"
)

// StylesheetPath, HTMXPath, and SSEPath are embedded browser asset routes.
const (
	StylesheetPath = "/assets/frontend.css"
	HTMXPath       = "/assets/htmx-2.0.10.min.js"
	SSEPath        = "/assets/htmx-ext-sse-2.2.4.min.js"
)

//go:embed frontend.css vendor/*.js
var assets embed.FS

// Asset returns one embedded Frontend asset.
func Asset(path string) ([]byte, error) {
	var name string
	switch path {
	case StylesheetPath:
		name = "frontend.css"
	case HTMXPath:
		name = "vendor/htmx-2.0.10.min.js"
	case SSEPath:
		name = "vendor/htmx-ext-sse-2.2.4.min.js"
	default:
		return nil, errors.New("unknown Frontend asset")
	}
	return assets.ReadFile(name)
}

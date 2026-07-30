// Package frontend owns the shared browser shell and its embedded assets.
package frontend

import (
	"bytes"
	"embed"
	"errors"
	"strconv"
	"sync"

	"github.com/dotwaffle/beamers/internal/fontassets"
)

// EventThemePath returns one Event's resolved controlled stylesheet route.
func EventThemePath(eventID int) string {
	return "/assets/events/" + strconv.Itoa(eventID) + "/theme.css"
}

// Embedded browser asset routes.
const (
	StylesheetPath        = "/assets/frontend.css"
	InstallationThemePath = "/assets/installation-theme.css"
	ChakraRegularPath     = "/assets/chakra-petch-regular.ttf"
	ChakraBoldPath        = "/assets/chakra-petch-bold.ttf"
	OpenSansPath          = "/assets/open-sans.ttf"
	HTMXPath              = "/assets/htmx-2.0.10.min.js"
	SSEPath               = "/assets/htmx-ext-sse-2.2.4.min.js"
	EventTimePath         = "/assets/event-time.js"
	WebAuthnPath          = "/assets/webauthn-v1.js"
)

//go:embed css/*.css vendor/*.js
var assets embed.FS

// stylesheetParts is the cascade order of the base stylesheet. It is listed
// explicitly rather than globbed so the order stays reviewable.
var stylesheetParts = []string{
	"css/00-fonts.css",
	"css/10-tokens.css",
	"css/20-reset.css",
	"css/30-layout.css",
	"css/40-components.css",
	"css/60-effects.css",
	"css/99-forced.css",
}

// stylesheet concatenates the base stylesheet once. Handlers read it at route
// registration, so the cost is paid before the first request.
var stylesheet = sync.OnceValues(func() ([]byte, error) {
	var combined bytes.Buffer
	for _, part := range stylesheetParts {
		content, err := assets.ReadFile(part)
		if err != nil {
			return nil, err
		}
		if combined.Len() > 0 {
			combined.WriteString("\n")
		}
		combined.Write(content)
	}
	return combined.Bytes(), nil
})

// Asset returns one embedded Frontend asset.
func Asset(path string) ([]byte, error) {
	var name string
	switch path {
	case StylesheetPath:
		return stylesheet()
	case ChakraRegularPath:
		return fontassets.Asset(fontassets.ChakraPetchRegular)
	case ChakraBoldPath:
		return fontassets.Asset(fontassets.ChakraPetchBold)
	case OpenSansPath:
		return fontassets.Asset(fontassets.OpenSans)
	case HTMXPath:
		name = "vendor/htmx-2.0.10.min.js"
	case SSEPath:
		name = "vendor/htmx-ext-sse-2.2.4.min.js"
	case EventTimePath:
		name = "vendor/event-time.js"
	case WebAuthnPath:
		name = "vendor/webauthn-v1.js"
	default:
		return nil, errors.New("unknown Frontend asset")
	}
	return assets.ReadFile(name)
}

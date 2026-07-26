// Package frontend owns the shared browser shell and its embedded assets.
package frontend

import (
	"embed"
	"errors"
)

// Embedded browser asset routes.
const (
	StylesheetPath    = "/assets/frontend.css"
	ChakraRegularPath = "/assets/chakra-petch-regular.ttf"
	ChakraBoldPath    = "/assets/chakra-petch-bold.ttf"
	OpenSansPath      = "/assets/open-sans.ttf"
	HTMXPath          = "/assets/htmx-2.0.10.min.js"
	SSEPath           = "/assets/htmx-ext-sse-2.2.4.min.js"
)

//go:embed frontend.css vendor/*.js vendor/fonts/*
var assets embed.FS

// Asset returns one embedded Frontend asset.
func Asset(path string) ([]byte, error) {
	var name string
	switch path {
	case StylesheetPath:
		name = "frontend.css"
	case ChakraRegularPath:
		name = "vendor/fonts/ChakraPetch-Regular.ttf"
	case ChakraBoldPath:
		name = "vendor/fonts/ChakraPetch-Bold.ttf"
	case OpenSansPath:
		name = "vendor/fonts/OpenSans.ttf"
	case HTMXPath:
		name = "vendor/htmx-2.0.10.min.js"
	case SSEPath:
		name = "vendor/htmx-ext-sse-2.2.4.min.js"
	default:
		return nil, errors.New("unknown Frontend asset")
	}
	return assets.ReadFile(name)
}

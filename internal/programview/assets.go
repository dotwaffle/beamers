// Package programview renders the authenticated Competition Program control View.
package programview

import _ "embed"

var (
	//go:embed control.js
	clientJavaScript []byte
	//go:embed control.css
	stylesheet []byte
)

// ClientJavaScript returns the immutable browser controller.
func ClientJavaScript() string { return string(clientJavaScript) }

// Stylesheet returns the immutable control View styles.
func Stylesheet() string { return string(stylesheet) }

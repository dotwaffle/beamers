package displays

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"

	"github.com/dotwaffle/beamers/internal/fontassets"
)

// ClientJavaScript refreshes a Display only after a revisioned invalidation.
//
//go:embed client.js
var ClientJavaScript []byte

// Stylesheet carries every Display presentation rule. It is a served asset
// rather than an inline block so a kiosk caches it between frames, and it shares
// the client's asset version so styles can never disagree with the markup that
// relies on them.
//
//go:embed display.css
var Stylesheet []byte

// assetDigest covers every Display asset the renderer depends on. ADR 0048
// requires one version across the whole set: a stylesheet change has to
// invalidate a running Display exactly as a page-code change does, which is how
// a presentation fix reaches a kiosk that has been up for days.
var assetDigest = func() string {
	digest := sha256.New()
	digest.Write(ClientJavaScript)
	digest.Write(Stylesheet)
	digest.Write(fontassets.Bundled(fontassets.ChakraPetchRegular))
	digest.Write(fontassets.Bundled(fontassets.OpenSans))
	return hex.EncodeToString(digest.Sum(nil))
}()

func assetPath(name string) string {
	return "/display/assets/" + assetDigest + "/" + name
}

// ClientJavaScriptPath returns the immutable URL for the embedded Display client.
func ClientJavaScriptPath() string {
	return assetPath("client.js")
}

// StylesheetPath returns the immutable URL for the embedded Display stylesheet.
func StylesheetPath() string {
	return assetPath("display.css")
}

// ChakraPetchRegularPath returns the immutable URL for the Display heading font.
func ChakraPetchRegularPath() string {
	return assetPath(fontassets.ChakraPetchRegular)
}

// OpenSansPath returns the immutable URL for the Display body font.
func OpenSansPath() string {
	return assetPath(fontassets.OpenSans)
}

// AssetVersion identifies the exact embedded Display asset set.
func AssetVersion() string {
	return assetDigest
}

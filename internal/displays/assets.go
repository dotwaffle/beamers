package displays

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// ClientJavaScript refreshes a Display only after a revisioned invalidation.
//
//go:embed client.js
var ClientJavaScript []byte

var clientJavaScriptDigest = func() string {
	digest := sha256.Sum256(ClientJavaScript)
	return hex.EncodeToString(digest[:])
}()

// ClientJavaScriptPath returns the immutable URL for the embedded Display client.
func ClientJavaScriptPath() string {
	return "/display/assets/" + clientJavaScriptDigest + "/client.js"
}

// AssetVersion identifies the exact embedded Display client build.
func AssetVersion() string {
	return clientJavaScriptDigest
}

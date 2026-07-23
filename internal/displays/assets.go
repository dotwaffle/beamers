package displays

import _ "embed"

// ClientJavaScript refreshes a Display only after a revisioned invalidation.
//
//go:embed client.js
var ClientJavaScript []byte

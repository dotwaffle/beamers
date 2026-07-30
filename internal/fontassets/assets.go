// Package fontassets owns the bundled browser fonts shared by Frontend and
// Display surfaces.
package fontassets

import (
	"bytes"
	_ "embed"
	"errors"
)

// Bundled font asset names.
const (
	ChakraPetchRegular = "chakra-petch-regular.ttf"
	ChakraPetchBold    = "chakra-petch-bold.ttf"
	OpenSans           = "open-sans.ttf"
)

var (
	//go:embed vendor/ChakraPetch-Regular.ttf
	chakraPetchRegular []byte
	//go:embed vendor/ChakraPetch-Bold.ttf
	chakraPetchBold []byte
	//go:embed vendor/OpenSans.ttf
	openSans []byte
)

// Bundled returns one known embedded font, or nil for an unknown name.
//
// Callers serving untrusted names should use Asset so an unknown name remains
// an explicit error. Callers enumerating the constants above may use Bundled
// when they need the bytes during immutable asset construction.
func Bundled(name string) []byte {
	return bytes.Clone(bundled(name))
}

func bundled(name string) []byte {
	switch name {
	case ChakraPetchRegular:
		return chakraPetchRegular
	case ChakraPetchBold:
		return chakraPetchBold
	case OpenSans:
		return openSans
	default:
		return nil
	}
}

// Asset returns one bundled font.
func Asset(name string) ([]byte, error) {
	asset := Bundled(name)
	if asset == nil {
		return nil, errors.New("unknown font asset")
	}
	return asset, nil
}

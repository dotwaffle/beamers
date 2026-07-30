package fontassets

import (
	"bytes"
	"testing"
)

func TestBundledFontsAreEmbedded(t *testing.T) {
	t.Parallel()

	for _, name := range []string{ChakraPetchRegular, ChakraPetchBold, OpenSans} {
		asset, err := Asset(name)
		if err != nil || len(asset) == 0 {
			t.Errorf("embedded font %q = %d bytes, error %v", name, len(asset), err)
		}
	}
	if _, err := Asset("unknown.ttf"); err == nil {
		t.Error("unknown font asset was accepted")
	}
}

func TestBundledFontsCannotBeMutatedByCallers(t *testing.T) {
	t.Parallel()

	first := Bundled(OpenSans)
	original := bytes.Clone(first)
	first[0] ^= 0xff

	if got := Bundled(OpenSans); !bytes.Equal(got, original) {
		t.Error("mutating returned font bytes changed the embedded asset")
	}
}

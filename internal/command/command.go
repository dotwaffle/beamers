// Package command validates command identity and hashes canonical payloads.
package command

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalidID means a command has no safe stable identity.
	ErrInvalidID = errors.New("command_id must be 1 to 200 visible characters")
)

// ValidateID checks a caller-supplied stable command identity.
func ValidateID(id string) error {
	if id == "" || utf8.RuneCountInString(id) > 200 || !utf8.ValidString(id) {
		return ErrInvalidID
	}
	for _, character := range id {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return ErrInvalidID
		}
	}
	return nil
}

// PayloadHash creates an unambiguous digest of canonical command values.
func PayloadHash(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

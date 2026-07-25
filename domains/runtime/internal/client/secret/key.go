// Package secret owns reverse user-runtime API token generation and format checks.
package secret

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	keyPrefix      = "mrk_"
	keyRandomBytes = 32
	keyHexChars    = keyRandomBytes * 2
)

var errInvalidKey = errors.New("invalid runtime key")

// NewKey generates an owner-readable Remote Runtime API token.
func NewKey() (string, error) {
	raw := make([]byte, keyRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return keyPrefix + hex.EncodeToString(raw), nil
}

// ValidateKeyFormat reports whether key matches the Remote Runtime token shape.
func ValidateKeyFormat(key string) error {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, keyPrefix) {
		return errInvalidKey
	}
	suffix := strings.TrimPrefix(key, keyPrefix)
	if len(suffix) != keyHexChars {
		return errInvalidKey
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		return errInvalidKey
	}
	return nil
}

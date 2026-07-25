package client

import "errors"

// ErrInvalidKey is returned when a Runtime key is missing or malformed.
var ErrInvalidKey = errors.New("invalid runtime key")

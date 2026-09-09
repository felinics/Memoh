// Package adapter defines error categories shared by audio provider adapters.
package adapter

import "errors"

// Adapters preserve these identities for transport-boundary error mapping.
// Detailed causes remain private and must not be used as client-facing messages.
var (
	ErrInvalidInput    = errors.New("invalid transcription input or configuration")
	ErrAudioTooLarge   = errors.New("transcription audio exceeds the provider limit")
	ErrRequestRejected = errors.New("transcription provider rejected the request")
	ErrRateLimited     = errors.New("transcription provider rate limited the request")
	ErrUnavailable     = errors.New("transcription provider is unavailable")
)

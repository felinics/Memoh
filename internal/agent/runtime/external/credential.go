package external

import (
	"errors"

	"github.com/felinics/memoh/internal/agentcredential"
)

// CredentialError reports an Agent credential the user must fix (missing, of
// the wrong kind, revoked, or undecryptable) as a FailureCredential. Any other
// error is returned unchanged.
func CredentialError(err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrNotFound),
		errors.Is(err, agentcredential.ErrIncompatible),
		errors.Is(err, agentcredential.ErrRevoked),
		errors.Is(err, agentcredential.ErrEncryptionUnavailable):
		return &Failure{Kind: FailureCredential, Err: err}
	default:
		return err
	}
}

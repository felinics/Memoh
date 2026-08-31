package external

import (
	"errors"

	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
)

func CredentialError(err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrNotFound):
		return apperror.Wrap(apperror.CodeAgentCredentialNotFound, err, nil)
	case errors.Is(err, agentcredential.ErrIncompatible):
		return apperror.Wrap(apperror.CodeAgentCredentialIncompatible, err, nil)
	case errors.Is(err, agentcredential.ErrRevoked):
		return apperror.Wrap(apperror.CodeAgentCredentialRevoked, err, nil)
	case errors.Is(err, agentcredential.ErrEncryptionUnavailable):
		return apperror.Wrap(apperror.CodeAgentCredentialEncryptionUnavailable, err, nil)
	default:
		return err
	}
}

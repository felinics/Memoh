package apps

import (
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/supermarket"
)

// RegistryError translates an error the Supermarket installer reported into
// its public code, keeping err as the cause. Any other error is returned
// unchanged.
func RegistryError(err error) error {
	var code apperror.Code
	switch {
	case errors.Is(err, supermarket.ErrAppNotFound):
		code = apperror.CodeRegistryAppNotFound
	case errors.Is(err, supermarket.ErrRegistryUnavailable):
		code = apperror.CodeRegistryUnavailable
	case errors.Is(err, supermarket.ErrAppInvalid):
		code = apperror.CodeRegistryAppInvalid
	case errors.Is(err, supermarket.ErrInstallFailed):
		code = apperror.CodeRegistryAppInstallFailed
	default:
		return err
	}
	return apperror.Wrap(code, err, nil)
}

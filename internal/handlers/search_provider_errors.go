package handlers

import (
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/searchproviders"
)

// searchProviderError translates a search provider failure the client can act
// on into its public error. Any other error is returned unchanged.
func searchProviderError(err error) error {
	switch {
	case errors.Is(err, searchproviders.ErrInvalidProvider):
		return apperror.Wrap(apperror.CodeSearchProviderInvalidProvider, err, nil)
	case errors.Is(err, searchproviders.ErrTypeConflict):
		return apperror.Wrap(apperror.CodeSearchProviderTypeConflict, err, nil)
	case errors.Is(err, searchproviders.ErrNameTaken):
		return apperror.Wrap(apperror.CodeProviderNameTaken, err, nil)
	default:
		return err
	}
}

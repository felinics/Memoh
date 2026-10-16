package handlers

import (
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/providertemplates"
)

// providerError translates a provider failure the client can act on into its
// public error. Any other error is returned unchanged.
func providerError(err error) error {
	if errors.Is(err, providers.ErrNameTaken) {
		return apperror.Wrap(apperror.CodeProviderNameTaken, err, nil)
	}
	return err
}

// providerTemplateError translates a failure of the provider template use
// cases, reading templates and creating a provider from one, into its public
// error. A failure that has a code after providerError keeps it; any other
// failure is provider_template.operation_failed.
func providerTemplateError(err error) error {
	if err == nil {
		return nil
	}
	if translated := providerError(err); apperror.CodeOf(translated) != "" {
		return translated
	}
	return apperror.Wrap(providerTemplateCode(err), err, nil)
}

func providerTemplateCode(err error) apperror.Code {
	switch {
	case errors.Is(err, providertemplates.ErrNotFound):
		return apperror.CodeProviderTemplateNotFound
	case errors.Is(err, providertemplates.ErrDomainInvalid):
		return apperror.CodeProviderTemplateDomainInvalid
	case errors.Is(err, providertemplates.ErrDomainMismatch):
		return apperror.CodeProviderTemplateDomainMismatch
	default:
		return apperror.CodeProviderTemplateOperationFailed
	}
}

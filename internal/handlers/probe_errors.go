package handlers

import (
	"errors"
	"net/url"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
)

// probeError translates why a provider or model connection test did not pass
// into its public error. Both test endpoints use it. A failure that no
// catalog code describes is returned unchanged; the client then shows its
// generic "test failed" copy.
//
// credentialsRejected is whether the service's verdict blames the credentials
// (status auth_error). The code must not say more than the verdict: a model
// check that fails with 401 is an error, not auth_error, because some
// gateways answer 401 for an unknown model (#1042), so its code does not
// claim the credentials were rejected either.
func probeError(err error, credentialsRejected bool) error {
	code := probeCode(err, credentialsRejected)
	if code == "" {
		return err
	}
	return apperror.Wrap(code, err, nil)
}

func probeCode(err error, credentialsRejected bool) apperror.Code {
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		switch sdk.KindOf(err) {
		case sdk.KindAuthentication:
			if !credentialsRejected {
				return apperror.CodeAgentProviderRequestRejected
			}
			return apperror.CodeAgentProviderAuthFailed
		case sdk.KindPermissionDenied:
			return apperror.CodeAgentProviderPermissionDenied
		case sdk.KindQuotaExhausted:
			return apperror.CodeAgentProviderQuotaExhausted
		case sdk.KindRateLimited:
			return apperror.CodeAgentProviderRateLimited
		case sdk.KindServerError:
			return apperror.CodeAgentProviderOverloaded
		default:
			return apperror.CodeAgentProviderRequestRejected
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return apperror.CodeAgentProviderUnreachable
	}
	return ""
}

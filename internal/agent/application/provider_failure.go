package application

import (
	"errors"
	"net/url"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
)

// providerFailureCode names the provider condition a run failure reports, or
// returns an empty code when the failure does not identify one. modelCall
// reports whether err is the failure of the model call itself.
//
// A provider that answered is read from the *sdk.APIError in the chain: its
// Kind names the condition, and an answer the SDK did not classify is a
// refusal the user has to resolve in the model settings. An error event inside
// a stream carries no HTTP status, so an unclassified one names nothing. A
// model call without an APIError whose chain holds a *url.Error is a request
// net/http got no response to: the provider could not be reached. The same
// error from the runtime's own work, such as an approval handler's request,
// names no provider.
func providerFailureCode(err error, modelCall bool) apperror.Code {
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Kind {
		case sdk.KindAuthentication:
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
			if apiErr.StatusCode >= 400 {
				return apperror.CodeAgentProviderRequestRejected
			}
			return ""
		}
	}
	var urlErr *url.Error
	if modelCall && errors.As(err, &urlErr) {
		return apperror.CodeAgentProviderUnreachable
	}
	return ""
}

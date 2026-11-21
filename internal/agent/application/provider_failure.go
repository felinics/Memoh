package application

import (
	"errors"
	"net/url"
	"slices"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/models"
)

func providerFailureArgs(err error) map[string]string {
	summary := models.UpstreamErrorSummary(err)
	if summary == "" {
		return nil
	}
	return map[string]string{apperror.ProviderErrorArgUpstream: summary}
}

func failureArgsForCode(code apperror.Code, cause error) map[string]string {
	if code == "" || cause == nil {
		return nil
	}
	public := ExternalAgentError(cause)
	if apperror.CodeOf(public) == code {
		if args := apperror.ArgsOf(public); len(args) > 0 {
			return args
		}
	}
	definition, ok := apperror.Lookup(code)
	if !ok || !slices.Contains(definition.AllowedArgs, apperror.ProviderErrorArgUpstream) {
		return nil
	}
	raw := cause
	if appErr, ok := apperror.As(cause); ok {
		raw = appErr.Cause()
	}
	return providerFailureArgs(raw)
}

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

// budgetFailureCode names a context the budget could not fit. When the
// compaction meant to fit it failed because the provider refused the account,
// for its balance, key or access, that refusal is named instead: no shorter
// request, new conversation or larger model gets past it, and the user has to
// go change something at the provider. Any other summarizer failure is still
// the budget's.
func budgetFailureCode(cause error) apperror.Code {
	switch code := providerFailureCode(cause, false); code {
	case apperror.CodeAgentProviderQuotaExhausted, apperror.CodeAgentProviderAuthFailed, apperror.CodeAgentProviderPermissionDenied:
		return code
	}
	return apperror.CodeContextBudgetUnsatisfied
}

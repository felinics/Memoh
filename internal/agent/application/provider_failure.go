package application

import (
	"regexp"
	"strings"

	"github.com/felinics/memoh/internal/apperror"
)

// Provider stream failures reach the application layer as the provider's own
// text: the runtime forwards `StreamEvent.Error`, and the native runtime's
// retry decision already reads the same text (`internal/agent/runtime/native/
// retry.go`). The patterns below name the four conditions a user can act on.
// Everything else keeps the generic interrupted response.
var (
	providerAuthPattern = regexp.MustCompile(
		`(?i)api error 40[13]\b|invalid[ _-]api[ _-]key|invalid_request_error: incorrect api key|authentication_error|unauthorized|permission_denied`,
	)
	providerQuotaPattern = regexp.MustCompile(
		`(?i)api error 402\b|insufficient[ _-](balance|quota|credit|funds)|exceeded your current quota|billing_(hard_limit|not_active)|payment required`,
	)
	providerRateLimitPattern = regexp.MustCompile(
		`(?i)(^|[^0-9])429($|[^0-9])|rate[ _-]?limit|too many requests|usage limit reached`,
	)
	providerOverloadPattern = regexp.MustCompile(
		`(?i)server_is_overloaded|overloaded_error|\boverloaded\b|api error 50[0234]\b|service unavailable|temporarily unavailable`,
	)
)

// providerFailureCode names the provider condition a failure text describes, or
// returns an empty code when the text does not identify one. Order resolves the
// overlaps in provider wording: an exhausted quota is often reported alongside
// a rate limit ("exceeded your current quota" arrives as a 429), and a rejected
// key is reported alongside both, so the more specific cause is matched first.
func providerFailureCode(detail string) apperror.Code {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	switch {
	case providerAuthPattern.MatchString(detail):
		return apperror.CodeAgentProviderAuthFailed
	case providerQuotaPattern.MatchString(detail):
		return apperror.CodeAgentProviderQuotaExhausted
	case providerRateLimitPattern.MatchString(detail):
		return apperror.CodeAgentProviderRateLimited
	case providerOverloadPattern.MatchString(detail):
		return apperror.CodeAgentProviderOverloaded
	default:
		return ""
	}
}

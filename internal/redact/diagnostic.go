package redact

import "regexp"

var (
	// Credentials that routinely end up verbatim in runtime error text: a
	// registry or provider URL carrying userinfo, and secret-bearing query
	// parameters.
	diagnosticURLUserinfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)([^/\s:@]+):([^/\s@]+)@`)
	diagnosticSecretParamPattern = regexp.MustCompile(`(?i)\b(token|password|passwd|pwd|secret|api_key|access_token)=([^&\s]+)`)
)

// Diagnostic masks the credentials in a message that is about to be persisted
// or shown to a user. Unlike Text it needs no registered secret: what a private
// registry reference or an upstream error quotes is not known up front, so the
// credentials are matched by shape instead.
func Diagnostic(message string) string {
	message = diagnosticURLUserinfoPattern.ReplaceAllString(message, "${1}***:***@")
	return diagnosticSecretParamPattern.ReplaceAllString(message, "${1}=***")
}

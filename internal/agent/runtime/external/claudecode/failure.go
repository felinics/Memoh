package claudecode

import (
	"log/slog"
	"slices"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// signInErrors are the categories Claude Code tags a failed API call with
// when the account itself has to be signed in again.
var signInErrors = []string{"authentication_failed", "oauth_org_not_allowed", "verification_required", "cloud_credential_error"}

// failedResultError is the error of a turn Claude Code ended as failed: what
// Claude Code said, shown as it said it. category is the last API error before
// the result; only a sign-in problem is told apart, since the user acts on it
// in Agent settings.
func failedResultError(message, category string) error {
	var attrs []slog.Attr
	if category != "" {
		attrs = append(attrs, slog.String("runtime_error_kind", category))
	}
	kind := external.FailureReported
	if slices.Contains(signInErrors, category) {
		kind = external.FailureAuthRequired
	}
	return external.Reported(kind, message, attrs...)
}

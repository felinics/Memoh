package codex

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
)

// unitFailureKinds names the failure the user can act on for the
// codexErrorInfo categories that have one. The rest (badRequest, sandboxError,
// sessionBudgetExceeded, threadRollbackFailed, other) leave the user nothing
// to do but retry, so they stay the plain failure.
var unitFailureKinds = map[string]external.FailureKind{
	// Codex also reports a plan without Codex access and an exhausted API
	// billing quota under this category.
	protocol.CodexErrorInfoUnitUsageLimitExceeded:    external.FailureUsageLimited,
	protocol.CodexErrorInfoUnitRateLimitExceeded:     external.FailureRateLimited,
	protocol.CodexErrorInfoUnitContextWindowExceeded: external.FailureContextWindowExceeded,
	// Codex reports only a ChatGPT sign-in it could not refresh as
	// unauthorized; a rejected API key arrives as other.
	protocol.CodexErrorInfoUnitUnauthorized:                external.FailureAuthRequired,
	protocol.CodexErrorInfoUnitServerOverloaded:            external.FailureOverloaded,
	protocol.CodexErrorInfoUnitInternalServerError:         external.FailureOverloaded,
	protocol.CodexErrorInfoUnitCyberPolicy:                 external.FailureRequestBlocked,
	protocol.CodexErrorInfoUnitMisalignmentPolicyViolation: external.FailureRequestBlocked,
}

// errorInfoSummary is what a codexErrorInfo says about a failure: its
// category, the upstream HTTP status when Codex forwarded one, and the failure
// kind the category stands for, zero when it has none.
type errorInfoSummary struct {
	category   string
	httpStatus *uint64
	kind       external.FailureKind
}

func summarizeErrorInfo(info *protocol.CodexErrorInfo) errorInfoSummary {
	switch {
	case info == nil:
		return errorInfoSummary{}
	case info.Unit != "":
		return errorInfoSummary{category: info.Unit, kind: unitFailureKinds[info.Unit]}
	case info.HTTPConnectionFailed != nil:
		return errorInfoSummary{category: "httpConnectionFailed", httpStatus: info.HTTPConnectionFailed.HTTPStatusCode, kind: external.FailureUpstreamUnreachable}
	case info.ResponseStreamConnectionFailed != nil:
		return errorInfoSummary{category: "responseStreamConnectionFailed", httpStatus: info.ResponseStreamConnectionFailed.HTTPStatusCode, kind: external.FailureUpstreamUnreachable}
	case info.ResponseStreamDisconnected != nil:
		return errorInfoSummary{category: "responseStreamDisconnected", httpStatus: info.ResponseStreamDisconnected.HTTPStatusCode, kind: external.FailureUpstreamUnreachable}
	case info.ResponseTooManyFailedAttempts != nil:
		// Codex gives up with the last status it saw: a 429 that is not a
		// usage limit is the model service throttling the account, anything
		// else is the service failing.
		status := info.ResponseTooManyFailedAttempts.HTTPStatusCode
		kind := external.FailureOverloaded
		if status != nil && *status == http.StatusTooManyRequests {
			kind = external.FailureRateLimited
		}
		return errorInfoSummary{category: "responseTooManyFailedAttempts", httpStatus: status, kind: kind}
	case info.ActiveTurnNotSteerable != nil:
		return errorInfoSummary{category: "activeTurnNotSteerable"}
	default:
		// A category this protocol snapshot does not know.
		return errorInfoSummary{category: errs.RedactURLs(truncateForLog(info.Raw()))}
	}
}

func (s errorInfoSummary) attrs() []slog.Attr {
	var attrs []slog.Attr
	if s.category != "" {
		// The key is not Codex's own: every runtime reports its category
		// under it, so one query covers them all.
		attrs = append(attrs, slog.String("runtime_error_kind", s.category))
	}
	if s.httpStatus != nil {
		attrs = append(attrs, slog.Uint64("upstream_http_status", *s.httpStatus))
	}
	return attrs
}

// turnErrorText is what Codex said about a turn error, for the log.
func turnErrorText(turnErr *protocol.TurnError) string {
	message := strings.TrimSpace(turnErr.Message)
	if turnErr.AdditionalDetails == nil {
		return message
	}
	details := strings.TrimSpace(*turnErr.AdditionalDetails)
	switch {
	case details == "":
		return message
	case message == "":
		return details
	default:
		return message + ": " + details
	}
}

// failedTurnError is the error of a turn Codex ended as failed. The category
// Codex gave stays on the error for the result record, and a category the
// user can act on makes it that Failure.
func failedTurnError(turnErr *protocol.TurnError) error {
	if turnErr == nil {
		return errs.NewDependency("codex turn failed")
	}
	summary := summarizeErrorInfo(turnErr.CodexErrorInfo)
	message := turnErrorText(turnErr)
	if message == "" {
		message = "codex turn failed"
	}
	err := errs.NewDependency(message, summary.attrs()...)
	if summary.kind == 0 {
		return err
	}
	return external.Fail(summary.kind, err)
}

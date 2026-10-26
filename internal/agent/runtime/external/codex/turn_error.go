package codex

import (
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
	"github.com/felinics/memoh/internal/errs"
)

// errorInfoSummary is what a codexErrorInfo says about a failure, for the
// log: its category and the upstream HTTP status when Codex forwarded one.
type errorInfoSummary struct {
	category   string
	httpStatus *uint64
}

func summarizeErrorInfo(info *protocol.CodexErrorInfo) errorInfoSummary {
	switch {
	case info == nil:
		return errorInfoSummary{}
	case info.Unit != "":
		return errorInfoSummary{category: info.Unit}
	case info.HTTPConnectionFailed != nil:
		return errorInfoSummary{category: "httpConnectionFailed", httpStatus: info.HTTPConnectionFailed.HTTPStatusCode}
	case info.ResponseStreamConnectionFailed != nil:
		return errorInfoSummary{category: "responseStreamConnectionFailed", httpStatus: info.ResponseStreamConnectionFailed.HTTPStatusCode}
	case info.ResponseStreamDisconnected != nil:
		return errorInfoSummary{category: "responseStreamDisconnected", httpStatus: info.ResponseStreamDisconnected.HTTPStatusCode}
	case info.ResponseTooManyFailedAttempts != nil:
		return errorInfoSummary{category: "responseTooManyFailedAttempts", httpStatus: info.ResponseTooManyFailedAttempts.HTTPStatusCode}
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

// turnErrorText is what Codex said about a turn error.
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

// failedTurnError is the error of a turn Codex ended as failed: what Codex
// said, shown as it said it. Only a sign-in Codex could not refresh is told
// apart, since the user acts on it in Agent settings.
func failedTurnError(turnErr *protocol.TurnError) error {
	if turnErr == nil {
		return external.Reported(external.FailureReported, "codex turn failed")
	}
	kind := external.FailureReported
	if turnErr.CodexErrorInfo != nil && turnErr.CodexErrorInfo.Unit == protocol.CodexErrorInfoUnitUnauthorized {
		kind = external.FailureAuthRequired
	}
	message := firstNonEmpty(turnErrorText(turnErr), "codex turn failed")
	return external.Reported(kind, message, summarizeErrorInfo(turnErr.CodexErrorInfo).attrs()...)
}

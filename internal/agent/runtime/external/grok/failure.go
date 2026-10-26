package grok

import (
	"context"
	"errors"
	"net/http"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
)

// authRequiredCode is the JSON-RPC error Grok answers with when it holds no
// usable credential.
const authRequiredCode = -32000

// serverFailure shapes a process acquisition failure. A failure the user can
// act on passes through so the application gives it its own code; anything
// else is the generic runtime-unavailable failure.
func serverFailure(err error) error {
	if errors.Is(err, external.ErrDependencyMissing) || errors.Is(err, external.ErrContainerWorkspaceRequired) || external.IsFailure(err) {
		return err
	}
	return external.Unavailable(err)
}

// turnFailure names what the user can do about a failed request to Grok.
// Grok reports a rejected model call as a JSON-RPC error carrying the HTTP
// status its API answered with. fallback is the kind of an error that says
// nothing more specific; zero returns such an error unchanged.
func turnFailure(err error, fallback external.FailureKind) error {
	var failure *external.Failure
	if errors.As(err, &failure) {
		return err
	}
	var reqErr *acp.RequestError
	if errors.As(err, &reqErr) {
		if reqErr.Code == authRequiredCode {
			return external.Fail(external.FailureAuthRequired, err)
		}
		if kind := apiFailureKind(reqErr.Data); kind != 0 {
			return external.Fail(kind, err)
		}
	}
	if fallback == 0 {
		return err
	}
	return external.Fail(fallback, err)
}

// resumeFailure decides what a failed session/resume means. It returns nil
// only when the session is gone for good and the caller may replace it with
// a new one; every other failure leaves the stored session in place for a
// later turn.
func resumeFailure(ctx context.Context, err error, replaceLost bool) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case !sessionGone(err):
		return turnFailure(err, external.FailureSessionResumeFailed)
	case !replaceLost:
		return external.ErrThreadUnavailable
	default:
		return nil
	}
}

// sessionGone reports that Grok no longer has the session, which it answers
// with FS_NOT_FOUND for the missing session file. Nothing else proves that:
// a broken connection, a missing credential and a failed model call all
// arrive as JSON-RPC errors too, and a retry may resume the session after any
// of them.
func sessionGone(err error) bool {
	if errors.Is(err, acpclient.ErrSessionResumeUnsupported) {
		return true
	}
	var answer *acp.RequestError
	if !errors.As(err, &answer) {
		return false
	}
	fields, _ := answer.Data.(map[string]any)
	return fields["code"] == "FS_NOT_FOUND"
}

func apiFailureKind(data any) external.FailureKind {
	fields, _ := data.(map[string]any)
	status, _ := fields["http_status"].(float64)
	message, _ := fields["message"].(string)
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden,
		// xAI answers a malformed or revoked key with 400, not 401.
		strings.Contains(message, "Incorrect API key"):
		return external.FailureAuthRequired
	case status == http.StatusTooManyRequests:
		return external.FailureRateLimited
	case status >= http.StatusInternalServerError:
		return external.FailureOverloaded
	default:
		return 0
	}
}

package grok

import (
	"context"
	"errors"
	"net/http"

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

// turnFailure shapes a failed request to Grok. An error Grok answered with
// is reported in Grok's words, told apart only when it is a sign-in problem:
// no usable credential, or an API that refused it. fallback is the kind of an
// error Grok did not answer; zero returns such an error unchanged.
func turnFailure(err error, fallback external.FailureKind) error {
	if external.IsFailure(err) {
		return err
	}
	if reqErr, text, ok := acpclient.AgentError(err); ok {
		kind := external.FailureReported
		if signInFailure(reqErr) {
			kind = external.FailureAuthRequired
		}
		return external.Reported(kind, text)
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
		// A later turn may resume the session, whatever Grok said this time.
		if reqErr, _, ok := acpclient.AgentError(err); ok && signInFailure(reqErr) {
			return external.Fail(external.FailureAuthRequired, err)
		}
		return external.Fail(external.FailureSessionResumeFailed, err)
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

// signInFailure reports an error that Grok's account has to be signed in
// again for: Grok holds no usable credential, or the API refused it.
func signInFailure(reqErr *acp.RequestError) bool {
	if reqErr.Code == authRequiredCode {
		return true
	}
	fields, _ := reqErr.Data.(map[string]any)
	status, _ := fields["http_status"].(float64)
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

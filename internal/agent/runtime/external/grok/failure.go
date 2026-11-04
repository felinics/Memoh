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

// turnFailure shapes a failed request to Grok: in Grok's words, told apart
// only when it is a sign-in problem (no usable credential, or an API that
// refused it).
func turnFailure(err error) error {
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
	return external.Upstream(err)
}

// resumeFailure shapes a failed session open. A session Grok no longer has
// is ErrThreadUnavailable: only a turn may replace it with a new one. Any
// other failure leaves the stored session in place for a later turn.
func resumeFailure(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, acpclient.ErrNativeSessionGone):
		return external.ErrThreadUnavailable
	}
	if reqErr, _, ok := acpclient.AgentError(err); ok && signInFailure(reqErr) {
		return external.Fail(external.FailureAuthRequired, err)
	}
	return turnFailure(err)
}

// sessionGone reports that Grok no longer has the session, which it answers
// with FS_NOT_FOUND for the missing session file. Nothing else proves that:
// a missing credential and a failed model call arrive as JSON-RPC errors
// too, and a retry may resume the session after any of them.
func sessionGone(err error) bool {
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

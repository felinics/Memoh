package external

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/errs"
)

// FailureKind names a runtime failure the user can act on. The application
// chooses the public error for each kind.
type FailureKind int

const (
	// FailureUnavailable: the runtime could not be started or reached.
	FailureUnavailable FailureKind = iota + 1
	// FailureAuthRequired: the runtime needs its account signed in or its
	// authentication configured.
	FailureAuthRequired
	// FailureSessionResumeFailed: the runtime could not resume the session's
	// native thread; a later turn may.
	FailureSessionResumeFailed
	// FailureGoalRequiresDefaultMode: a goal turn was asked for outside the
	// runtime's default mode.
	FailureGoalRequiresDefaultMode
	// FailureModeUnavailable: the runtime cannot apply the requested mode.
	FailureModeUnavailable
	// FailureControlFailed: a runtime control the turn needed failed.
	FailureControlFailed
	// FailureCredential: the Agent's credential cannot be used. Failure.Err
	// is the agentcredential error that says why.
	FailureCredential
	// FailureCredentialBusy: the credential cannot change while a turn runs
	// on it.
	FailureCredentialBusy
	// FailureReported: the agent failed the turn and said why; Message is
	// what it said.
	FailureReported
	// FailureRuntimeBusy: the previous native process must finish before restart.
	FailureRuntimeBusy
	// FailureForkLatestOnly: the requested fork anchor is no longer the latest.
	FailureForkLatestOnly
)

var failureText = map[FailureKind]string{ //nolint:gosec // G101 matches the credential kinds; these are failure texts.
	FailureUnavailable:             "external agent runtime is unavailable",
	FailureAuthRequired:            ErrAuthRequired.Error(),
	FailureSessionResumeFailed:     "runtime session could not be resumed",
	FailureGoalRequiresDefaultMode: "runtime goal requires the default mode",
	FailureModeUnavailable:         ErrModeUnavailable.Error(),
	FailureRuntimeBusy:             "external agent runtime is still in use",
	FailureForkLatestOnly:          "only the latest turn can be forked",
	FailureControlFailed:           "runtime control failed",
	FailureCredential:              "agent credential is unusable",
	FailureCredentialBusy:          "agent credential is in use by a running turn",
	FailureReported:                "external agent reported a failure",
}

// Failure is a runtime failure the user can act on. Like a public error it
// hides Err from errors.Is and errors.As: callers branch on Kind, and a
// cancellation or sentinel beneath it does not change how they treat it.
// Diagnostics still reach Err through Cause.
type Failure struct {
	Kind FailureKind
	Err  error
	// Message is what the agent itself said about the failure, made fit to
	// show and keep: users read it as they would in the agent's own client.
	Message string
}

func (f *Failure) Error() string {
	if f.Err != nil {
		return f.Err.Error()
	}
	return failureText[f.Kind]
}

// Cause returns the error the runtime failed with.
func (f *Failure) Cause() error { return f.Err }

// Unavailable reports that the runtime could not start or serve the turn.
func Unavailable(err error) error {
	return &Failure{Kind: FailureUnavailable, Err: err}
}

// Fail reports a failure of kind, caused by err when err is not nil.
func Fail(kind FailureKind, err error) error {
	return &Failure{Kind: kind, Err: err}
}

// maxMessageBytes bounds a reported message; an agent can attach a whole
// response body.
const maxMessageBytes = 2000

// Reported is a failure the agent reported in its own words: kind is
// FailureReported, or FailureAuthRequired when the agent's protocol marked it
// as a sign-in problem. URLs in the message lose their credentials and
// queries. attrs describe the failure to the result record.
func Reported(kind FailureKind, message string, attrs ...slog.Attr) error {
	message = strings.TrimSpace(message)
	public := errs.RedactURLs(message)
	if len(public) > maxMessageBytes {
		public = strings.ToValidUTF8(public[:maxMessageBytes], "") + "…"
	}
	return &Failure{Kind: kind, Err: errs.NewDependency(message, attrs...), Message: public}
}

// IsFailure reports whether err carries a Failure.
func IsFailure(err error) bool {
	var failure *Failure
	return errors.As(err, &failure)
}

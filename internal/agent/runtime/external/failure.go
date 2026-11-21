package external

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/errs"
)

// FailureKind names a runtime failure the user can act on. There are three
// sorts: what the agent or its process said, shown as it said it
// (FailureReported); a sign-in the agent's protocol marked as such
// (FailureAuthRequired); and Memoh's own rules refusing a request, which
// have no upstream words (the rest). The application chooses the public
// error for each kind.
type FailureKind int

const (
	// FailureReported: the agent or its process failed and said why; Message
	// is what it said.
	FailureReported FailureKind = iota + 1
	// FailureAuthRequired: the runtime needs its account signed in or its
	// authentication configured.
	FailureAuthRequired
	// FailureGoalRequiresDefaultMode: a goal turn was asked for outside the
	// runtime's default mode.
	FailureGoalRequiresDefaultMode
	// FailureModeUnavailable: the requested mode, model or reasoning effort
	// is not one the runtime offers.
	FailureModeUnavailable
	// FailureCredential: the Agent's credential cannot be used. Failure.Err
	// is the agentcredential error that says why.
	FailureCredential
	// FailureCredentialBusy: the credential cannot change while a turn runs
	// on it.
	FailureCredentialBusy
	// FailureRuntimeBusy: the conversation's session is still serving a turn.
	FailureRuntimeBusy
	// FailureForkLatestOnly: the requested fork anchor is no longer the latest.
	FailureForkLatestOnly
)

var failureText = map[FailureKind]string{ //nolint:gosec // G101 matches the credential kinds; these are failure texts.
	FailureReported:                "external agent reported a failure",
	FailureAuthRequired:            "runtime authentication is required",
	FailureGoalRequiresDefaultMode: "runtime goal requires the default mode",
	FailureModeUnavailable:         "runtime mode is unavailable",
	FailureCredential:              "agent credential is unusable",
	FailureCredentialBusy:          "agent credential is in use by a running turn",
	FailureRuntimeBusy:             "external agent runtime is still in use",
	FailureForkLatestOnly:          "only the latest turn can be forked",
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

// Upstream reports a failure that came from the agent or its process (a
// start that failed with its exit status and stderr, a refused resume, a
// control the agent rejected) in the words it failed with. A Failure, a
// missing workspace dependency, a workspace that is not a container and a
// cancellation pass through unchanged.
func Upstream(err error) error {
	switch {
	case err == nil, IsFailure(err), errors.Is(err, ErrDependencyMissing), errors.Is(err, ErrContainerWorkspaceRequired),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return &Failure{Kind: FailureReported, Err: err, Message: PublicMessage(err.Error())}
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
	return &Failure{Kind: kind, Err: errs.NewDependency(message, attrs...), Message: PublicMessage(message)}
}

// PublicMessage makes what an agent said fit to show and keep: URLs lose
// their credentials and queries, and a whole response body is cut short.
func PublicMessage(message string) string {
	public := errs.RedactURLs(strings.TrimSpace(message))
	if len(public) > maxMessageBytes {
		public = strings.ToValidUTF8(public[:maxMessageBytes], "") + "…"
	}
	return public
}

// IsFailure reports whether err carries a Failure.
func IsFailure(err error) bool {
	var failure *Failure
	return errors.As(err, &failure)
}

// IsFailureKind reports whether err carries a Failure of kind.
func IsFailureKind(err error, kind FailureKind) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Kind == kind
}

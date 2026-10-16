package external

import "errors"

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
	// FailureUsageLimited: the runtime's account has used up its usage
	// allowance; a later turn may run once it resets.
	FailureUsageLimited
	// FailureRateLimited: the runtime's model service is throttling the
	// account; a turn sent a moment later may run.
	FailureRateLimited
	// FailureContextWindowExceeded: the conversation no longer fits in the
	// model's context window; it has to be compacted or started over.
	FailureContextWindowExceeded
	// FailureOverloaded: the runtime's model service is failing or at
	// capacity; a later turn or another model may run.
	FailureOverloaded
	// FailureUpstreamUnreachable: the runtime could not reach its model
	// service, or lost the connection to it.
	FailureUpstreamUnreachable
	// FailureRequestBlocked: the model service's policy refused the request;
	// the same request will be refused again.
	FailureRequestBlocked
)

var failureText = map[FailureKind]string{ //nolint:gosec // G101 matches the credential kinds; these are failure texts.
	FailureUnavailable:             "external agent runtime is unavailable",
	FailureAuthRequired:            ErrAuthRequired.Error(),
	FailureSessionResumeFailed:     "runtime session could not be resumed",
	FailureGoalRequiresDefaultMode: "runtime goal requires the default mode",
	FailureModeUnavailable:         ErrModeUnavailable.Error(),
	FailureControlFailed:           "runtime control failed",
	FailureCredential:              "agent credential is unusable",
	FailureCredentialBusy:          "agent credential is in use by a running turn",
	FailureUsageLimited:            "external agent usage limit reached",
	FailureRateLimited:             "external agent rate limit reached",
	FailureContextWindowExceeded:   "external agent context window exceeded",
	FailureOverloaded:              "external agent model service is overloaded",
	FailureUpstreamUnreachable:     "external agent model service is unreachable",
	FailureRequestBlocked:          "external agent request was blocked by policy",
}

// Failure is a runtime failure the user can act on. Like a public error it
// hides Err from errors.Is and errors.As: callers branch on Kind, and a
// cancellation or sentinel beneath it does not change how they treat it.
// Diagnostics still reach Err through Cause.
type Failure struct {
	Kind FailureKind
	Err  error
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

// IsFailure reports whether err carries a Failure.
func IsFailure(err error) bool {
	var failure *Failure
	return errors.As(err, &failure)
}

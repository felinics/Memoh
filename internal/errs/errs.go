package errs

import (
	"context"
	stderrors "errors"
	"log/slog"
	"time"
)

type faultError struct {
	msg      string
	cause    error
	attrs    []slog.Attr
	stack    []Frame
	explicit bool
	panic    bool
}

func (e *faultError) Error() string {
	return render(e)
}

func (e *faultError) Unwrap() error {
	return e.cause
}

// New creates an internal error attributed to this process by default.
func New(msg string, attrs ...slog.Attr) error {
	return makeError(0, msg, nil, false, attrs...)
}

// NewWithDepth is New with the recorded stack starting depth frames further
// up. A helper that builds one kind of error uses it so the source names the
// helper's caller.
func NewWithDepth(depth int, msg string, attrs ...slog.Attr) error {
	return makeError(depth, msg, nil, false, attrs...)
}

// Wrap adds context to err and keeps the chain; errors.Is and errors.As still
// see err. It returns nil when err is nil.
func Wrap(err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(0, msg, err, false, attrs...)
}

// WrapWithDepth is Wrap with the recorded stack starting depth frames further
// up. A wrapping helper uses it so the source is the helper's caller rather
// than the helper; depth 0 is Wrap.
func WrapWithDepth(depth int, err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(depth, msg, err, false, attrs...)
}

// NewDependency creates an error attributed to a dependency.
func NewDependency(msg string, attrs ...slog.Attr) error {
	return makeError(0, msg, nil, true, attrs...)
}

// WrapDependency wraps err and attributes it to a dependency.
func WrapDependency(err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(0, msg, err, true, attrs...)
}

// WrapDependencyWithDepth is WrapDependency with depth as in WrapWithDepth.
func WrapDependencyWithDepth(depth int, err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(depth, msg, err, true, attrs...)
}

// makeError must be called directly by an exported constructor: the skip
// count below assumes exactly that frame layout.
func makeError(depth int, msg string, cause error, explicit bool, attrs ...slog.Attr) error {
	e := &faultError{msg: msg, cause: cause, attrs: append([]slog.Attr(nil), attrs...), explicit: explicit}
	if !hasStack(cause) {
		e.stack = captureStack(4+max(depth, 0), maxStackFrames)
	}
	return e
}

var errTimeoutCause = stderrors.New("errs: local timeout")

// WithTimeout creates a timeout this process sets for itself. When it expires
// the context's cause is not context.DeadlineExceeded, so Analyze does not
// attribute the failure to the caller.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, d, errTimeoutCause)
}

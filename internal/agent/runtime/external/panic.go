package external

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// PanicError is the failure a panic in a goroutine of a runtime becomes. The
// panic value is private: the application maps the error to its catch-all
// public code and never shows the text.
type PanicError struct {
	Where string
	Value any
}

func (e *PanicError) Error() string { return fmt.Sprintf("%s panicked: %v", e.Where, e.Value) }

// IsPanic reports whether err is, or wraps, a recovered panic.
func IsPanic(err error) bool {
	var target *PanicError
	return errors.As(err, &target)
}

// RecoveredPanic records a panic recovered in a goroutine a runtime started
// and returns the failure it becomes. Output from the CLI or its protocol is
// the likeliest source of a panic here, and a goroutine of its own is out of
// reach of every recover above it, so the process would go down with every
// session in it.
//
// The caller recovers, because recover only takes effect when the deferred
// function itself calls it, and then ends whatever the goroutine was serving
// through the runtime's existing close or error path.
func RecoveredPanic(ctx context.Context, logger *slog.Logger, where string, r any, attrs ...any) error {
	if logger != nil {
		logger.ErrorContext(ctx, "external runtime goroutine panicked",
			append([]any{
				slog.String("where", where),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())),
			}, attrs...)...)
	}
	return &PanicError{Where: where, Value: r}
}

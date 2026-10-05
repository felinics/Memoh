//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"log/slog"
	"runtime"
)

const maxStackFrames = 16

// Recovered turns a value from recover into an internal error with a stack.
// Call it directly in the deferred function. The stack starts where the panic
// happened. The panic value can hold user data, so it is not part of the
// error text; only a runtime error keeps its description.
func Recovered(v any) error {
	msg := "panic"
	if re, ok := v.(runtime.Error); ok {
		msg = "panic: " + re.Error()
	}
	stack := captureStack(3, maxStackFrames)
	for i, frame := range stack {
		if frame.Function == "runtime.gopanic" || frame.Function == "panic" {
			stack = stack[i+1:]
			break
		}
	}
	return &faultError{msg: msg, stack: stack, panic: true}
}

func captureStack(skip, limit int) []Frame {
	pcs := make([]uintptr, limit)
	n := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	out := make([]Frame, 0, n)
	for len(out) < limit {
		frame, more := frames.Next()
		if frame.Function == "" {
			break
		}
		out = append(out, Frame{Function: frame.Function, File: frame.File, Line: frame.Line})
		if !more {
			break
		}
	}
	return out
}

// hasStack follows the same traversal as Analyze, so an error wrapped under a
// public error that is only reachable through Cause() is not given a second
// stack.
func hasStack(err error) bool {
	found := false
	walk(err, false, false, func(n node) bool {
		if e, ok := n.err.(*faultError); ok && len(e.stack) > 0 {
			found = true
			return false
		}
		return true
	})
	return found
}

// Frame is a source frame where an error was produced.
type Frame struct {
	Function, File string
	Line           int
}

// LogValue encodes the frame as a log group.
func (f Frame) LogValue() slog.Value {
	return slog.GroupValue(slog.String("function", f.Function), slog.String("file", f.File), slog.Int("line", f.Line))
}

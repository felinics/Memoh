// Package logger builds the process's slog logger.
//
// The rules this package exists to enforce:
//
//   - A logger is constructed and passed, never reached for. There is no
//     package-level logger variable and no logging functions here. Code that
//     logs takes a *slog.Logger the way it takes any other dependency.
//   - The context carries request data, not the logger. slog's own API took
//     the opposite direction on purpose: Handler.Handle receives a context so
//     a handler can read values that are already there, which is what
//     correlation.Handler below does.
//   - Output keys are snake_case, including the correlation fields. Log
//     records are not OpenTelemetry attributes; renaming them to dotted form
//     would only collide with the underscore-flattening most log pipelines
//     apply on ingest.
//
// Nothing here writes to os.Stdout by itself. Init used to, which is why this
// package had no test that could look at its output.
package logger

import (
	"io"
	"log/slog"
	"strings"
)

// New builds a logger writing to w. format selects the encoding ("json" for
// JSON, anything else for text); level is one of debug, info, warn, error and
// falls back to info.
//
// The returned logger already carries the correlation handler, so any record
// logged with a context that has a request or trace identity picks those
// fields up without the call site repeating them.
func New(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var encoder slog.Handler
	if strings.ToLower(format) == "json" {
		encoder = slog.NewJSONHandler(w, opts)
	} else {
		encoder = slog.NewTextHandler(w, opts)
	}

	return slog.New(correlationHandler{inner: encoder})
}

// SetDefault points slog's package-level logger, and with it the standard
// log package, at l.
//
// This exists for output that is not ours: a dependency calling log.Printf,
// or slog.Default() from a library. Call it once during startup. It is not
// the way this codebase logs — application code holds a *slog.Logger.
func SetDefault(l *slog.Logger) {
	slog.SetDefault(l)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

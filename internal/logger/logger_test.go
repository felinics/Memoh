package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"testing/slogtest"

	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/logger"
)

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", line, err)
	}
	return payload
}

func TestNewReportsTheCallersMessageAsMsg(t *testing.T) {
	// The message belongs in msg. Putting a fixed string there and the real
	// message in an attribute makes every record look like the same event to
	// anything that groups or filters by msg.
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")
	log.Info("workspace snapshot failed", slog.String("workspace_id", "ws_42"))

	payload := decode(t, buf.String())
	if got := payload["msg"]; got != "workspace snapshot failed" {
		t.Errorf("msg = %v, want the caller's message", got)
	}
	if _, ok := payload["message"]; ok {
		t.Error("a message attribute duplicates msg and must not be emitted")
	}
	if got := payload["workspace_id"]; got != "ws_42" {
		t.Errorf("workspace_id = %v, want ws_42", got)
	}
}

func TestNewHonoursLevelAndFormat(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, "warn", "json")
	log.Info("filtered out")
	if buf.Len() != 0 {
		t.Errorf("info passed a warn threshold: %s", buf.String())
	}
	log.Warn("kept")
	if got := decode(t, buf.String())["msg"]; got != "kept" {
		t.Errorf("msg = %v, want kept", got)
	}

	buf.Reset()
	text := logger.New(&buf, "info", "text")
	text.Info("plain")
	if line := buf.String(); !strings.Contains(line, "msg=plain") {
		t.Errorf("text format did not render msg=plain: %q", line)
	}
}

func TestNewFallsBackToInfoForAnUnknownLevel(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, "chatty", "json")
	log.Debug("dropped")
	if buf.Len() != 0 {
		t.Errorf("debug passed the info fallback: %s", buf.String())
	}
	log.Info("kept")
	if buf.Len() == 0 {
		t.Error("info was dropped by the info fallback")
	}
}

func TestHandlerSatisfiesSlogtest(t *testing.T) {
	// slogtest checks the parts of the Handler contract that are easy to get
	// wrong when wrapping one: group nesting, empty attrs, inline groups,
	// Record.Time handling.
	var buf bytes.Buffer
	log := logger.New(&buf, "debug", "json")

	results := func() []map[string]any {
		var out []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			out = append(out, decode(t, line))
		}
		return out
	}
	slogtest.Run(t, func(*testing.T) slog.Handler {
		buf.Reset()
		return log.Handler()
	}, func(*testing.T) map[string]any {
		got := results()
		if len(got) != 1 {
			t.Fatalf("want one record, got %d", len(got))
		}
		return got[0]
	})
}

// The rest of this file covers the failure the correlation handler exists to
// avoid. A handler that adds fields in Handle but does not rebuild itself in
// WithAttrs and WithGroup keeps working for the root logger and silently stops
// for every derived one. This codebase derives a logger with With in 166
// places, so only testing the root logger would prove almost nothing.

const (
	sampleTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	sampleSpanID  = "00f067aa0ba902b7"
)

func tracedContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	traceID, err := trace.TraceIDFromHex(sampleTraceID)
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex(sampleSpanID)
	if err != nil {
		t.Fatal(err)
	}
	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))
}

func TestCorrelationReachesRootAndDerivedLoggers(t *testing.T) {
	ctx := tracedContext(t, logger.ContextWithRequestID(context.Background(), "req_abc123"))

	for _, tc := range []struct {
		name    string
		derive  func(*slog.Logger) *slog.Logger
		nesting string
	}{
		{"root", func(l *slog.Logger) *slog.Logger { return l }, ""},
		{"With", func(l *slog.Logger) *slog.Logger {
			return l.With(slog.String("component", "agent"))
		}, ""},
		{"With twice", func(l *slog.Logger) *slog.Logger {
			return l.With(slog.String("component", "agent")).With(slog.String("bot_id", "b_1"))
		}, ""},
		{"WithGroup", func(l *slog.Logger) *slog.Logger { return l.WithGroup("g") }, "g"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := tc.derive(logger.New(&buf, "info", "json"))
			log.InfoContext(ctx, "handled")

			payload := decode(t, buf.String())
			// WithGroup nests everything the record carries, correlation
			// included. Nothing here relies on that, but the field reference
			// says not to group the root logger for exactly this reason.
			if tc.nesting != "" {
				nested, ok := payload[tc.nesting].(map[string]any)
				if !ok {
					t.Fatalf("group %q missing from %v", tc.nesting, payload)
				}
				payload = nested
			}
			for key, want := range map[string]string{
				"request_id": "req_abc123",
				"trace_id":   sampleTraceID,
				"span_id":    sampleSpanID,
			} {
				if got, _ := payload[key].(string); got != want {
					t.Errorf("%s = %q, want %q (derived logger lost the correlation handler)", key, got, want)
				}
			}
		})
	}
}

func TestCorrelationOmitsKeysItCannotFill(t *testing.T) {
	// An empty trace_id would match queries for records that have no trace at
	// all, so absent identity means absent keys rather than empty ones.
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")
	log.InfoContext(context.Background(), "no identity")

	payload := decode(t, buf.String())
	for _, key := range []string{"request_id", "trace_id", "span_id"} {
		if _, ok := payload[key]; ok {
			t.Errorf("%s present with no identity in context: %v", key, payload)
		}
	}
}

func TestCorrelationIgnoresAnEmptyRequestID(t *testing.T) {
	ctx := logger.ContextWithRequestID(context.Background(), "")
	if got := logger.RequestIDFromContext(ctx); got != "" {
		t.Errorf("RequestIDFromContext = %q, want empty", got)
	}
	var buf bytes.Buffer
	logger.New(&buf, "info", "json").InfoContext(ctx, "blank")
	if _, ok := decode(t, buf.String())["request_id"]; ok {
		t.Error("an empty request id was emitted as a key")
	}
}

func TestLoggingWithoutAContextReportsNoCorrelation(t *testing.T) {
	// slog.Logger.Info uses context.Background internally. Correlation
	// therefore depends on the *Context variants, which is why the field
	// reference requires them wherever a context is in scope.
	var buf bytes.Buffer
	log := logger.New(&buf, "info", "json")
	ctx := tracedContext(t, logger.ContextWithRequestID(context.Background(), "req_abc123"))
	_ = ctx

	log.Info("context dropped")
	if _, ok := decode(t, buf.String())["request_id"]; ok {
		t.Error("request_id appeared without a context; the test no longer proves the *Context requirement")
	}
}

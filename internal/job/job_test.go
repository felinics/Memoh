package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/telemetry"
)

// syncBuffer lets a test read what a unit on another goroutine wrote.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func newLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return logger.New(buf, "debug", "json"), buf
}

func oneRecord(t *testing.T, buf *syncBuffer) map[string]any {
	t.Helper()
	records := buf.records(t)
	if len(records) != 1 || records[0]["msg"] != "job" {
		t.Fatalf("records = %v, want one job record", records)
	}
	return records[0]
}

func TestRunRecordsSuccessAtInfo(t *testing.T) {
	log, buf := newLogger()

	err := Run(context.Background(), log, "test.unit", Options{}, func(ctx context.Context) error {
		Annotate(ctx, slog.String("status", "completed"))
		return nil
	}, slog.String("bot_id", "bot-1"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	record := oneRecord(t, buf)
	if record["level"] != "INFO" || record["operation"] != "test.unit" || record["bot_id"] != "bot-1" || record["status"] != "completed" {
		t.Fatalf("record = %v", record)
	}
	if _, ok := record["latency"]; !ok {
		t.Fatalf("record has no latency: %v", record)
	}
	if _, ok := record["error"]; ok {
		t.Fatalf("success record carries error fields: %v", record)
	}
}

func TestRunRecordsFailureAsAsync(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		level     string
		fault     string
		willRetry bool
	}{
		{name: "client fault is ours", err: apperror.New(apperror.CodeCapabilityNotFound, nil), level: "ERROR", fault: "server"},
		{name: "dependency", err: errs.NewDependency("provider down"), level: "ERROR", fault: "dependency"},
		{name: "retried dependency", err: WillRetry(errs.NewDependency("provider down")), level: "WARN", fault: "dependency", willRetry: true},
		{name: "retried server", err: errs.Wrap(WillRetry(errs.New("write failed")), "provision"), level: "ERROR", fault: "server", willRetry: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := newLogger()
			err := Run(context.Background(), log, "test.unit", Options{}, func(context.Context) error { return tc.err })
			if !errors.Is(err, tc.err) {
				t.Fatalf("Run() error = %v, want fn's error", err)
			}
			record := oneRecord(t, buf)
			if record["level"] != tc.level || record["fault"] != tc.fault || record["will_retry"] != tc.willRetry {
				t.Fatalf("level/fault/will_retry = %v/%v/%v, want %s/%s/%v", record["level"], record["fault"], record["will_retry"], tc.level, tc.fault, tc.willRetry)
			}
		})
	}
}

func TestWillRetryKeepsTheErrorText(t *testing.T) {
	cause := errs.New("write failed")
	if err := WillRetry(cause); err.Error() != "write failed" || errs.Text(err) != "write failed" || !errors.Is(err, cause) {
		t.Fatalf("WillRetry changed the error: %q", err.Error())
	}
	if WillRetry(nil) != nil {
		t.Fatal("WillRetry(nil) != nil")
	}
}

func TestRunRecoversPanic(t *testing.T) {
	log, buf := newLogger()

	err := Run(context.Background(), log, "test.unit", Options{}, func(context.Context) error {
		panic("unit exploded")
	})

	if err == nil || !errs.Analyze(context.Background(), err).Panic {
		t.Fatalf("Run() error = %v, want the recovered panic", err)
	}
	record := oneRecord(t, buf)
	if record["level"] != "ERROR" || record["panic"] != true {
		t.Fatalf("record = %v, want ERROR with panic", record)
	}
}

func TestRunRequestID(t *testing.T) {
	parent := logger.ContextWithRequestID(context.Background(), "req-parent")
	for _, tc := range []struct {
		name string
		opts Options
		own  bool
	}{
		{name: "derived work keeps the id", opts: Options{}},
		{name: "own id", opts: Options{OwnRequestID: true}, own: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log, buf := newLogger()
			var inside string
			_ = Run(parent, log, "test.unit", tc.opts, func(ctx context.Context) error {
				inside = logger.RequestIDFromContext(ctx)
				return nil
			})
			record := oneRecord(t, buf)
			if record["request_id"] != inside {
				t.Fatalf("record request_id = %v, unit saw %q", record["request_id"], inside)
			}
			if tc.own == (inside == "req-parent") || inside == "" {
				t.Fatalf("unit request id = %q (own=%v)", inside, tc.own)
			}
		})
	}
}

func TestRunStartsLinkedRootSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		otel.SetTextMapPropagator(previousPropagator)
	})

	log, _ := newLogger()
	parentCtx, parent := telemetry.Tracer().Start(context.Background(), "request")
	_ = Run(parentCtx, log, "test.unit", Options{}, func(context.Context) error { return errs.New("failed") })
	parent.End()

	var unit sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "test.unit" {
			unit = span
		}
	}
	if unit == nil {
		t.Fatal("no test.unit span")
	}
	if unit.Parent().IsValid() || unit.SpanContext().TraceID() == parent.SpanContext().TraceID() {
		t.Fatal("unit span joined the caller's trace")
	}
	if len(unit.Links()) != 1 || unit.Links()[0].SpanContext.SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("links = %v, want one link to the caller's span", unit.Links())
	}
	if unit.Status().Code != codes.Error {
		t.Fatalf("status = %v, want error", unit.Status())
	}

	// A unit with its own request id was started by no request, so the span
	// in ctx (the registering request's) is not its cause.
	recorder2 := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder2)))
	parentCtx, parent = telemetry.Tracer().Start(context.Background(), "register")
	_ = Run(parentCtx, log, "test.timer", Options{OwnRequestID: true}, func(context.Context) error { return nil })
	parent.End()
	for _, span := range recorder2.Ended() {
		if span.Name() == "test.timer" && (len(span.Links()) != 0 || span.Parent().IsValid()) {
			t.Fatalf("own-request unit links = %v parent = %v, want an unlinked root", span.Links(), span.Parent())
		}
	}
}

func TestGoOutlivesItsCaller(t *testing.T) {
	log, buf := newLogger()
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	done := make(chan error, 1)

	Go(ctx, log, "test.unit", Options{}, func(ctx context.Context) error {
		<-release
		done <- ctx.Err()
		return nil
	})
	cancel()
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unit context ended with its caller: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unit did not run")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(buf.records(t)) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if record := oneRecord(t, buf); record["level"] != "INFO" {
		t.Fatalf("record = %v", record)
	}
}

func TestAnnotateOutsideAUnitDoesNothing(_ *testing.T) {
	Annotate(context.Background(), slog.String("k", "v"))
}

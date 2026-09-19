package telemetry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/felinics/memoh/internal/telemetry"
)

// PostgreSQL writes the offending values into its error message, so recording
// the error verbatim would put the contents of a row in the trace — the same
// data the query arguments were withheld to protect.
func TestPgxTracerClassifiesAnErrorWithoutQuotingIt(t *testing.T) {
	recorder := recordSpans(t)
	tracer := telemetry.PgxTracer{}

	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{
		SQL:  "INSERT INTO users (email) VALUES ($1)",
		Args: []any{"someone@example.test"},
	})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{
		Code:     "23505",
		Message:  "duplicate key value violates unique constraint \"users_email_key\"",
		Detail:   "Key (email)=(someone@example.test) already exists.",
		Severity: "ERROR",
	}})

	span := onlySpan(t, recorder)
	if span.Status().Code != codes.Error {
		t.Error("a failed query was not marked as an error")
	}
	if got := attrs(span)["db.response.status_code"].AsString(); got != "23505" {
		t.Errorf("db.response.status_code = %q, want the SQLSTATE", got)
	}
	for key, value := range attrs(span) {
		if strings.Contains(value.Emit(), "someone@example.test") {
			t.Errorf("attribute %s carries the row's value: %s", key, value.Emit())
		}
	}
	for _, event := range span.Events() {
		for _, kv := range event.Attributes {
			if strings.Contains(kv.Value.Emit(), "someone@example.test") {
				t.Errorf("event %s carries the row's value: %s", event.Name, kv.Value.Emit())
			}
		}
	}
	if got := attrs(span)["db.query.text"].AsString(); got == "" {
		t.Error("the statement text is missing; without it a slow query span says nothing")
	}
}

// An error with no SQLSTATE still has to be distinguishable — a dial failure,
// a closed pool and a cancelled query are different problems.
func TestPgxTracerKeepsAClassForErrorsWithoutASQLSTATE(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"cancelled", context.Canceled, "context.Canceled"},
		{"deadline", context.DeadlineExceeded, "context.DeadlineExceeded"},
		{"other", errors.New("dial tcp: connection refused"), "*errors.errorString"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := recordSpans(t)
			tracer := telemetry.PgxTracer{}
			ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
			tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: tc.err})

			span := onlySpan(t, recorder)
			if got := attrs(span)["error.type"].AsString(); got != tc.want {
				t.Errorf("error.type = %q, want %q", got, tc.want)
			}
		})
	}
}

func onlySpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("want one span, got %d", len(ended))
	}
	if got := ended[0].Name(); got != "postgresql.query" {
		t.Fatalf("span name = %q, want postgresql.query", got)
	}
	return ended[0]
}

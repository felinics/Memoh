package telemetry

import (
	"context"
	"errors"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// PgxTracer records a span per query.
//
// It implements pgx's own QueryTracer hook rather than pulling in a
// third-party bridge: the interface is two methods, and doing it here settles
// the one decision a generic bridge gets wrong for us — query arguments are
// never recorded. They are the values of the rows being read and written, so
// a span carrying them would publish exactly the data `docs/logging.md`
// forbids putting in a log record. The SQL text is recorded; it is code, not
// data, and without it a slow query span says only that something was slow.
//
// Install it on the pool config: poolCfg.ConnConfig.Tracer = telemetry.PgxTracer{}.
// With tracing off the global tracer is a no-op, so this costs a call that
// returns a shared non-recording span.
type PgxTracer struct{}

var _ pgx.QueryTracer = PgxTracer{}

func (PgxTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.DBSystemPostgreSQL,
			semconv.DBQueryText(data.SQL),
		),
	}
	if conn != nil {
		if cfg := conn.Config(); cfg != nil {
			opts = append(opts, trace.WithAttributes(
				semconv.DBNamespace(cfg.Database),
				semconv.ServerAddress(cfg.Host),
				semconv.ServerPort(int(cfg.Port)),
			))
		}
	}
	ctx, _ = otel.Tracer(ScopeName).Start(ctx, "postgresql.query", opts...)
	return ctx
}

func (PgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil && span.IsRecording() {
		// The error is classified, not recorded. PostgreSQL puts the offending
		// values in its message: a unique violation reads "Key (email)=(a@b.com)
		// already exists". Calling RecordError would put that row's contents in
		// the trace — the same data the arguments were withheld to protect.
		// SQLSTATE says what went wrong without saying what the value was.
		var pgErr *pgconn.PgError
		switch {
		case errors.As(data.Err, &pgErr):
			span.SetAttributes(attribute.String("db.response.status_code", pgErr.Code))
		case errors.Is(data.Err, context.Canceled):
			span.SetAttributes(attribute.String("error.type", "context.Canceled"))
		case errors.Is(data.Err, context.DeadlineExceeded):
			span.SetAttributes(attribute.String("error.type", "context.DeadlineExceeded"))
		default:
			// The type, never the message. A dial failure or a closed pool
			// still needs to be distinguishable from a rejected query, and a
			// Go type name cannot contain a row's contents.
			span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", data.Err)))
		}
		span.SetStatus(codes.Error, "")
	}
	span.End()
}

// RecordPoolStats reports a pool's state as the pgxpool.* metrics:
// connections in use against the maximum, how often an acquire found the
// pool empty and how long it waited. A request that is slow because every
// connection is taken looks, on its span, like a slow query; these say which
// one it is.
//
// The metrics come from otelpgx, which is used here for nothing else. They
// are counts read from pool.Stat(), so the objection that keeps its query
// tracer out (see PgxTracer) does not apply. Each pool is told apart by
// db.client.connection.pool.name, host:port/database.
//
// Call it once per pool, after the pool is opened. The meter comes from the
// global provider, which hands off to the one Setup installs whether the pool
// was opened before Setup ran or after; with metrics off it is a no-op. A
// failure to register goes to the OpenTelemetry error handler, because
// missing pool metrics are not a reason to refuse a database.
func RecordPoolStats(pool *pgxpool.Pool) {
	if err := otelpgx.RecordStats(pool); err != nil {
		otel.Handle(err)
	}
}

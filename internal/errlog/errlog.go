package errlog

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/errs"
)

const meterName = "github.com/felinics/memoh/internal/errlog"

// Options describes how the unit of work ended.
type Options struct {
	// Async marks a unit with no external caller (a background run, a
	// scheduled job, an event dispatch). A client fault there is this
	// process's fault.
	Async bool
	// WillRetry reports that the unit will be retried automatically.
	WillRetry bool
}

// Result is the error outcome of a unit of work and its record level.
type Result struct {
	Report errs.Report
	Level  slog.Level
	// retry is set only for a failed async unit; the result record then
	// carries will_retry.
	retry *bool
}

// Finish attributes err at the end of a unit of work, sets the span status
// and counts an error without a recorded origin. A nil err is a success. ctx
// is the unit's own context, the one its span and cancellation belong to.
func Finish(ctx context.Context, operation string, err error, opts Options) Result {
	if err == nil {
		return Result{Level: slog.LevelInfo}
	}
	report := analyze(ctx, operation, err, opts)
	span := trace.SpanFromContext(ctx)
	if report.Fault == errs.FaultServer || report.Fault == errs.FaultDependency {
		span.SetStatus(codes.Error, report.Reason)
	}
	span.SetAttributes(attribute.String("error.type", report.Reason))
	result := Result{Report: report, Level: levelFor(report, opts)}
	if opts.Async {
		willRetry := opts.WillRetry
		result.retry = &willRetry
	}
	return result
}

// Event gives the fields of an event record: one failed attempt in a retry
// loop, a fallback, or a swallowed error. Its level is always WARN. An event
// does not decide the unit's outcome, so the span is left alone; an error
// without a recorded origin is still counted.
func Event(ctx context.Context, operation string, err error, opts Options) Result {
	if err == nil {
		return Result{Level: slog.LevelWarn}
	}
	return Result{Report: analyze(ctx, operation, err, opts), Level: slog.LevelWarn}
}

func analyze(ctx context.Context, operation string, err error, opts Options) errs.Report {
	report := errs.Analyze(ctx, err)
	if opts.Async && report.Fault == errs.FaultClient {
		report.Fault = errs.FaultServer
		report.Unlocated = len(report.Stack) == 0
	}
	if report.Unlocated && (report.Fault == errs.FaultServer || report.Fault == errs.FaultDependency) {
		recordUnlocated(ctx, operation)
	}
	return report
}

func levelFor(report errs.Report, opts Options) slog.Level {
	if report.Panic {
		return slog.LevelError
	}
	if opts.WillRetry && report.Fault == errs.FaultDependency {
		return slog.LevelWarn
	}
	switch report.Fault {
	case errs.FaultServer:
		return slog.LevelError
	case errs.FaultDependency:
		// The remote already logged its own failure at ERROR; the request_id
		// on this record finds it.
		if report.Remote && (report.RemoteFault == string(errs.FaultServer) || report.RemoteFault == string(errs.FaultDependency)) {
			return slog.LevelWarn
		}
		return slog.LevelError
	case errs.FaultClient, errs.FaultCanceled:
		return slog.LevelInfo
	default:
		return slog.LevelError
	}
}

func recordUnlocated(ctx context.Context, operation string) {
	counter, err := otel.Meter(meterName).Int64Counter("errors.unlocated_total")
	if err != nil {
		return
	}
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", operation)))
}

// Attrs returns the error fields of the record. A successful unit has none.
func (r Result) Attrs() []slog.Attr {
	if r.Report.Fault == "" {
		return nil
	}
	attrs := r.Report.LogAttrs()
	if r.retry != nil {
		attrs = append(attrs, slog.Bool("will_retry", *r.retry))
	}
	return attrs
}

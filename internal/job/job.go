// Package job is the boundary of a background unit of work: work that has an
// object (a session, a workspace, a schedule firing, a task) and an end, but
// no caller waiting for its outcome.
//
// A unit runs under its own root span linked to the operation that caused
// it, recovers its own panic, and writes exactly one result record with
// msg=job. The work inside the unit returns its failure instead of logging
// it; an error the unit handles and continues past is an errlog.Event.
//
// Periodic passes and ticks, reconnect loops and single attempts inside a
// unit are not units: they have no object of their own, and their failures
// are events.
package job

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/telemetry"
)

// Options configures a unit.
type Options struct {
	// OwnRequestID gives the unit a new request id and no link. It is for
	// work no request started, such as a timer or a cron firing, where the id
	// and span in ctx name something unrelated: the request that registered
	// the timer. Work derived from a request or a turn keeps the id it was
	// started with and links to the span that started it.
	OwnRequestID bool
}

// Go runs fn on a new goroutine as one unit and writes its result record.
// operation names the unit; it is a bounded constant such as agent.title,
// and also the label of errors.unlocated_total. ctx is detached from its
// cancellation first, because the unit outlives whatever started it.
func Go(ctx context.Context, log *slog.Logger, operation string, opts Options, fn func(context.Context) error, attrs ...slog.Attr) {
	go func() {
		_ = run(ctx, log, operation, opts, fn, attrs)
	}()
}

// Run is Go on the caller's goroutine, for a caller that already runs on its
// own (a cron callback, a reconcile worker). It returns fn's error, or the
// recovered panic, so the caller can act on it; the caller does not record
// it again.
func Run(ctx context.Context, log *slog.Logger, operation string, opts Options, fn func(context.Context) error, attrs ...slog.Attr) error {
	return run(ctx, log, operation, opts, fn, attrs)
}

// WillRetry marks err as a failure the unit's owner retries automatically.
// The result record then carries will_retry=true, and a dependency failure is
// recorded at WARN. Mark only an attempt that is still inside the retry
// budget: the attempt that exhausts it is reported as the outcome.
func WillRetry(err error) error {
	if err == nil {
		return nil
	}
	return &retryError{err: err}
}

// Annotate adds attrs to the result record of the unit ctx belongs to, for
// facts the unit learns while it runs: a skip reason, an exit code. Outside a
// unit it does nothing.
func Annotate(ctx context.Context, attrs ...slog.Attr) {
	if notes, ok := ctx.Value(annotationsKey{}).(*annotations); ok {
		notes.add(attrs)
	}
}

func run(ctx context.Context, log *slog.Logger, operation string, opts Options, fn func(context.Context) error, attrs []slog.Attr) error {
	if log == nil {
		log = slog.Default()
	}
	ctx = context.WithoutCancel(ctx)
	// The unit is a root of its own: the span that started it may end long
	// before the unit does. The link keeps the two reachable.
	var trigger telemetry.Trigger
	if opts.OwnRequestID {
		ctx = logger.ContextWithRequestID(ctx, httpx.NewRequestID())
	} else {
		trigger = telemetry.TriggerFrom(ctx)
		if trigger == (telemetry.Trigger{}) {
			trigger = telemetry.TriggerFromContext(ctx)
		}
	}
	ctx, span := telemetry.StartLinked(ctx, trigger, operation)
	defer span.End()
	notes := &annotations{}
	ctx = context.WithValue(ctx, annotationsKey{}, notes)

	start := time.Now()
	err := call(ctx, fn)
	var retry *retryError
	result := errlog.Finish(ctx, operation, err, errlog.Options{Async: true, WillRetry: errors.As(err, &retry)})
	record := make([]slog.Attr, 0, 2+len(attrs)+len(result.Attrs()))
	record = append(record, slog.String("operation", operation), slog.Duration("latency", time.Since(start)))
	record = append(record, attrs...)
	record = append(record, notes.list()...)
	record = append(record, result.Attrs()...)
	log.LogAttrs(ctx, result.Level, "job", record...)
	return err
}

func call(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = errs.Recovered(v)
		}
	}()
	return fn(ctx)
}

type retryError struct {
	err error
}

func (e *retryError) Error() string { return e.err.Error() }

func (e *retryError) Unwrap() error { return e.err }

type annotationsKey struct{}

type annotations struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

func (a *annotations) add(attrs []slog.Attr) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attrs = append(a.attrs, attrs...)
}

func (a *annotations) list() []slog.Attr {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]slog.Attr(nil), a.attrs...)
}

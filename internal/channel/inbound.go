package channel

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/logger"
	"github.com/felinics/memoh/internal/telemetry"
)

// ErrInboundQueueFull indicates the synchronous inbound queue admission failed
// because all worker slots are saturated.
var ErrInboundQueueFull = errors.New("inbound queue full")

// IsInboundQueueFull reports whether err means the inbound queue rejected a
// message due to local capacity.
func IsInboundQueueFull(err error) bool {
	return errors.Is(err, ErrInboundQueueFull)
}

type inboundTask struct {
	cfg ChannelConfig
	msg InboundMessage
	// trigger names the request that enqueued this message, so the work the
	// worker does can be traced back to what asked for it. It is carried on
	// the task rather than in a context because the enqueuing request is
	// answered and gone before a worker looks at this.
	trigger telemetry.Trigger
}

// HandleInbound enqueues an inbound message for asynchronous processing by the worker pool.
//
// ctx is read for the identity of the request doing the enqueuing and is
// deliberately not kept: the work outlives it.
func (m *Manager) HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
	if m.processor == nil {
		return errors.New("inbound processor not configured")
	}
	m.startInboundWorkers() //nolint:contextcheck // The shared pool intentionally owns a request-independent lifecycle context.
	if m.inboundCtx != nil && m.inboundCtx.Err() != nil {
		return errors.New("inbound dispatcher stopped")
	}
	task := inboundTask{
		cfg:     cfg,
		msg:     msg,
		trigger: telemetry.TriggerFrom(ctx),
	}
	select {
	case m.inboundQueue <- task:
		return nil
	default:
		return ErrInboundQueueFull
	}
}

func (m *Manager) handleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
	if m.processor == nil {
		return errors.New("inbound processor not configured")
	}
	sender := m.newReplySender(cfg, msg.Channel)
	return m.processor.HandleInbound(ctx, cfg, msg, sender)
}

// handleConnectionInbound is the handler a long-lived adapter connection
// calls for each message. The message is a unit of its own; the connection
// it arrived on started for an unrelated reason, so nothing is linked.
func (m *Manager) handleConnectionInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
	return m.runInboundUnit(ctx, telemetry.Trigger{}, cfg, msg)
}

// withInboundRequestID gives one inbound message a request id of its own. The
// id a context already carries names something else: the request that
// enqueued the message, or the configuration request that started the
// connection it arrived on, which every message on that connection would
// otherwise report.
func withInboundRequestID(ctx context.Context) context.Context {
	return logger.ContextWithRequestID(ctx, httpx.NewRequestID())
}

// runInboundUnit handles one inbound message under its own trace and writes
// its one result line. The queue worker and adapter connections both reach
// the processor through it. It returns the error so that an adapter can act
// on it (ack, retry, reconnect); an adapter does not log it again.
//
// Its own trace, rather than the enqueuing request's: that request was
// answered before this ran, so a parent-child edge would give the trace a
// parent that ends before its child. The link keeps the two reachable from
// each other while letting each report an honest duration.
func (m *Manager) runInboundUnit(ctx context.Context, trigger telemetry.Trigger, cfg ChannelConfig, msg InboundMessage) error {
	ctx, span := telemetry.StartLinked(ctx, trigger, "channel.inbound",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("channel", msg.Channel.String()),
			attribute.String("agent.bot_id", cfg.BotID),
		),
	)
	defer span.End()

	start := time.Now()
	err := m.handleInbound(ctx, cfg, msg)
	if err != nil {
		span.RecordError(err)
	}
	// The unit is not async: the message has a sender, so a client fault
	// stays the sender's.
	result := errlog.Finish(ctx, "channel.inbound", err, errlog.Options{})
	if m.logger != nil {
		attrs := append([]slog.Attr{
			slog.String("channel", msg.Channel.String()),
			slog.String("bot_id", cfg.BotID),
			slog.Duration("latency", time.Since(start)),
		}, result.Attrs()...)
		m.logger.LogAttrs(ctx, result.Level, "inbound message", attrs...)
	}
	return err
}

func (m *Manager) startInboundWorkers() {
	m.inboundOnce.Do(func() {
		// The pool is shared by every inbound request, so it must not retain
		// values from whichever request happened to initialize it first.
		inboundCtx, inboundCancel := context.WithCancel(context.Background())
		m.inboundCtx, m.inboundCancel = inboundCtx, inboundCancel
		for i := 0; i < m.inboundWorkers; i++ {
			go m.runInboundWorker(inboundCtx)
		}
	})
}

func (m *Manager) runInboundWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-m.inboundQueue:
			m.runInboundTask(ctx, task)
		}
	}
}

// runInboundTask handles one queued message.
func (m *Manager) runInboundTask(ctx context.Context, task inboundTask) {
	_ = m.runInboundUnit(withInboundRequestID(ctx), task.trigger, task.cfg, task.msg)
}

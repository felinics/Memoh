package channel

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	if err := m.processor.HandleInbound(ctx, cfg, msg, sender); err != nil {
		if m.logger != nil {
			m.logger.ErrorContext(ctx, "inbound processing failed", slog.String("channel", msg.Channel.String()), slog.Any("error", err))
		}
		return err
	}
	return nil
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

// runInboundTask handles one queued message under its own trace.
//
// Its own, rather than the enqueuing request's: that request was answered
// before this ran, so a parent-child edge would give the trace a parent that
// ends before its child. The link keeps the two reachable from each other
// while letting each report an honest duration.
func (m *Manager) runInboundTask(ctx context.Context, task inboundTask) {
	ctx, span := telemetry.StartLinked(ctx, task.trigger, "channel.inbound",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("channel", task.msg.Channel.String()),
			attribute.String("agent.bot_id", task.msg.BotID),
		),
	)
	defer span.End()

	if err := m.handleInbound(ctx, task.cfg, task.msg); err != nil {
		span.RecordError(err)
		if m.logger != nil {
			m.logger.ErrorContext(ctx, "inbound processing failed", slog.String("channel", task.msg.Channel.String()), slog.Any("error", err))
		}
	}
}

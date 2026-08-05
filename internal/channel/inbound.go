package channel

import (
	"context"
	"errors"
	"log/slog"
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
}

// HandleInbound enqueues an inbound message for asynchronous processing by the worker pool.
func (m *Manager) HandleInbound(_ context.Context, cfg ChannelConfig, msg InboundMessage) error {
	if m.processor == nil {
		return errors.New("inbound processor not configured")
	}
	m.startInboundWorkers() //nolint:contextcheck // The shared pool intentionally owns a request-independent lifecycle context.
	if m.inboundCtx != nil && m.inboundCtx.Err() != nil {
		return errors.New("inbound dispatcher stopped")
	}
	task := inboundTask{
		cfg: cfg,
		msg: msg,
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
			m.logger.Error("inbound processing failed", slog.String("channel", msg.Channel.String()), slog.Any("error", err))
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
			if err := m.handleInbound(ctx, task.cfg, task.msg); err != nil {
				if m.logger != nil {
					m.logger.Error("inbound processing failed", slog.String("channel", task.msg.Channel.String()), slog.Any("error", err))
				}
			}
		}
	}
}

package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
func (m *Manager) HandleInbound(ctx context.Context, cfg ChannelConfig, msg InboundMessage) error {
	if m.processor == nil {
		return errors.New("inbound processor not configured")
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errManagerStopped
	}
	m.startInboundWorkersLocked(ctx)
	task := inboundTask{
		cfg: cfg,
		msg: msg,
	}
	select {
	case m.inboundQueue <- task:
		m.mu.Unlock()
		return nil
	default:
		m.mu.Unlock()
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

// startInboundWorkersLocked starts the pool while the caller holds m.mu, so
// worker setup and shutdown admission are serialized.
func (m *Manager) startInboundWorkersLocked(ctx context.Context) {
	m.inboundOnce.Do(func() {
		workerCtx := context.WithoutCancel(ctx)
		inboundCtx, inboundCancel := context.WithCancel(workerCtx)
		done := make(chan struct{})
		m.lifecycleMu.Lock()
		m.inboundCtx = inboundCtx
		m.inboundCancel = inboundCancel
		m.inboundDone = done
		m.lifecycleMu.Unlock()
		var workers sync.WaitGroup
		workers.Add(m.inboundWorkers)
		for i := 0; i < m.inboundWorkers; i++ {
			go func() {
				defer workers.Done()
				m.runInboundWorker(inboundCtx)
			}()
		}
		go func() {
			workers.Wait()
			close(done)
		}()
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
					m.logger.ErrorContext(ctx, "inbound processing failed", slog.String("channel", task.msg.Channel.String()), slog.Any("error", err))
				}
			}
		}
	}
}

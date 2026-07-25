package session

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
)

func (m *Manager) localControl(streamID string) *runControl {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	streamID = strings.TrimSpace(streamID)
	var match *runControl
	for key, ctrl := range m.controls {
		if key.streamID != streamID {
			continue
		}
		if match != nil {
			return nil
		}
		match = ctrl
	}
	return match
}

func (m *Manager) localControlForScope(botID, sessionID, streamID string) *runControl {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.controls[scopedRunControlKey(botID, sessionID, streamID)]
}

func (m *Manager) localControlForHandle(handle RunHandle) *runControl {
	handle = handle.normalized()
	ctrl := m.localControlForScope(handle.BotID, handle.SessionID, handle.StreamID)
	if ctrl == nil || ctrl.botID != handle.BotID || ctrl.sessionID != handle.SessionID || ctrl.generation != handle.Generation {
		return nil
	}
	return ctrl
}

func (m *Manager) forgetLocalControlForHandle(ctx context.Context, handle RunHandle) {
	handle = handle.normalized()
	m.mu.Lock()
	ctrl := m.controls[scopedRunControlKey(handle.BotID, handle.SessionID, handle.StreamID)]
	if ctrl == nil || ctrl.botID != handle.BotID || ctrl.sessionID != handle.SessionID || ctrl.generation != handle.Generation {
		m.mu.Unlock()
		return
	}
	delete(m.controls, ctrl.key())
	m.mu.Unlock()
	ctrl.stopCommands()
	_ = waitRunControlReady(ctx, ctrl)
	_ = m.stopLeaseRenewalContext(ctx, ctrl)
	ctrl.revokeOwnership(context.Canceled)
}

func (m *Manager) forgetLocalControl(ctx context.Context, streamID string) {
	m.mu.Lock()
	var ctrl *runControl
	for key, candidate := range m.controls {
		if key.streamID == strings.TrimSpace(streamID) {
			if ctrl != nil {
				m.mu.Unlock()
				return
			}
			ctrl = candidate
		}
	}
	if ctrl != nil {
		delete(m.controls, ctrl.key())
	}
	m.mu.Unlock()
	if ctrl == nil {
		return
	}
	ctrl.stopCommands()
	_ = waitRunControlReady(ctx, ctrl)
	_ = m.stopLeaseRenewalContext(ctx, ctrl)
	ctrl.revokeOwnership(context.Canceled)
}

func (m *Manager) removeLocalControl(streamID string, expected *runControl) {
	m.mu.Lock()
	key := expected.key()
	removed := false
	if key.streamID == strings.TrimSpace(streamID) && m.controls[key] == expected {
		delete(m.controls, key)
		removed = true
	}
	m.mu.Unlock()
	if removed {
		expected.stopCommands()
		expected.revokeOwnership(ErrRunOwnershipLost)
	}
}

func (m *Manager) stopAllLocalControls(ctx context.Context) error {
	m.mu.Lock()
	controls := make([]*runControl, 0, len(m.controls))
	for streamID, ctrl := range m.controls {
		controls = append(controls, ctrl)
		delete(m.controls, streamID)
	}
	m.mu.Unlock()
	var stopErr error
	for _, ctrl := range controls {
		ctrl.revokeOwnership(context.Canceled)
		ctrl.stopCommands()
		select {
		case ctrl.abortCh <- struct{}{}:
		default:
		}
		if ctrl.cancel != nil {
			ctrl.cancel()
		}
		if err := waitRunControlReady(ctx, ctrl); err != nil && stopErr == nil {
			stopErr = err
		}
		if err := m.stopLeaseRenewalContext(ctx, ctrl); err != nil && stopErr == nil {
			stopErr = err
		}
	}
	return stopErr
}

const runtimeOwnerShutdownError = "runtime owner shut down"

func (m *Manager) releaseAllLocalRuns(ctx context.Context) error {
	if m == nil || m.distributed == nil {
		return nil
	}
	m.mu.Lock()
	controls := make([]*runControl, 0, len(m.controls))
	for _, ctrl := range m.controls {
		controls = append(controls, ctrl)
	}
	m.mu.Unlock()

	var releaseErr error
	for _, ctrl := range controls {
		_, err := m.finishRunState(ctx, ctrl.handle(), RunStatusLost, runtimeOwnerShutdownError)
		if err == nil || errors.Is(err, ErrRunOwnershipLost) {
			continue
		}
		if releaseErr == nil {
			releaseErr = err
		}
		m.logger.WarnContext(ctx, "release runtime run during shutdown failed", slog.Any("error", err), slog.String("stream_id", ctrl.streamID))
	}
	return releaseErr
}

func (c *runControl) commandContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if c != nil {
		if fence, ok := runtimefence.FromContext(c.lifecycleCtx); ok {
			parent = runtimefence.WithContext(parent, fence)
		}
	}
	ctx, cancel := context.WithCancel(parent)
	if c == nil || c.lifecycleCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(c.lifecycleCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (c *runControl) commandsActive() bool {
	return c != nil && c.lifecycleCtx != nil && c.lifecycleCtx.Err() == nil
}

func (c *runControl) stopCommands() {
	if c == nil {
		return
	}
	if c.lifecycleCancel != nil {
		c.lifecycleCancel()
	}
	c.closeInject()
}

func (c *runControl) sendInject(ctx context.Context, message agentdomain.InjectMessage) (bool, string) {
	if c == nil {
		return false, "active runtime is not available"
	}
	c.injectMu.Lock()
	defer c.injectMu.Unlock()
	if c.injectClosed || c.injectCh == nil {
		return false, "active runtime is not available"
	}
	select {
	case c.injectCh <- message:
		return true, ""
	case <-ctx.Done():
		return false, ctx.Err().Error()
	default:
		return false, "active runtime is not accepting steer messages"
	}
}

func (c *runControl) closeInject() {
	if c == nil {
		return
	}
	c.injectMu.Lock()
	defer c.injectMu.Unlock()
	if c.injectClosed || c.injectCh == nil {
		return
	}
	close(c.injectCh)
	c.injectClosed = true
}

func (c *runControl) markReady() {
	if c == nil {
		return
	}
	c.readyOnce.Do(func() { close(c.ready) })
}

func waitRunControlReady(ctx context.Context, ctrl *runControl) error {
	if ctrl == nil || ctrl.ready == nil {
		return nil
	}
	select {
	case <-ctrl.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) startLeaseRenewal(ctx context.Context, ctrl *runControl) {
	if m == nil || ctrl == nil || m.distributed == nil || m.ownerLeaseTTL <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ctrl.leaseLifecycleMu.Lock()
	ctrl.leaseStop = cancel
	ctrl.leaseDone = done
	ctrl.leaseLifecycleMu.Unlock()
	interval := leaseRenewInterval(m.ownerLeaseTTL)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !ctrl.leaseIsValidAt(time.Now()) {
					m.cancelRunControl(ctrl)
					return
				}
				localRenewalStarted := time.Now()
				leaseDeadline := ctrl.leaseDeadline()
				if leaseDeadline.IsZero() || !localRenewalStarted.Before(leaseDeadline) {
					m.cancelRunControl(ctrl)
					return
				}
				renewCtx, renewCancel := context.WithDeadline(ctx, leaseDeadline)
				now, err := m.backend.Now(renewCtx)
				if err == nil {
					err = m.distributed.RenewLease(renewCtx, Key{BotID: ctrl.botID, SessionID: ctrl.sessionID}, ctrl.streamID, m.ownerID, ctrl.generation, now, now.Add(m.ownerLeaseTTL))
				}
				renewCancel()
				if err == nil {
					deadline := localRenewalStarted.Add(m.ownerLeaseTTL)
					if !time.Now().Before(deadline) {
						m.cancelRunControl(ctrl)
						cancel()
						return
					}
					ctrl.setLeaseValidUntil(deadline)
					continue
				}
				if ctx.Err() != nil {
					return
				}
				m.logger.WarnContext(ctx, "renew runtime owner lease failed", slog.Any("error", err), slog.String("stream_id", ctrl.streamID))
				if errors.Is(err, ErrRunOwnershipLost) || !ctrl.leaseIsValidAt(time.Now()) {
					m.cancelRunControl(ctrl)
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		m.watchLeaseDeadline(ctx, cancel, ctrl)
	}()
	go func() {
		workers.Wait()
		close(done)
	}()
}

func (c *runControl) setLeaseValidUntil(deadline time.Time) {
	if c == nil {
		return
	}
	c.leaseMu.Lock()
	c.leaseValidUntil = deadline
	c.leaseMu.Unlock()
	if c.leaseChanged != nil {
		select {
		case c.leaseChanged <- struct{}{}:
		default:
		}
	}
}

func (m *Manager) watchLeaseDeadline(ctx context.Context, stop context.CancelFunc, ctrl *runControl) {
	for {
		deadline := ctrl.leaseDeadline()
		if deadline.IsZero() {
			select {
			case <-ctx.Done():
				return
			case <-ctrl.leaseChanged:
				continue
			}
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-ctrl.leaseChanged:
			stopTimer(timer)
			continue
		case <-timer.C:
			if ctrl.leaseIsValidAt(time.Now()) {
				continue
			}
			stop()
			m.cancelRunControl(ctrl)
			return
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (c *runControl) leaseDeadline() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.leaseMu.RLock()
	defer c.leaseMu.RUnlock()
	return c.leaseValidUntil
}

func (c *runControl) leaseIsValidAt(now time.Time) bool {
	if c == nil {
		return false
	}
	c.leaseMu.RLock()
	deadline := c.leaseValidUntil
	c.leaseMu.RUnlock()
	return !deadline.IsZero() && now.Before(deadline)
}

func (*Manager) cancelRunControl(ctrl *runControl) {
	if ctrl == nil {
		return
	}
	ctrl.revokeOwnership(ErrRunOwnershipLost)
	ctrl.stopCommands()
	select {
	case ctrl.abortCh <- struct{}{}:
	default:
	}
	if ctrl.cancel != nil {
		ctrl.cancel()
	}
}

func (c *runControl) revokeOwnership(cause error) {
	if c == nil || c.ownershipCancel == nil {
		return
	}
	c.ownershipOnce.Do(func() { c.ownershipCancel(cause) })
}

func (m *Manager) stopLeaseRenewal(ctrl *runControl) {
	_ = m.stopLeaseRenewalContext(context.Background(), ctrl)
}

func (*Manager) stopLeaseRenewalContext(ctx context.Context, ctrl *runControl) error {
	if ctrl == nil {
		return nil
	}
	ctrl.leaseLifecycleMu.Lock()
	stop := ctrl.leaseStop
	done := ctrl.leaseDone
	ctrl.leaseLifecycleMu.Unlock()
	if stop == nil {
		return nil
	}
	stop()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctrl.leaseLifecycleMu.Lock()
	ctrl.leaseStop = nil
	ctrl.leaseDone = nil
	ctrl.leaseLifecycleMu.Unlock()
	return nil
}

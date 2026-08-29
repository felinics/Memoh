package sessionruntime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
)

func (m *Manager) localControl(runID string) *runControl {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runID = strings.TrimSpace(runID)
	var match *runControl
	for key, ctrl := range m.controls {
		if key.runID != runID {
			continue
		}
		if match != nil {
			return nil
		}
		match = ctrl
	}
	return match
}

func (m *Manager) localControlForScope(botID, sessionID, runID string) *runControl {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.controls[scopedRunControlKey(botID, sessionID, runID)]
}

func (m *Manager) localControlForHandle(handle RunHandle) *runControl {
	handle = handle.normalized()
	ctrl := m.localControlForScope(handle.BotID, handle.SessionID, handle.RunID)
	if ctrl == nil || ctrl.botID != handle.BotID || ctrl.sessionID != handle.SessionID || ctrl.generation != handle.Generation {
		return nil
	}
	return ctrl
}

func (m *Manager) forgetLocalControlForHandle(ctx context.Context, handle RunHandle) {
	handle = handle.normalized()
	m.mu.Lock()
	ctrl := m.controls[scopedRunControlKey(handle.BotID, handle.SessionID, handle.RunID)]
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

func (m *Manager) forgetLocalControl(ctx context.Context, runID string) {
	m.mu.Lock()
	var ctrl *runControl
	for key, candidate := range m.controls {
		if key.runID == strings.TrimSpace(runID) {
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

func (m *Manager) removeLocalControl(runID string, expected *runControl) {
	m.mu.Lock()
	key := expected.key()
	removed := false
	if key.runID == strings.TrimSpace(runID) && m.controls[key] == expected {
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
	for runID, ctrl := range m.controls {
		controls = append(controls, ctrl)
		delete(m.controls, runID)
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
		runCtx, cancel := ctrl.cleanupContext(ctx)
		_, err := m.finishRunState(runCtx, ctrl.handle(), RunStatusLost, "", runtimeOwnerShutdownError)
		cancel()
		if err == nil || errors.Is(err, ErrRunOwnershipLost) {
			continue
		}
		if releaseErr == nil {
			releaseErr = err
		}
		m.logger.Warn("release runtime run during shutdown failed", slog.Any("error", err), slog.String("run_id", ctrl.runID))
	}
	return releaseErr
}

func (c *runControl) cleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if c == nil || c.lifecycleCtx == nil {
		return context.WithCancel(parent)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.lifecycleCtx))
	stop := context.AfterFunc(parent, cancel)
	if parent.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

func (c *runControl) commandContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if c == nil || c.lifecycleCtx == nil {
		ctx, cancelCause := context.WithCancelCause(parent)
		cancel := func() { cancelCause(context.Canceled) }
		return ctx, cancel
	}

	// The run owns context values; the command transport only contributes its
	// cancellation. Detach both here so ownership loss can retain its cause.
	base, cancelCause := context.WithCancelCause(context.WithoutCancel(c.lifecycleCtx))
	ctx := base
	stopDeadline := func() {}
	// A command deadline must stay a deadline: callers distinguish an expired
	// command from a cancelled one by ctx.Err(), which a cancel-only relay
	// would always report as context.Canceled.
	deadline, hasDeadline := parent.Deadline()
	if hasDeadline {
		var cancelDeadline context.CancelFunc
		ctx, cancelDeadline = context.WithDeadline(base, deadline)
		stopDeadline = cancelDeadline
	}
	cancelParent := func() {
		cause := context.Cause(parent)
		// The deadline layer expires on its own at the same instant and is the
		// only path that can report ctx.Err() as context.DeadlineExceeded. Check
		// Err rather than Cause: a parent cancelled early may carry a
		// DeadlineExceeded cause while its cancellation mode is still Canceled.
		if hasDeadline && errors.Is(parent.Err(), context.DeadlineExceeded) {
			return
		}
		cancelCause(cause)
	}
	cancelOwner := func() {
		if c.ownershipWasLost() {
			cancelCause(ErrRunOwnershipLost)
			return
		}
		cancelCause(context.Canceled)
	}
	stopParent := context.AfterFunc(parent, cancelParent)
	stopOwner := context.AfterFunc(c.lifecycleCtx, cancelOwner)
	// AfterFunc callbacks run asynchronously. Apply cancellation synchronously
	// when either source is already done because callers inspect ctx.Err next.
	if parent.Err() != nil {
		cancelParent()
	}
	if c.lifecycleCtx.Err() != nil {
		cancelOwner()
	}
	return ctx, func() {
		stopParent()
		stopOwner()
		stopDeadline()
		cancelCause(context.Canceled)
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
	c.stopInject()
}

func (c *runControl) sendInject(ctx context.Context, message turn.InjectMessage) (bool, string) {
	if c == nil {
		return false, "active runtime is not available"
	}
	c.injectMu.Lock()
	defer c.injectMu.Unlock()
	if c.injectStopped || c.injectCh == nil {
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

func (c *runControl) stopInject() {
	if c == nil {
		return
	}
	c.injectMu.Lock()
	defer c.injectMu.Unlock()
	if c.injectStopped || c.injectCh == nil {
		return
	}
	c.injectStopped = true
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
					err = m.distributed.RenewLease(renewCtx, Key{BotID: ctrl.botID, SessionID: ctrl.sessionID}, ctrl.runID, m.ownerID, ctrl.generation, now, now.Add(m.ownerLeaseTTL))
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
				m.logger.Warn("renew runtime owner lease failed", slog.Any("error", err), slog.String("run_id", ctrl.runID))
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
	if c == nil {
		return
	}
	// Recorded before the nil check on the cancel func, so the flag means the
	// same thing for a control that never carried one.
	if errors.Is(cause, ErrRunOwnershipLost) {
		c.ownershipLost.Store(true)
	}
	if c.ownershipCancel == nil {
		return
	}
	c.ownershipOnce.Do(func() { c.ownershipCancel(cause) })
}

// ownershipWasLost reports that this process was told it no longer owns the run.
// Ordinary teardown revokes with context.Canceled and does not set it, so the
// answer distinguishes "the run ended" from "the run was taken away".
func (c *runControl) ownershipWasLost() bool {
	return c != nil && c.ownershipLost.Load()
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

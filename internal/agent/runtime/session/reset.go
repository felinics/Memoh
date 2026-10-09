package sessionruntime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimefence"
)

const minimumHistoryResetTTL = 30 * time.Second

// SetHistoryResetHandler installs the owner-local operation-boundary closer.
// SessionPool supplies it without sessionruntime depending on the ACP package.
func (m *Manager) SetHistoryResetHandler(handler HistoryResetHandler) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.historyResetHandler = handler
	m.mu.Unlock()
}

// WaitForHistoryReset prevents ACP pre-session/cold-start paths that are not
// themselves durable run admissions from crossing the shared reset gate.
func (m *Manager) WaitForHistoryReset(ctx context.Context, botID, sessionID string) error {
	return m.waitForHistoryReset(ctx, ResetScope{BotID: botID, SessionID: sessionID})
}

func (m *Manager) BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (context.Context, func(), error) {
	return m.beginHistoryReset(ctx, ResetScope{BotID: botID, SessionID: sessionID})
}

func (m *Manager) BeginBotHistoryReset(ctx context.Context, botID string) (context.Context, func(), error) {
	return m.beginHistoryReset(ctx, ResetScope{BotID: botID})
}

func (m *Manager) beginHistoryReset(ctx context.Context, scope ResetScope) (context.Context, func(), error) {
	if m == nil || m.backend == nil || m.runs == nil {
		return nil, nil, ErrHistoryResetUnavailable
	}
	scope = scope.normalized()
	if !scope.valid() {
		return nil, nil, errors.New("bot_id is required for history reset")
	}
	resetStore, ok := m.runs.(ledger.ResetStore)
	if !ok {
		return nil, nil, ErrHistoryResetUnavailable
	}
	ttl := 4 * m.ownerLeaseTTL
	if ttl < minimumHistoryResetTTL {
		ttl = minimumHistoryResetTTL
	}
	token := uuid.NewString()
	wantedDurable := ledger.ResetLease{
		Scope: scope.kind(), BotID: scope.BotID, SessionID: scope.SessionID, Token: token,
	}

	// The durable PostgreSQL lease is the single correctness authority: every
	// destructive mutation re-validates its token under the bot parent lock,
	// and admission/claim reject against it. The live backend below only
	// mirrors the lease so cross-process live gates converge promptly; its
	// loss is never fatal and it is never a reason to give the lease back.
	var durable ledger.ResetLease
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		acquired, applied, err := resetStore.AcquireReset(ctx, wantedDurable, ttl)
		if errors.Is(err, ledger.ErrResetScopeNotFound) {
			return nil, nil, err
		}
		if err != nil {
			return nil, nil, errs.WrapDependency(err, "acquire PostgreSQL history reset fence")
		}
		if applied {
			durable = acquired
			break
		}
		if err := waitHistoryResetRetry(ctx, ttl); err != nil {
			return nil, nil, err
		}
	}

	var live ResetLease
	liveHeld := false
	if resetBackend, hasLive := m.backend.(HistoryResetBackend); hasLive {
		if acquiredLive, liveApplied, liveErr := resetBackend.AcquireHistoryReset(ctx, scope, token, ttl); liveErr == nil && liveApplied {
			live = acquiredLive
			liveHeld = true
		} else {
			m.logger.WarnContext(ctx, "live history reset marker unavailable; continuing on the durable lease",
				slog.Any("error", liveErr),
				slog.String("scope", scope.kind()),
				slog.String("bot_id", scope.BotID),
				slog.String("session_id", scope.SessionID),
			)
		}
	}

	// Once acquired, the lease is a lifecycle owned by the mutation rather than
	// by the HTTP request socket. Lease renewal is the only cancellation source;
	// callers still receive resetCtx and must use it for every mutation so a
	// lost lease aborts the in-flight statement and prevents the next step.
	resetCtx, cancelReset := context.WithCancelCause(context.WithoutCancel(ctx))
	resetCtx = runtimefence.WithResetContext(resetCtx, runtimefence.ResetFence{
		Scope: durable.Scope, BotID: durable.BotID, SessionID: durable.SessionID, Token: durable.Token, LeaseTTL: ttl,
	})
	stopRenew := make(chan struct{})
	renewDone := make(chan struct{})
	go m.renewHistoryReset(resetCtx, cancelReset, resetStore, ttl, live, liveHeld, durable, stopRenew, renewDone)

	var resetKeys []Key
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			if len(resetKeys) > 0 && context.Cause(resetCtx) == nil {
				// The deletion has settled while the lease still holds every new
				// run out: restart the projections once more so subscribers that
				// fell back to the ledger during the reset read it again.
				restartCtx, cancel := context.WithTimeout(resetCtx, ttl/3)
				if err := m.invalidateHistoryResetSnapshots(restartCtx, resetKeys, false); err != nil {
					result := errlog.Event(ctx, "session_runtime.history_reset", errs.Wrap(err, "restart runtime snapshots after history reset",
						slog.String("scope", scope.kind()), slog.String("bot_id", scope.BotID), slog.String("session_id", scope.SessionID)), errlog.Options{})
					m.logger.LogAttrs(ctx, result.Level, "restart runtime snapshots after history reset failed; subscribers reconcile on their own", result.Attrs()...)
				}
				cancel()
			}
			close(stopRenew)
			<-renewDone
			cancelReset(context.Canceled)
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(resetCtx), ttl/3)
			defer cancel()
			if liveHeld {
				if resetBackend, hasLive := m.backend.(HistoryResetBackend); hasLive {
					if released, err := resetBackend.ReleaseHistoryReset(releaseCtx, live); err != nil || !released {
						m.logger.WarnContext(ctx, "release live history reset marker failed",
							slog.Any("error", err),
							slog.String("scope", scope.kind()),
							slog.String("bot_id", scope.BotID),
							slog.String("session_id", scope.SessionID),
						)
					}
				}
			}
			if released, err := resetStore.ReleaseReset(releaseCtx, durable); err != nil || !released {
				m.logger.WarnContext(ctx, "release PostgreSQL history reset fence failed",
					slog.Any("error", err),
					slog.String("scope", scope.kind()),
					slog.String("bot_id", scope.BotID),
					slog.String("session_id", scope.SessionID),
				)
			}
		})
	}

	if err := m.drainHistoryReset(resetCtx, scope, resetStore); err != nil {
		err = runtimefence.NormalizeResetError(resetCtx, err)
		release()
		return nil, nil, err
	}
	if err := context.Cause(resetCtx); err != nil {
		release()
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, err
	}
	// Every run the reset covers is terminal now and none can start until
	// release. Drop them from the live projections before the caller deletes
	// their history; a projection that cannot be cleared fails the reset, so
	// no deletion proceeds under a view that still shows the run.
	keys, err := historyResetSessions(resetCtx, scope, resetStore)
	if err == nil {
		resetKeys = keys
		err = m.invalidateHistoryResetSnapshots(resetCtx, keys, true)
	}
	if err != nil {
		err = runtimefence.NormalizeResetError(resetCtx, err)
		release()
		return nil, nil, err
	}
	return resetCtx, release, nil
}

func historyResetSessions(ctx context.Context, scope ResetScope, store ledger.ResetStore) ([]Key, error) {
	if scope.SessionID != "" {
		return []Key{{BotID: scope.BotID, SessionID: scope.SessionID}}, nil
	}
	ids, err := store.SessionIDsByBot(ctx, scope.BotID)
	if err != nil {
		return nil, errs.WrapDependency(err, "list history reset sessions")
	}
	keys := make([]Key, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, Key{BotID: scope.BotID, SessionID: id})
	}
	return keys, nil
}

// invalidateHistoryResetSnapshots restarts each projection under a new epoch,
// which makes every subscriber reload it. Before the deletion it drops the
// run a projection shows and leaves projections without one alone; after it,
// it restarts every projection but never touches an active run.
func (m *Manager) invalidateHistoryResetSnapshots(ctx context.Context, keys []Key, beforeDeletion bool) error {
	for _, key := range keys {
		now, err := m.backend.Now(ctx)
		if err != nil {
			return errs.WrapDependency(err, "load runtime backend time")
		}
		epoch := m.newEpoch()
		snapshot, changed, err := m.backend.Update(ctx, key, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
			if !ok {
				return snapshot, false, nil
			}
			run := snapshot.CurrentRunView
			if beforeDeletion && run == nil || !beforeDeletion && run != nil && isActiveRunStatus(run.Status) {
				return snapshot, false, nil
			}
			snapshot.CurrentRunView = nil
			snapshot.Epoch = epoch
			snapshot.Seq = 0
			snapshot.UpdatedAt = now
			return snapshot, true, nil
		})
		if err != nil {
			return errs.WrapDependency(err, "invalidate runtime snapshot for history reset")
		}
		if !changed {
			continue
		}
		if err := m.publishRuntimeDelta(ctx, snapshot, "", RuntimeDelta{}); err != nil {
			result := errlog.Event(ctx, "session_runtime.history_reset", errs.Wrap(err, "publish history reset snapshot",
				slog.String("bot_id", key.BotID), slog.String("session_id", key.SessionID)), errlog.Options{})
			m.logger.LogAttrs(ctx, result.Level, "publish history reset snapshot failed; subscribers will reconcile from snapshot", result.Attrs()...)
		}
	}
	return nil
}

func (m *Manager) renewHistoryReset(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	durableStore ledger.ResetStore,
	ttl time.Duration,
	live ResetLease,
	liveHeld bool,
	durable ledger.ResetLease,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	interval := ttl / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			durableCtx, durableCancel := context.WithTimeout(context.WithoutCancel(ctx), interval/2)
			nextDurable, durableOK, durableErr := durableStore.RenewReset(durableCtx, durable, ttl)
			durableCancel()
			switch {
			case durableErr == nil && durableOK:
				durable = nextDurable
			case durableErr == nil:
				// A token-CAS miss is authoritative loss: a successor owns the
				// scope now, or the row is gone.
				cancel(ErrHistoryResetLeaseLost)
				m.logger.ErrorContext(ctx, "history reset lease was taken over or expired")
				return
			default:
				// Transport errors and timeouts are not loss. The renew may be
				// blocked behind this owner's own fenced mutation holding the
				// bot parent lock — and that mutation refreshes the expiry
				// in-transaction. Correctness never depends on this loop:
				// every destructive statement re-validates the token under the
				// parent lock, so a genuinely lost lease fails closed there
				// while the next successful renew tick observes the CAS miss.
				m.logger.WarnContext(ctx, "history reset lease renewal will retry",
					slog.Any("error", durableErr),
					slog.Time("durable_expires_at", durable.ExpiresAt),
				)
				continue
			}
			if liveHeld {
				// Best-effort mirror only; never a correctness signal.
				if liveStore, hasLive := m.backend.(HistoryResetBackend); hasLive {
					liveCtx, liveCancel := context.WithTimeout(context.WithoutCancel(ctx), interval/2)
					if nextLive, liveOK, liveErr := liveStore.RenewHistoryReset(liveCtx, live, ttl); liveErr == nil && liveOK {
						live = nextLive
					}
					liveCancel()
				}
			}
		}
	}
}

func (m *Manager) drainHistoryReset(ctx context.Context, scope ResetScope, store ledger.ResetStore) error {
	for {
		var runs []ledger.Run
		var err error
		if scope.SessionID == "" {
			runs, err = store.ActiveRunsByBot(ctx, scope.BotID)
		} else {
			run, loadErr := m.runs.ActiveRun(ctx, scope.SessionID)
			switch {
			case loadErr == nil:
				runs = []ledger.Run{run}
			case errors.Is(loadErr, ledger.ErrRunNotFound):
			default:
				err = loadErr
			}
		}
		if err != nil {
			return errs.WrapDependency(err, "list active history reset runs")
		}
		if len(runs) == 0 {
			return nil
		}
		for _, run := range runs {
			if err := m.stopRunForHistoryReset(ctx, run); err != nil && !errors.Is(err, ErrCommandTargetNotActive) {
				return err
			}
		}
		if err := waitHistoryResetRetry(ctx, m.ownerLeaseTTL); err != nil {
			return err
		}
	}
}

func (m *Manager) stopRunForHistoryReset(ctx context.Context, run ledger.Run) error {
	if strings.TrimSpace(run.OwnerID) == "" {
		return m.fenceAndFinalizeOrphanForHistoryReset(ctx, run)
	}
	ref, ok, err := m.RunRef(ctx, run.BotID, run.SessionID, run.RunID)
	if err != nil {
		return errs.WrapDependency(err, "load history reset run route")
	}
	if !ok {
		// The owner route expired or was lost before the durable row was reaped.
		// The row's own fencing token is still enough to terminalize it; if a
		// successor raced us, CAS returns applied=false and the drain loop reloads
		// the current state on its next pass.
		return m.fenceAndFinalizeOrphanForHistoryReset(ctx, run)
	}
	now, err := m.backend.Now(ctx)
	if err != nil {
		return errs.WrapDependency(err, "load runtime backend time")
	}
	cmd := Command{
		Type: CommandHistoryReset, ID: "history-reset-" + uuid.NewString(),
		BotID: run.BotID, SessionID: run.SessionID, RunID: run.RunID,
		Generation: ref.Generation, FencingToken: run.FencingToken,
		CreatedAt: now, ExpiresAt: now.Add(minimumHistoryResetTTL),
	}
	if ref.OwnerID == "" || ref.OwnerID == m.ownerID {
		result := m.executeRoutedCommand(ctx, cmd)
		return commandResultErrorFor(cmd, result)
	}
	return m.dispatchRemoteHistoryReset(ctx, ref.OwnerID, cmd)
}

func (m *Manager) fenceAndFinalizeOrphanForHistoryReset(ctx context.Context, run ledger.Run) error {
	resetFence, ok := runtimefence.ResetFromContext(ctx)
	if !ok {
		return ErrHistoryResetLeaseLost
	}
	orphanStore, ok := m.runs.(ledger.OrphanResetStore)
	if !ok {
		return ErrHistoryResetUnavailable
	}
	_, _, err := orphanStore.FenceAndFinalizeOrphan(ctx, ledger.ResetLease{
		Scope: resetFence.Scope, BotID: resetFence.BotID, SessionID: resetFence.SessionID, Token: resetFence.Token,
	}, run)
	if err == nil || errors.Is(err, ErrHistoryResetLeaseLost) {
		return err
	}
	return errs.WrapDependency(err, "finalize orphaned run for history reset")
}

func (m *Manager) dispatchRemoteHistoryReset(ctx context.Context, ownerID string, cmd Command) error {
	cmd.ReplyOwnerID = m.ownerID
	waiter := &commandWaiter{result: make(chan error, 1)}
	m.mu.Lock()
	if m.pendingCommands[cmd.ID] == nil {
		m.pendingCommands[cmd.ID] = make(map[*commandWaiter]struct{})
	}
	m.pendingCommands[cmd.ID][waiter] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pendingCommands[cmd.ID], waiter)
		if len(m.pendingCommands[cmd.ID]) == 0 {
			delete(m.pendingCommands, cmd.ID)
		}
		m.mu.Unlock()
	}()
	if err := m.distributed.PublishCommand(ctx, ownerID, cmd); err != nil {
		if errors.Is(err, ErrCommandOwnerUnavailable) {
			return err
		}
		return errs.WrapDependency(err, "publish history reset command")
	}
	timeout := time.Until(cmd.ExpiresAt)
	if timeout <= 0 {
		return ErrCommandExpired
	}
	err := m.waitCommandResult(ctx, cmd, waiter.result, timeout, ownerID)
	if err == nil || errors.Is(err, ErrCommandTargetNotActive) || IsHistoryResetBusy(err) {
		return err
	}
	// The run's owner is another process: its failure reaches this one as a
	// dependency's.
	return errs.WrapDependency(err, "reset run on its owner")
}

func (m *Manager) applyHistoryResetCommand(ctx context.Context, cmd Command, ctrl *runControl) error {
	if ctrl == nil {
		return ErrCommandTargetNotActive
	}
	if _, err := m.abortLocal(ctx, ctrl); err != nil && !errors.Is(err, ErrCommandTargetNotActive) {
		return err
	}
	m.mu.Lock()
	handler := m.historyResetHandler
	m.mu.Unlock()
	if handler == nil {
		return ErrHistoryResetUnavailable
	}
	return handler(ctx, ResetScope{BotID: cmd.BotID, SessionID: cmd.SessionID})
}

func waitHistoryResetRetry(ctx context.Context, base time.Duration) error {
	delay := base / 20
	if delay < 10*time.Millisecond {
		delay = 10 * time.Millisecond
	}
	if delay > 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// IsHistoryResetBusy reports whether err, from beginning a history reset,
// means something else holds the conversation: another reset took the lease
// over or deleted the scope, or a run's owner did not stop it in time. The
// caller can try again shortly; any other error is a failure.
func IsHistoryResetBusy(err error) bool {
	switch {
	case errors.Is(err, ErrHistoryResetLeaseLost), errors.Is(err, ledger.ErrResetScopeNotFound):
		return true
	case errs.FaultOf(err) == apperror.FaultDependency:
		return false
	}
	for _, busy := range []error{ErrCommandBusy, ErrCommandExpired, ErrCommandNotAcknowledged, ErrCommandOwnerUnavailable, context.DeadlineExceeded} {
		if errors.Is(err, busy) {
			return true
		}
	}
	return false
}

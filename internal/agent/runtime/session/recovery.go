package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/runtimekind"
)

// recoverWaitingDecision transfers a parked run whose owner is gone: its lease
// expired, or the live backend generation that owned it no longer exists. It
// reports whether the run was handled; false leaves the reaper's lost path.
//
// PostgreSQL is advanced first. For a lease candidate the old Redis
// lease-index member remains until reserveRecoveredWaitingDecision succeeds;
// that old member is the retry pointer if this process dies between the two
// stores. A run the generation sweep found has no such member: the stale
// generation that pointed at it is the one the reclaim replaces, so a
// reservation that fails afterwards ends the run instead of stranding it.
func (m *Manager) recoverWaitingDecision(ctx context.Context, candidate LeaseCandidate) (bool, error) {
	if m == nil || m.runs == nil {
		return false, nil
	}
	m.mu.Lock()
	decisions := m.decisionStore
	m.mu.Unlock()
	reclaimer, ok := m.fence.(DecisionFenceActivator)
	if decisions == nil || !ok {
		return false, nil
	}

	run, err := m.runs.Get(ctx, candidate.RunID)
	if err != nil {
		if errors.Is(err, ledger.ErrRunNotFound) {
			return false, nil
		}
		return false, err
	}
	if run.State != ledger.StateWaitingDecision || !run.AbortRequestedAt.IsZero() {
		return false, nil
	}
	preserved, err := pendingRecoverableDecisions(ctx, decisions, run)
	if err != nil || len(preserved) == 0 {
		return false, err
	}
	// Read before the reclaim, while the retry pointer still finds the run.
	messages, err := decisions.ParkedRunMessages(ctx, run)
	if err != nil {
		return false, fmt.Errorf("load parked run messages: %w", err)
	}

	if m.distributed != nil {
		ref, live, err := m.distributed.LoadRunRef(ctx, Key{BotID: run.BotID, SessionID: run.SessionID}, run.RunID)
		if err != nil {
			return false, err
		}
		if live && ref.FencingToken == run.FencingToken {
			// A previous pass completed the reclaim and only its stale, older index
			// member remains. Do not steal a live successor merely to clean it up.
			return true, nil
		}
	}
	if ctrl := m.localControlForScope(run.BotID, run.SessionID, run.RunID); ctrl != nil {
		m.cancelRunControl(ctrl)
		m.forgetLocalControlForHandle(context.WithoutCancel(ctx), ctrl.handle())
	}

	newToken, err := m.runs.NextFencingToken(ctx)
	if err != nil {
		return false, fmt.Errorf("draw waiting-decision recovery fencing token: %w", err)
	}
	if newToken <= run.FencingToken {
		return false, errors.New("waiting-decision recovery fencing token did not advance")
	}
	liveGeneration, err := m.liveness.LivenessGeneration(ctx)
	if err != nil {
		return false, fmt.Errorf("read waiting-decision recovery generation: %w", err)
	}
	liveGeneration = strings.TrimSpace(liveGeneration)
	if liveGeneration == "" {
		return false, errors.New("waiting-decision recovery generation is empty")
	}
	if err := reclaimer.ReclaimWaitingDecision(
		ctx,
		run.BotID, run.SessionID, run.RunID, m.ownerID, liveGeneration,
		run.FencingToken, newToken,
		preserved,
	); err != nil {
		return false, err
	}
	run.OwnerID = m.ownerID
	run.FencingToken = newToken
	run.LiveGeneration = liveGeneration
	run.OwnerSince = time.Now().UTC()
	if err := m.reserveRecoveredWaitingDecision(ctx, run, messages); err != nil {
		if !candidate.ExpiresAt.IsZero() {
			return false, err
		}
		return true, m.endUnreservedWaitingDecision(ctx, run, err)
	}
	return true, nil
}

// endUnreservedWaitingDecision records the run this process reclaimed but
// could not reserve as lost and closes its decisions, so no answerable request
// is left on a run that nothing will continue.
func (m *Manager) endUnreservedWaitingDecision(ctx context.Context, run ledger.Run, cause error) error {
	m.logger.WarnContext(ctx, "recovered waiting-decision run could not be reserved",
		slog.String("run_id", run.RunID), slog.Any("error", cause))
	handle := RunHandle{
		BotID: run.BotID, SessionID: run.SessionID, RunID: run.RunID, OwnerID: m.ownerID,
		TurnID: run.TurnID, FencingToken: run.FencingToken,
	}.normalized()
	terminal, err := m.finalizeLedgerRunFromState(ctx, handle, RunStatusLost, runErrorBackendLost, ledger.StateWaitingDecision)
	if terminal.RunID != "" {
		terminal.Cause = cause
		m.cancelUnfinalizedDecisions(ctx, terminal)
		m.reconcileAndObserveTerminalRun(ctx, terminal)
	}
	return err
}

// pendingRecoverableDecisions is shared by graceful handoff and lease recovery
// so shutdown cannot preserve a decision that a successor cannot continue.
func pendingRecoverableDecisions(ctx context.Context, decisions DecisionStore, run ledger.Run) ([]runtimefence.PreservedDecision, error) {
	targets, err := decisions.PendingRuntimeDecisions(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	// Only a native parked run is worth reclaiming: its decision
	// continuation is rebuilt from the database by the answering pipeline.
	// An inline waiter run (codex, claude, ACP gateway tools) blocked its
	// turn inside the dead owner's process; preserving its decisions would
	// fake a resumable run whose answers have nowhere to go. Declining here
	// lets the reaper's default path mark the run lost — answers then land
	// on the honest CanRespond=false cancellation.
	for _, target := range targets {
		if target.InlineDecision || runtimekind.UsesDecisionWaiter(target.SessionRuntime) {
			return nil, nil
		}
	}
	preserved := make([]runtimefence.PreservedDecision, 0, len(targets))
	for i, target := range targets {
		target = target.normalized()
		targets[i] = target
		if !target.runtimeOwned() || target.RunID != run.RunID || target.TurnID != run.TurnID ||
			target.BotID != run.BotID || target.SessionID != run.SessionID ||
			target.FencingToken != run.FencingToken {
			return nil, ErrCommandTargetMismatch
		}
		decisionKind, kindErr := recoveryDecisionKind(target.Type)
		if kindErr != nil {
			return nil, kindErr
		}
		preserved = append(preserved, runtimefence.PreservedDecision{Kind: decisionKind, ID: target.ID})
	}

	return preserved, nil
}

func recoveryDecisionKind(commandType string) (string, error) {
	switch strings.TrimSpace(commandType) {
	case CommandToolApprovalResponse:
		return runtimefence.DecisionToolApproval, nil
	case CommandUserInputResponse:
		return runtimefence.DecisionUserInput, nil
	default:
		return "", fmt.Errorf("unsupported recovered runtime decision command %q", commandType)
	}
}

// reserveRecoveredWaitingDecision reserves the run for this owner. A live
// view the backend kept is shown as it was; otherwise the run is shown from
// messages, what it persisted. Either way this owner's converter continues
// from the view, so the continuation extends it rather than overwriting it.
func (m *Manager) reserveRecoveredWaitingDecision(ctx context.Context, run ledger.Run, messages []chatview.UIMessage) error {
	if m == nil || m.backend == nil {
		return errors.New("waiting-decision recovery requires a live backend")
	}
	now, err := m.backend.Now(ctx)
	if err != nil {
		return fmt.Errorf("load waiting-decision recovery time: %w", err)
	}
	generation := m.newGeneration()
	handle := RunHandle{
		BotID: run.BotID, SessionID: run.SessionID, RunID: run.RunID, OwnerID: m.ownerID,
		TurnID: run.TurnID, Generation: generation, FencingToken: run.FencingToken,
	}.normalized()
	lifecycleBase := runtimefence.WithContext(context.WithoutCancel(ctx), runtimefence.Fence{
		BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken,
	})
	lifecycleCtx, lifecycleCancel := context.WithCancel(lifecycleBase)
	ctrl := &runControl{
		botID: handle.BotID, sessionID: handle.SessionID, runID: handle.RunID,
		ownerID: m.ownerID,
		turnID:  handle.TurnID, generation: handle.Generation, fencingToken: handle.FencingToken,
		lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel,
		converter:    chatview.NewUIMessageStreamConverter(),
		leaseChanged: make(chan struct{}, 1),
		ready:        make(chan struct{}),
	}
	_ = ctrl.completeAdmissionForAbort()
	m.mu.Lock()
	if m.isClosed() {
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return ErrManagerClosed
	}
	if _, exists := m.controls[ctrl.key()]; exists {
		m.mu.Unlock()
		ctrl.revokeOwnership(ErrRunOwnershipLost)
		return errors.New("recovered runtime run is already locally owned")
	}
	m.controls[ctrl.key()] = ctrl
	m.mu.Unlock()

	// A memory run has no lease and its view names no owner, as at admission.
	ownerID := ""
	var leaseExpiresAt *time.Time
	if m.distributed != nil {
		ownerID = m.ownerID
		expiresAt := now.Add(m.ownerLeaseTTL)
		leaseExpiresAt = &expiresAt
		ctrl.setLeaseValidUntil(time.Now().Add(m.ownerLeaseTTL))
	}
	key := handle.key()
	epoch := m.newEpoch()
	snapshot, changed, err := m.reserveLiveRun(ctx, key, RunRef{
		BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID,
		OwnerID: m.ownerID, Generation: handle.Generation, FencingToken: handle.FencingToken,
	}, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
		if !ok {
			snapshot = EmptySnapshot(handle.BotID, handle.SessionID)
		}
		if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.RunID != handle.RunID &&
			isActiveRunStatus(snapshot.CurrentRunView.Status) {
			return snapshot, false, errors.New("another runtime run is active during decision recovery")
		}
		snapshot.BotID = handle.BotID
		snapshot.SessionID = handle.SessionID
		if strings.TrimSpace(snapshot.Epoch) == "" {
			snapshot.Epoch = epoch
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.RunID != handle.RunID {
			restored, err := cloneUIMessages(messages)
			if err != nil {
				return snapshot, false, err
			}
			if restored == nil {
				restored = []chatview.UIMessage{}
			}
			snapshot.CurrentRunView = &CurrentRunView{
				RunID: handle.RunID, TurnID: run.TurnID, TurnPosition: run.TurnPosition,
				InvocationID: run.InvocationID,
				StartedAt:    run.CreatedAt, Messages: restored,
			}
		}
		view := snapshot.CurrentRunView
		view.RunID = handle.RunID
		view.TurnID = run.TurnID
		view.TurnPosition = run.TurnPosition
		view.Generation = handle.Generation
		view.FencingToken = handle.FencingToken
		view.Status = RunStatusWaitingDecision
		view.OwnerID = ownerID
		view.OwnerLeaseExpiresAt = leaseExpiresAt
		view.UpdatedAt = now
		return snapshot, true, nil
	})
	if err != nil || !changed {
		m.removeLocalControl(handle.RunID, ctrl)
		if err == nil {
			err = errors.New("recovered runtime run could not be reserved")
		}
		return err
	}
	ctrl.converter.Resume(snapshot.CurrentRunView.Messages)
	ctrl.markReady()
	// The previous producer is gone, so this owner has no local terminal write
	// to wait for before reconstructing and continuing the durable decision.
	ctrl.markDecisionReady()
	m.startLeaseRenewal(context.WithoutCancel(ctx), ctrl)
	if err := m.publishRuntimeDelta(context.WithoutCancel(ctx), snapshot, handle.RunID, RuntimeDelta{
		CurrentRunView: snapshot.CurrentRunView,
	}); err != nil {
		m.logger.WarnContext(ctx, "publish recovered waiting-decision checkpoint failed",
			slog.Any("error", err), slog.String("run_id", handle.RunID))
	}
	return nil
}

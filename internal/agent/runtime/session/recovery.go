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

// recoverWaitingDecision transfers a parked run whose owner lease expired.
// PostgreSQL is advanced first and the old Redis lease-index member remains
// until reserveRecoveredWaitingDecision succeeds. That old member is the retry
// pointer if this process dies between the two stores.
func (m *Manager) recoverWaitingDecision(ctx context.Context, candidate LeaseCandidate) (bool, error) {
	if m == nil || m.distributed == nil || m.runs == nil {
		return false, nil
	}
	m.mu.Lock()
	decisions := m.decisionStore
	m.mu.Unlock()
	reclaimer, ok := m.fence.(DecisionFenceActivator)
	if decisions == nil {
		return false, errors.New("waiting-decision recovery store is not configured")
	}
	if !ok {
		return false, errors.New("waiting-decision recovery fence is not configured")
	}

	run, err := m.runs.Get(ctx, candidate.RunID)
	if err != nil {
		if errors.Is(err, ledger.ErrRunNotFound) {
			return false, nil
		}
		return false, err
	}
	if run.State != ledger.StateWaitingDecision {
		return false, nil
	}
	targets, err := decisions.PendingRuntimeDecisions(ctx, run.RunID)
	if err != nil {
		return false, err
	}
	if len(targets) == 0 {
		return false, nil
	}
	// Only a native parked run is worth reclaiming: its decision
	// continuation is rebuilt from the database by the answering pipeline.
	// An inline waiter run (codex, claude, ACP gateway tools) blocked its
	// turn inside the dead owner's process; preserving its decisions would
	// fake a resumable run whose answers have nowhere to go. Declining here
	// lets the reaper's default path mark the run lost — answers then land
	// on the honest CanRespond=false cancellation.
	if runtimekind.UsesDecisionWaiter(targets[0].SessionRuntime) {
		return false, nil
	}
	preserved := make([]runtimefence.PreservedDecision, 0, len(targets))
	for i, target := range targets {
		target = target.normalized()
		targets[i] = target
		if !target.runtimeOwned() || target.RunID != run.RunID || target.TurnID != run.TurnID ||
			target.BotID != run.BotID || target.SessionID != run.SessionID ||
			target.FencingToken != run.FencingToken {
			return false, ErrCommandTargetMismatch
		}
		decisionKind, kindErr := recoveryDecisionKind(target.Type)
		if kindErr != nil {
			return false, kindErr
		}
		preserved = append(preserved, runtimefence.PreservedDecision{Kind: decisionKind, ID: target.ID})
	}

	ref, live, err := m.distributed.LoadRunRef(ctx, Key{BotID: run.BotID, SessionID: run.SessionID}, run.RunID)
	if err != nil {
		return false, err
	}
	if live && ref.FencingToken == run.FencingToken {
		// A previous pass completed the reclaim and only its stale, older index
		// member remains. Do not steal a live successor merely to clean it up.
		return true, nil
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
	if err := m.reserveRecoveredWaitingDecision(ctx, run); err != nil {
		return false, err
	}
	return true, nil
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

func (m *Manager) reserveRecoveredWaitingDecision(ctx context.Context, run ledger.Run) error {
	if m == nil || m.distributed == nil {
		return errors.New("waiting-decision recovery requires a distributed backend")
	}
	now, err := m.backend.Now(ctx)
	if err != nil {
		return fmt.Errorf("load waiting-decision recovery time: %w", err)
	}
	generation := m.newGeneration()
	handle := RunHandle{
		BotID: run.BotID, SessionID: run.SessionID, RunID: run.RunID,
		TurnID: run.TurnID, Generation: generation, FencingToken: run.FencingToken,
	}.normalized()
	lifecycleBase := runtimefence.WithContext(context.WithoutCancel(ctx), runtimefence.Fence{
		BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken,
	})
	lifecycleCtx, lifecycleCancel := context.WithCancel(lifecycleBase)
	ctrl := &runControl{
		botID: handle.BotID, sessionID: handle.SessionID, runID: handle.RunID,
		turnID: handle.TurnID, generation: handle.Generation, fencingToken: handle.FencingToken,
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

	localLeaseStarted := time.Now()
	ctrl.setLeaseValidUntil(localLeaseStarted.Add(m.ownerLeaseTTL))
	expiresAt := now.Add(m.ownerLeaseTTL)
	key := handle.key()
	epoch := m.newEpoch()
	snapshot, changed, err := m.distributed.StartRun(ctx, key, RunRef{
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
			snapshot.CurrentRunView = &CurrentRunView{
				RunID: handle.RunID, TurnID: run.TurnID,
				InvocationID: run.InvocationID,
				StartedAt:    run.CreatedAt, Messages: []chatview.UIMessage{},
			}
		}
		view := snapshot.CurrentRunView
		view.RunID = handle.RunID
		view.TurnID = run.TurnID
		view.Generation = handle.Generation
		view.Status = RunStatusWaitingDecision
		view.OwnerID = m.ownerID
		view.OwnerLeaseExpiresAt = &expiresAt
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
	ctrl.markReady()
	// The previous producer is gone, so this owner has no local terminal write
	// to wait for before reconstructing and continuing the durable decision.
	ctrl.markDecisionReady()
	m.startLeaseRenewal(context.WithoutCancel(ctx), ctrl)
	if err := m.publishRuntimeDelta(context.WithoutCancel(ctx), snapshot, handle.RunID, RuntimeDelta{
		CurrentRunView: snapshot.CurrentRunView,
	}); err != nil {
		m.logger.Warn("publish recovered waiting-decision checkpoint failed",
			slog.Any("error", err), slog.String("run_id", handle.RunID))
	}
	return nil
}

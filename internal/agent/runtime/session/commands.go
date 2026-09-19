package sessionruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

func (m *Manager) RunRef(ctx context.Context, botID, sessionID, runID string) (RunRef, bool, error) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	if m == nil || m.backend == nil || botID == "" || sessionID == "" || runID == "" {
		return RunRef{}, false, nil
	}
	if ctrl := m.localControlForScope(botID, sessionID, runID); ctrl != nil {
		ownerID := ""
		if m.distributed != nil {
			ownerID = m.ownerID
		}
		return RunRef{BotID: ctrl.botID, SessionID: ctrl.sessionID, RunID: ctrl.runID, OwnerID: ownerID, Generation: ctrl.generation}, true, nil
	}
	if m.distributed == nil {
		return RunRef{}, false, nil
	}
	return m.distributed.LoadRunRef(ctx, Key{BotID: botID, SessionID: sessionID}, runID)
}

func runHandleForCommand(cmd Command) RunHandle {
	return RunHandle{
		BotID: cmd.BotID, SessionID: cmd.SessionID, RunID: cmd.RunID,
		Generation: cmd.Generation, FencingToken: cmd.FencingToken,
	}.normalized()
}

// DecisionContinuationContext detaches the model continuation from the short
// command acknowledgement deadline while keeping it tied to the run owner's
// lifecycle and persistence fence.
func (m *Manager) DecisionContinuationContext(cmd Command) (context.Context, context.CancelFunc, RunHandle, error) {
	ctrl := m.localControlForScope(cmd.BotID, cmd.SessionID, cmd.RunID)
	if ctrl == nil || ctrl.generation != strings.TrimSpace(cmd.Generation) || !ctrl.commandsActive() {
		return nil, func() {}, RunHandle{}, ErrCommandTargetNotActive
	}
	// The acknowledgement request ends before the continuation. Only the run
	// lifecycle owns this context, so no transport cancellation is attached.
	ctx, cancel := ctrl.commandContext(context.Background())
	handle := ctrl.handle()
	if err := m.ValidateRunOwnership(ctx, handle); err != nil {
		cancel()
		return nil, func() {}, RunHandle{}, err
	}
	return ctx, cancel, handle, nil
}

// WaitDecisionContinuationReady holds the resumed model call until the stream
// that produced the deferred decision has finished its terminal persistence.
// The decision itself is already committed and acknowledged; this barrier only
// prevents its tool result from racing the assistant tool-call write.
func (m *Manager) WaitDecisionContinuationReady(ctx context.Context, cmd Command) error {
	ctrl := m.localControlForScope(cmd.BotID, cmd.SessionID, cmd.RunID)
	if ctrl == nil || ctrl.generation != strings.TrimSpace(cmd.Generation) || !ctrl.commandsActive() {
		return ErrCommandTargetNotActive
	}
	ready := ctrl.decisionReadySignal()
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return m.ValidateRunOwnership(ctx, runHandleForCommand(cmd))
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ValidateRunOwnership fails closed before durable side effects when this
// process no longer owns the active runtime run.
func (m *Manager) ValidateRunOwnership(ctx context.Context, handle RunHandle) error {
	if m == nil || m.backend == nil {
		return errors.New("session runtime manager is not configured")
	}
	handle = handle.normalized()
	if !handle.valid() {
		return ErrRunOwnershipLost
	}
	ctrl := m.localControlForHandle(handle)
	if ctrl == nil {
		return ErrRunOwnershipLost
	}
	key := handle.key()
	if m.distributed == nil {
		snapshot, ok, err := m.backend.Load(ctx, key)
		if err != nil {
			return fmt.Errorf("validate runtime ownership: %w", err)
		}
		if !ok || !runMatchesHandle(snapshot.CurrentRunView, handle) || !m.runOwnerMatches(snapshot.CurrentRunView) || !isActiveRunStatus(snapshot.CurrentRunView.Status) {
			return ErrRunOwnershipLost
		}
		return nil
	}
	if !ctrl.leaseIsValidAt(time.Now()) {
		return ErrRunOwnershipLost
	}
	ref := RunRef{BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, OwnerID: m.ownerID, Generation: handle.Generation}
	if err := m.distributed.ValidateRunOwnership(ctx, key, ref); err != nil {
		if errors.Is(err, ErrRunOwnershipLost) {
			return ErrRunOwnershipLost
		}
		return fmt.Errorf("validate runtime ownership: %w", err)
	}
	// A Redis round trip can consume the final part of the conservative local
	// lease window. Recheck after the atomic server-side decision before any
	// durable side effect begins.
	if !ctrl.leaseIsValidAt(time.Now()) || m.localControlForHandle(handle) != ctrl {
		return ErrRunOwnershipLost
	}
	return nil
}

func (m *Manager) Abort(ctx context.Context, botID, sessionID, runID string) (bool, error) {
	return m.abort(ctx, botID, sessionID, runID, "")
}

// AbortControl aborts a run on behalf of a client that named the request with
// its own control id. Two things follow from that id and neither is available
// to plain Abort:
//
// The abort becomes idempotent across instances. A client that retries — or
// that reconnects to a different server and retries there — gets the answer the
// first attempt produced rather than a second execution, because the control id
// keys the shared command result. Without it a retry after the run terminalized
// would report "not active" and contradict the ack the client already holds.
//
// The intent is recorded durably before anything is routed, so a run that is
// aborted survives as aborted-on-purpose rather than as an unexplained
// cancellation, even if the owner never answers (SR-CTL-001).
func (m *Manager) AbortControl(ctx context.Context, botID, sessionID, runID, controlID string) (bool, error) {
	controlID = strings.TrimSpace(controlID)
	if controlID == "" {
		return m.abort(ctx, botID, sessionID, runID, "")
	}
	commandID := abortControlCommandID(strings.TrimSpace(sessionID), strings.TrimSpace(runID), controlID)
	if applied, replayed, err := m.replayControlResult(ctx, commandID); replayed {
		return applied, err
	}
	m.recordAbortIntent(ctx, strings.TrimSpace(runID))
	applied, err := m.abortWithCommandID(ctx, botID, sessionID, runID, "", commandID)
	m.rememberControlResult(ctx, commandID, botID, sessionID, runID, err)
	return applied, err
}

// abortControlCommandID namespaces the client's control id by the run it
// addresses. A control id is only unique to the client that minted it, so
// keying the shared result on it alone would let two clients collide.
func abortControlCommandID(sessionID, runID, controlID string) string {
	return "abort:" + sessionID + ":" + runID + ":" + controlID
}

// replayControlResult answers from the shared command result when this control
// id was already resolved. It runs before the run is resolved at all: once a run
// terminalizes its live reference is gone, so a retry that looked the run up
// first would report it missing instead of replaying the abort that ended it.
func (m *Manager) replayControlResult(ctx context.Context, commandID string) (bool, bool, error) {
	if m == nil || m.distributed == nil {
		return false, false, nil
	}
	stored, ok, err := m.distributed.LoadCommandResult(ctx, commandID)
	if err != nil || !ok {
		return false, false, nil
	}
	if resultErr := commandResultError(stored); resultErr != nil {
		return false, true, resultErr
	}
	return true, true, nil
}

func (m *Manager) rememberControlResult(ctx context.Context, commandID, botID, sessionID, runID string, err error) {
	// Only a successful transition is safe to pin: a transport failure says
	// nothing about the run, and pinning it would make a retry replay the
	// failure forever instead of reaching the owner.
	if err != nil || m == nil || m.distributed == nil {
		return
	}
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.commandTimeout())
	defer cancel()
	result := Command{
		Type:      CommandResult,
		ID:        commandID,
		BotID:     strings.TrimSpace(botID),
		SessionID: strings.TrimSpace(sessionID),
		RunID:     strings.TrimSpace(runID),
	}
	if storeErr := m.distributed.StoreCommandResult(storeCtx, result, m.commandResultTTL()); storeErr != nil {
		m.logger.Warn("store runtime abort control result failed",
			slog.Any("error", storeErr), slog.String("command_id", commandID))
	}
}

// recordAbortIntent persists that a human asked for this run to stop. It is
// best effort on purpose: the ledger row is the record of intent, not the
// mechanism, so failing to write it must not stop the abort from reaching the
// owner that can actually honour it.
func (m *Manager) recordAbortIntent(ctx context.Context, runID string) {
	if m == nil || m.runs == nil || runID == "" {
		return
	}
	if _, _, err := m.runs.RequestAbort(ctx, runID); err != nil {
		m.logger.Warn("record runtime abort intent failed",
			slog.Any("error", err), slog.String("run_id", runID))
	}
}

func (m *Manager) AbortRun(ctx context.Context, handle RunHandle) (bool, error) {
	handle = handle.normalized()
	if !handle.valid() {
		return false, ErrRunOwnershipLost
	}
	return m.abort(ctx, handle.BotID, handle.SessionID, handle.RunID, handle.Generation)
}

func (m *Manager) abort(ctx context.Context, botID, sessionID, runID, expectedGeneration string) (bool, error) {
	return m.abortWithCommandID(ctx, botID, sessionID, runID, expectedGeneration, "")
}

// abortWithCommandID is abort with the routed command's identity supplied. A
// caller-stable id is what makes a retry resolve to the first attempt's result
// instead of executing twice; an empty one falls back to a fresh id.
func (m *Manager) abortWithCommandID(ctx context.Context, botID, sessionID, runID, expectedGeneration, commandID string) (bool, error) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	runID = strings.TrimSpace(runID)
	expectedGeneration = strings.TrimSpace(expectedGeneration)
	if m == nil || m.backend == nil || botID == "" || sessionID == "" || runID == "" {
		return false, nil
	}
	if ctrl := m.localControlForScope(botID, sessionID, runID); ctrl != nil {
		if ctrl.botID != botID || ctrl.sessionID != sessionID {
			return false, ErrCommandTargetMismatch
		}
		if expectedGeneration != "" && ctrl.generation != expectedGeneration {
			if m.distributed == nil {
				return false, ErrRunOwnershipLost
			}
		} else {
			aborted, abortErr := m.abortLocal(ctx, ctrl)
			if expectedGeneration != "" || abortErr == nil && aborted || !errors.Is(abortErr, ErrCommandTargetNotActive) {
				return aborted, abortErr
			}
			// A generationless request may have reached a server whose stale
			// local control has already lost its lease. Resolve the current
			// owner below instead of letting that stale control shadow it.
		}
	}
	if m.distributed == nil {
		return false, nil
	}
	ref, ok, err := m.distributed.LoadRunRef(ctx, Key{BotID: botID, SessionID: sessionID}, runID)
	if err != nil || !ok || strings.TrimSpace(ref.OwnerID) == "" {
		return false, err
	}
	if ref.BotID != botID || ref.SessionID != sessionID {
		return false, ErrCommandTargetMismatch
	}
	if expectedGeneration != "" && ref.Generation != expectedGeneration {
		return false, ErrRunOwnershipLost
	}
	createdAt, err := m.backend.Now(ctx)
	if err != nil {
		return false, fmt.Errorf("load runtime command time: %w", err)
	}
	if strings.TrimSpace(commandID) == "" {
		commandID = "abort-" + uuid.NewString()
	}
	cmd := Command{
		Type:       CommandAbort,
		ID:         commandID,
		BotID:      ref.BotID,
		SessionID:  ref.SessionID,
		RunID:      ref.RunID,
		Generation: ref.Generation,
		CreatedAt:  createdAt,
		ExpiresAt:  createdAt.Add(m.commandTimeout()),
	}
	if err := m.dispatchRemoteCommand(ctx, ref.OwnerID, cmd); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) abortLocal(ctx context.Context, ctrl *runControl) (bool, error) {
	if ctrl == nil || m.localControlForHandle(ctrl.handle()) != ctrl {
		return false, ErrCommandTargetNotActive
	}
	waitingDecision := false
	if snapshot, ok, err := m.backend.Load(ctx, Key{BotID: ctrl.botID, SessionID: ctrl.sessionID}); err == nil && ok &&
		runMatchesHandle(snapshot.CurrentRunView, ctrl.handle()) {
		waitingDecision = strings.EqualFold(snapshot.CurrentRunView.Status, RunStatusWaitingDecision)
	}
	phase, abortOwner := ctrl.requestAbortPhase()
	if phase != runAbortPhasePreClaim && m.distributed != nil && !ctrl.leaseIsValidAt(time.Now()) {
		m.cancelRunControl(ctrl)
		return false, ErrCommandTargetNotActive
	}
	if phase == runAbortPhasePreClaim {
		if acknowledged, err := m.requestAbort(ctx, ctrl); err != nil && !errors.Is(err, ErrRunOwnershipLost) && !errors.Is(err, ErrCommandTargetNotActive) {
			return false, err
		} else if acknowledged {
			if ctrl.claimVisibleAndTakeAbort() {
				if err := m.abortClaimedAdmission(context.WithoutCancel(ctx), ctrl); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		if claimed, owner := ctrl.takeAbortIfClaimed(); claimed {
			if owner {
				if err := m.abortClaimedAdmission(context.WithoutCancel(ctx), ctrl); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		select {
		case ctrl.abortCh <- struct{}{}:
		default:
		}
		if ctrl.admissionCancel != nil {
			ctrl.admissionCancel()
		}
		if ctrl.cancel != nil {
			ctrl.cancel()
		}
		return true, nil
	}
	if phase == runAbortPhaseAdmission {
		if abortOwner {
			if err := m.abortClaimedAdmission(context.WithoutCancel(ctx), ctrl); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	acknowledged, err := m.requestAbort(ctx, ctrl)
	if err != nil {
		return false, err
	}
	if !acknowledged {
		return false, ErrCommandTargetNotActive
	}
	ctrl.stopCommands()
	select {
	case ctrl.abortCh <- struct{}{}:
	default:
	}
	if ctrl.cancel != nil {
		ctrl.cancel()
	}
	if waitingDecision && !ctrl.hasInlineDecision() {
		// A native parked run has no live stream to observe the cancel; the
		// terminal write must happen here. An inline runtime's turn is still
		// alive blocked on its waiter — the cancel unwinds it and the turn's
		// own FinishRun records the abort AFTER the driver actually returns,
		// so the ledger's terminal state is a truthful "driver stopped"
		// signal for deletion barriers on any instance.
		if err := m.FinishRun(context.WithoutCancel(ctx), ctrl.handle(), RunStatusAborted, ""); err != nil {
			return false, err
		}
	}
	return true, nil
}

const (
	runAbortPhasePreClaim = iota
	runAbortPhaseAdmission
	runAbortPhaseRunning
)

func (c *runControl) requestAbortPhase() (int, bool) {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	c.abortRequested = true
	if c.admissionComplete {
		return runAbortPhaseRunning, false
	}
	if !c.claimEstablished {
		return runAbortPhasePreClaim, false
	}
	if !c.abortFinalizing {
		c.abortFinalizing = true
		return runAbortPhaseAdmission, true
	}
	return runAbortPhaseAdmission, false
}

func (c *runControl) establishClaimForAbort() (bool, bool) {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	c.claimEstablished = true
	if !c.abortRequested {
		return false, false
	}
	if !c.abortFinalizing {
		c.abortFinalizing = true
		return true, true
	}
	return true, false
}

func (c *runControl) claimVisibleAndTakeAbort() bool {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	c.claimEstablished = true
	if c.abortFinalizing {
		return false
	}
	c.abortFinalizing = true
	return true
}

func (c *runControl) takeAbortIfClaimed() (bool, bool) {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	if !c.claimEstablished {
		return false, false
	}
	if !c.abortFinalizing {
		c.abortFinalizing = true
		return true, true
	}
	return true, false
}

func (c *runControl) completeAdmissionForAbort() bool {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	if c.abortRequested || c.abortFinalizing {
		return false
	}
	c.admissionComplete = true
	return true
}

func (c *runControl) abortWasRequested() bool {
	c.abortStateMu.Lock()
	defer c.abortStateMu.Unlock()
	return c.abortRequested
}

func (m *Manager) abortClaimedAdmission(ctx context.Context, ctrl *runControl) error {
	if ctrl == nil || m.localControlForHandle(ctrl.handle()) != ctrl {
		return ErrCommandTargetNotActive
	}
	acknowledged, err := m.requestAbort(ctx, ctrl)
	if err != nil {
		return err
	}
	if !acknowledged {
		return ErrCommandTargetNotActive
	}
	ctrl.stopCommands()
	select {
	case ctrl.abortCh <- struct{}{}:
	default:
	}
	if ctrl.cancel != nil {
		ctrl.cancel()
	}
	ctrl.markReady()
	return m.FinishRun(ctx, ctrl.handle(), RunStatusAborted, "")
}

// RouteDecisionResponse is the single decision entry point for WebSocket, HTTP,
// and gRPC callers. PostgreSQL resolves the decision before live ownership is
// consulted, while the client control id is checked first so a successful
// command remains replayable after the run terminalizes.
func (m *Manager) RouteDecisionResponse(ctx context.Context, response DecisionResponse) (DecisionResponseResult, error) {
	if m == nil || m.backend == nil {
		return DecisionResponseResult{}, nil
	}
	response.ControlID = strings.TrimSpace(response.ControlID)
	response.Type = strings.TrimSpace(response.Type)
	response.DecisionID = strings.TrimSpace(response.DecisionID)
	response.BotID = strings.TrimSpace(response.BotID)
	response.SessionID = strings.TrimSpace(response.SessionID)
	response.RunID = strings.TrimSpace(response.RunID)
	if response.ControlID == "" {
		return DecisionResponseResult{}, errors.New("decision control_id is required")
	}
	if response.DecisionID == "" || response.BotID == "" {
		return DecisionResponseResult{}, errors.New("decision_id and bot_id are required")
	}
	if response.Type != CommandToolApprovalResponse && response.Type != CommandUserInputResponse {
		return DecisionResponseResult{}, fmt.Errorf("unsupported runtime decision command %q", response.Type)
	}

	commandID := decisionControlCommandID(response.Type, response.BotID, response.DecisionID, response.ControlID)
	requestHash := decisionResponsePayloadHash(response.Type, response.DecisionID, response.Payload)
	if stored, ok, err := m.loadCommandResult(ctx, commandID); err != nil {
		return DecisionResponseResult{Handled: true}, err
	} else if ok {
		err := commandResultErrorFor(Command{PayloadHash: requestHash}, stored)
		return DecisionResponseResult{Handled: true, Applied: err == nil, Replayed: true}, err
	}

	m.mu.Lock()
	decisions := m.decisionStore
	m.mu.Unlock()
	if decisions == nil {
		return DecisionResponseResult{}, nil
	}
	target, err := decisions.ResolveRuntimeDecision(ctx, response.Type, response.DecisionID)
	if err != nil {
		return DecisionResponseResult{}, err
	}
	target = target.normalized()
	if !target.runtimeOwned() {
		// ACP/MCP and other unfenced decisions retain their waiter-backed path.
		return DecisionResponseResult{}, nil
	}
	result := DecisionResponseResult{Handled: true, RunID: target.RunID, SessionID: target.SessionID}
	if target.Type != response.Type ||
		target.BotID != response.BotID ||
		response.SessionID != "" && target.SessionID != response.SessionID ||
		response.RunID != "" && target.RunID != response.RunID {
		return result, ErrCommandTargetMismatch
	}
	if target.ControlID != "" {
		if target.ControlID == response.ControlID {
			if target.PayloadHash != requestHash {
				return result, ErrCommandPayloadConflict
			}
			return DecisionResponseResult{Handled: true, Applied: true, Replayed: true}, nil
		}
		return result, nil
	}
	if !strings.EqualFold(target.Status, "pending") {
		return result, nil
	}
	if m.runs == nil {
		return result, ErrLedgerUnavailable
	}
	run, err := m.runs.Get(ctx, target.RunID)
	if err != nil {
		if errors.Is(err, ledger.ErrRunNotFound) {
			return result, nil
		}
		return result, fmt.Errorf("load decision runtime run: %w", err)
	}
	if run.BotID != target.BotID || run.SessionID != target.SessionID ||
		run.TurnID != target.TurnID || run.FencingToken != target.FencingToken {
		return result, ErrCommandTargetMismatch
	}
	if run.State != ledger.StateWaitingDecision {
		return result, nil
	}

	ref, ok, err := m.decisionRunRef(ctx, target)
	if err != nil {
		return result, err
	}
	if !ok || strings.TrimSpace(ref.OwnerID) == "" && m.distributed != nil {
		return result, ErrCommandOwnerUnavailable
	}
	result.Generation = ref.Generation
	createdAt, err := m.backend.Now(ctx)
	if err != nil {
		return result, fmt.Errorf("load runtime command time: %w", err)
	}
	cmd := Command{
		Type: response.Type, ID: commandID,
		BotID: target.BotID, SessionID: target.SessionID, RunID: target.RunID,
		Generation: ref.Generation, FencingToken: target.FencingToken,
		TargetID: target.ID, DecisionResolved: true, StreamOutput: response.streamOutput,
		Payload: append([]byte(nil), response.Payload...), PayloadHash: requestHash,
		CreatedAt: createdAt, ExpiresAt: createdAt.Add(m.commandTimeout()),
	}
	if m.distributed == nil || ref.OwnerID == m.ownerID {
		stored := m.executeRoutedCommand(ctx, cmd)
		err := commandResultErrorFor(cmd, stored)
		result.Applied = err == nil
		return result, err
	}
	err = m.dispatchRemoteCommand(ctx, ref.OwnerID, cmd)
	result.Applied = err == nil
	return result, err
}

func decisionControlCommandID(commandType, botID, decisionID, controlID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(commandType),
		strings.TrimSpace(botID),
		strings.TrimSpace(decisionID),
		strings.TrimSpace(controlID),
	}, "\x00")))
	return fmt.Sprintf("decision-control-%x", sum[:])
}

func decisionResponsePayloadHash(commandType, decisionID string, payload []byte) string {
	semantic := activeCommandPayloadHash(commandType, payload)
	return commandPayloadHash([]byte(strings.Join([]string{
		strings.TrimSpace(commandType),
		strings.TrimSpace(decisionID),
		semantic,
	}, "\x00")))
}

func (m *Manager) decisionRunRef(ctx context.Context, target DecisionTarget) (RunRef, bool, error) {
	key := Key{BotID: target.BotID, SessionID: target.SessionID}
	if m.distributed != nil {
		return m.distributed.LoadRunRef(ctx, key, target.RunID)
	}
	snapshot, ok, err := m.backend.Load(ctx, key)
	if err != nil || !ok || snapshot.CurrentRunView == nil {
		return RunRef{}, false, err
	}
	run := snapshot.CurrentRunView
	if strings.TrimSpace(run.RunID) != target.RunID || !isActiveRunStatus(run.Status) {
		return RunRef{}, false, nil
	}
	return RunRef{
		BotID: target.BotID, SessionID: target.SessionID, RunID: target.RunID,
		Generation: strings.TrimSpace(run.Generation), FencingToken: target.FencingToken,
	}, true, nil
}

func (m *Manager) dispatchRemoteCommand(ctx context.Context, ownerID string, cmd Command) error {
	ownerID = strings.TrimSpace(ownerID)
	cmd.ID = strings.TrimSpace(cmd.ID)
	if ownerID == "" {
		return errors.New("target runtime owner is unknown")
	}
	if cmd.ID == "" {
		return errors.New("runtime command id is required")
	}
	cmd.ReplyOwnerID = m.ownerID
	waiter := &commandWaiter{result: make(chan error, 1), payloadHash: cmd.PayloadHash}
	m.mu.Lock()
	if m.isClosed() {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	waiters := m.pendingCommands[cmd.ID]
	if waiters == nil {
		waiters = make(map[*commandWaiter]struct{})
		m.pendingCommands[cmd.ID] = waiters
	}
	waiters[waiter] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if waiters := m.pendingCommands[cmd.ID]; waiters != nil {
			delete(waiters, waiter)
			if len(waiters) == 0 {
				delete(m.pendingCommands, cmd.ID)
			}
		}
		m.mu.Unlock()
	}()
	if err := m.distributed.PublishCommand(ctx, ownerID, cmd); err != nil {
		return err
	}
	return m.waitCommandResult(ctx, cmd, waiter.result, m.commandTimeout(), ownerID)
}

func commandPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func activeCommandPayloadHash(commandType string, payload []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return commandPayloadHash(payload)
	}
	canonical := make(map[string]any)
	switch commandType {
	case CommandToolApprovalResponse:
		decision := strings.ToLower(runtimeCommandString(runtimeCommandMapValue(raw, "decision")))
		switch decision {
		case "approve", "approved":
			decision = "approved"
		case "reject", "rejected":
			decision = "rejected"
		}
		canonical["decision"] = decision
		// Zero values stay out of the canonical form so hashes of pre-option_id
		// payloads keep matching rows persisted by earlier binaries; only a real
		// selection adds new semantics (and therefore a new hash).
		if optionID := runtimeCommandExactString(runtimeCommandMapValue(raw, "option_id")); strings.TrimSpace(optionID) != "" {
			canonical["option_id"] = optionID
		}
		canonical["reason"] = runtimeCommandString(runtimeCommandMapValue(raw, "reason"))
	case CommandUserInputResponse:
		canceled, _ := runtimeCommandMapValue(raw, "canceled").(bool)
		canonical["canceled"] = canceled
		if canceled {
			canonical["reason"] = runtimeCommandString(runtimeCommandMapValue(raw, "reason"))
			break
		}
		answers := canonicalRuntimeAnswers(runtimeCommandMapValue(raw, "answers"))
		if len(answers) > 0 {
			canonical["answers"] = answers
		} else {
			canonical["text_answer"] = runtimeCommandString(runtimeCommandMapValue(raw, "text_answer"))
		}
	default:
		return commandPayloadHash(payload)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return commandPayloadHash(payload)
	}
	return commandPayloadHash(encoded)
}

func runtimeCommandMapValue(values map[string]any, name string) any {
	want := strings.ReplaceAll(strings.ToLower(name), "_", "")
	for key, value := range values {
		if strings.ReplaceAll(strings.ToLower(key), "_", "") == want {
			return value
		}
	}
	return nil
}

func runtimeCommandString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func runtimeCommandExactString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func canonicalRuntimeAnswers(value any) []map[string]any {
	items, _ := value.([]any)
	answers := make([]map[string]any, 0, len(items))
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		optionValues, _ := runtimeCommandMapValue(raw, "option_ids").([]any)
		optionIDs := make([]string, 0, len(optionValues))
		for _, optionID := range optionValues {
			optionIDs = append(optionIDs, runtimeCommandString(optionID))
		}
		answer := map[string]any{
			"question_id": runtimeCommandString(runtimeCommandMapValue(raw, "question_id")),
			"option_ids":  optionIDs,
			"custom_text": runtimeCommandString(runtimeCommandMapValue(raw, "custom_text")),
			"text":        runtimeCommandString(runtimeCommandMapValue(raw, "text")),
		}
		// skipped=false is the legacy default; keep it out of the canonical form
		// (see the option_id note above) so only a real skip changes the hash.
		if skipped, _ := runtimeCommandMapValue(raw, "skipped").(bool); skipped {
			answer["skipped"] = true
		}
		answers = append(answers, answer)
	}
	sort.SliceStable(answers, func(i, j int) bool {
		return answers[i]["question_id"].(string) < answers[j]["question_id"].(string)
	})
	return answers
}

func (m *Manager) requestAbort(ctx context.Context, ctrl *runControl) (bool, error) {
	if ctrl == nil {
		return false, nil
	}
	acknowledged := false
	_, _, err := m.updateActiveAndPublish(ctx, ctrl.handle(), func(snapshot Snapshot, now time.Time) (Snapshot, bool, error) {
		run := snapshot.CurrentRunView
		if run == nil {
			return snapshot, false, nil
		}
		if run.RunID != ctrl.runID || !m.runOwnerMatches(run) || !isAbortableRunStatus(run.Status) {
			return snapshot, false, nil
		}
		acknowledged = true
		if strings.EqualFold(run.Status, RunStatusAborting) {
			return snapshot, false, nil
		}
		snapshot.Seq++
		snapshot.UpdatedAt = now
		run.Status = RunStatusAborting
		run.UpdatedAt = now
		return snapshot, true, nil
	}, func(snapshot Snapshot) RuntimeDelta {
		return runtimeRunPatch(snapshot, true, false, false)
	})
	return acknowledged, err
}

func (m *Manager) applyCommand(ctx context.Context, cmd Command) {
	switch strings.TrimSpace(cmd.Type) {
	case CommandAbort, CommandSteerWake, CommandToolApprovalResponse, CommandUserInputResponse, CommandHistoryReset:
		m.publishStoredCommandResult(ctx, cmd, m.executeRoutedCommand(ctx, cmd))
	case CommandResult:
		m.completePendingCommand(cmd)
	}
}

func (m *Manager) activeCommandContext(ctx context.Context, cmd Command) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lookupTimeout := m.commandTimeout()
	if strings.TrimSpace(cmd.Type) == CommandHistoryReset && !cmd.ExpiresAt.IsZero() {
		if remaining := time.Until(cmd.ExpiresAt); remaining > lookupTimeout {
			lookupTimeout = remaining
		}
	}
	lookupCtx, lookupCancel := context.WithTimeout(ctx, lookupTimeout)
	lookupStarted := time.Now()
	now, err := m.backend.Now(lookupCtx)
	lookupElapsed := time.Since(lookupStarted)
	if err != nil {
		lookupCancel()
		return nil, func() {}, fmt.Errorf("load runtime command time: %w", err)
	}
	expiresAt := cmd.ExpiresAt
	if expiresAt.IsZero() || (!cmd.CreatedAt.IsZero() && !expiresAt.After(cmd.CreatedAt)) {
		lookupCancel()
		return nil, func() {}, ErrCommandExpired
	}
	remaining := expiresAt.Sub(now) - lookupElapsed
	if remaining <= 0 {
		lookupCancel()
		return nil, func() {}, ErrCommandExpired
	}
	lookupCancel()
	commandCtx, commandCancel := context.WithTimeout(ctx, remaining)
	return commandCtx, commandCancel, nil
}

func (m *Manager) applyRoutedCommand(ctx context.Context, cmd Command) error {
	ctrl := m.localControlForScope(cmd.BotID, cmd.SessionID, cmd.RunID)
	if ctrl == nil || ctrl.botID != strings.TrimSpace(cmd.BotID) || ctrl.sessionID != strings.TrimSpace(cmd.SessionID) || ctrl.generation != strings.TrimSpace(cmd.Generation) {
		return ErrCommandTargetNotActive
	}
	commandCtx, cancel := ctrl.commandContext(ctx)
	defer cancel()
	if !ctrl.commandsActive() || commandCtx.Err() != nil {
		return ErrCommandTargetNotActive
	}
	handle := runHandleForCommand(cmd)
	if err := m.ValidateRunOwnership(commandCtx, handle); err != nil {
		if errors.Is(err, ErrRunOwnershipLost) {
			return ErrCommandTargetNotActive
		}
		return err
	}
	snapshot, ok, err := m.backend.Load(commandCtx, Key{BotID: cmd.BotID, SessionID: cmd.SessionID})
	if err != nil {
		return err
	}
	if !ok || snapshot.CurrentRunView == nil {
		return ErrCommandTargetNotActive
	}
	run := snapshot.CurrentRunView
	if run.RunID != ctrl.runID || run.Generation != ctrl.generation || !m.runOwnerMatches(run) || !isActiveRunStatus(run.Status) {
		return ErrCommandTargetNotActive
	}
	if strings.TrimSpace(cmd.Type) == CommandAbort {
		_, err := m.abortLocal(commandCtx, ctrl)
		return err
	}
	if strings.TrimSpace(cmd.Type) == CommandSteerWake {
		if !run.SteerSupported || (run.Status != RunStatusRunning && run.Status != RunStatusWaitingDecision) {
			return ErrCommandTargetNotActive
		}
		m.wakeSteer(ctrl)
		return nil
	}
	if strings.TrimSpace(cmd.Type) == CommandHistoryReset {
		return m.applyHistoryResetCommand(commandCtx, cmd, ctrl)
	}
	if !cmd.DecisionResolved {
		return ErrCommandTargetNotActive
	}
	m.mu.Lock()
	handler := m.commandHandler
	m.mu.Unlock()
	if handler == nil {
		return errors.New("runtime command handler is not configured")
	}
	if !m.beginCommandTarget(cmd) {
		return ErrCommandBusy
	}
	defer m.endCommandTarget(cmd)
	err = handler(commandCtx, cmd)
	return err
}

func (m *Manager) executeRoutedCommand(ctx context.Context, cmd Command) Command {
	leader, executionDone := m.beginCommandExecution(cmd.ID)
	if !leader {
		select {
		case <-executionDone:
		case <-ctx.Done():
			return newCommandResult(cmd, ctx.Err())
		}
		if result, ok, err := m.loadCommandResultForExecution(ctx, cmd.ID); err != nil {
			return newCommandResult(cmd, err)
		} else if ok {
			if errors.Is(commandResultErrorFor(cmd, result), ErrCommandPayloadConflict) {
				return newCommandResult(cmd, ErrCommandPayloadConflict)
			}
			return result
		}
		return newCommandResult(cmd, errors.New("runtime command result is unavailable"))
	}
	defer m.finishCommandExecution(cmd.ID, executionDone)

	if result, ok, err := m.loadCommandResultForExecution(ctx, cmd.ID); err != nil {
		return newCommandResult(cmd, err)
	} else if ok {
		if errors.Is(commandResultErrorFor(cmd, result), ErrCommandPayloadConflict) {
			return newCommandResult(cmd, ErrCommandPayloadConflict)
		}
		return result
	}
	commandCtx, cancel, err := m.activeCommandContext(ctx, cmd)
	if err == nil {
		err = m.applyRoutedCommand(commandCtx, cmd)
	}
	cancel()
	return m.persistCommandResult(ctx, cmd, err)
}

func newCommandResult(request Command, err error) Command {
	result := Command{
		Type: CommandResult, ID: request.ID, BotID: request.BotID, SessionID: request.SessionID,
		RunID: request.RunID, Generation: request.Generation, FencingToken: request.FencingToken,
		TargetID: request.TargetID, DecisionResolved: request.DecisionResolved,
		PayloadHash: request.PayloadHash, CreatedAt: time.Now().UTC(),
	}
	if err == nil {
		return result
	}
	result.Error = err.Error()
	switch {
	case errors.Is(err, ErrCommandTargetNotActive):
		result.ErrorCode = "target_not_active"
	case errors.Is(err, ErrCommandExpired):
		result.ErrorCode = "command_expired"
	case errors.Is(err, context.Canceled):
		result.ErrorCode = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		result.ErrorCode = "deadline_exceeded"
	case errors.Is(err, ErrCommandBusy):
		result.ErrorCode = "command_busy"
	case errors.Is(err, ErrCommandPayloadConflict):
		result.ErrorCode = "payload_conflict"
	default:
		result.ErrorCode = "runtime_command_failed"
	}
	return result
}

func (m *Manager) persistCommandResult(ctx context.Context, request Command, err error) Command {
	result := newCommandResult(request, err)
	// A stable command ID must not pin transient execution failures. Only a
	// successful domain transition is safe to reuse without re-evaluation;
	// conflicting retries are still rejected by the stored success hash.
	if err != nil {
		return result
	}
	if m == nil || strings.TrimSpace(request.ID) == "" {
		return result
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.commandTimeout())
	defer cancel()
	if storeErr := m.storeCommandResult(persistCtx, result); storeErr != nil {
		m.logger.Warn("store runtime command result failed", slog.Any("error", storeErr), slog.String("command_id", request.ID))
		return result
	}
	stored, ok, loadErr := m.loadCommandResult(persistCtx, request.ID)
	if loadErr != nil {
		m.logger.Warn("reload runtime command result failed", slog.Any("error", loadErr), slog.String("command_id", request.ID))
		return result
	}
	if ok {
		return stored
	}
	return result
}

func (m *Manager) loadCommandResultForExecution(ctx context.Context, commandID string) (Command, bool, error) {
	loadCtx, cancel := context.WithTimeout(ctx, m.commandTimeout())
	defer cancel()
	return m.loadCommandResult(loadCtx, commandID)
}

func (m *Manager) publishCommandResult(ctx context.Context, request Command, err error) {
	replyOwnerID := strings.TrimSpace(request.ReplyOwnerID)
	if replyOwnerID == "" {
		return
	}
	if m.distributed == nil {
		return
	}
	result := m.persistCommandResult(ctx, request, err)
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.commandTimeout())
	defer cancel()
	if publishErr := m.distributed.PublishCommand(publishCtx, replyOwnerID, result); publishErr != nil {
		m.logger.Warn("publish runtime command result failed", slog.Any("error", publishErr), slog.String("command_id", request.ID))
	}
}

func (m *Manager) publishStoredCommandResult(ctx context.Context, request, result Command) {
	if m == nil || m.distributed == nil || strings.TrimSpace(request.ReplyOwnerID) == "" {
		return
	}
	if err := commandResultErrorFor(request, result); errors.Is(err, ErrCommandPayloadConflict) {
		result = Command{
			Type: CommandResult, ID: request.ID, BotID: request.BotID, SessionID: request.SessionID,
			RunID: request.RunID, Generation: request.Generation, TargetID: request.TargetID,
			PayloadHash: request.PayloadHash, ErrorCode: "payload_conflict", Error: ErrCommandPayloadConflict.Error(),
			CreatedAt: time.Now().UTC(),
		}
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.commandTimeout())
	defer cancel()
	if err := m.distributed.PublishCommand(publishCtx, request.ReplyOwnerID, result); err != nil {
		m.logger.Warn("republish stored runtime command result failed", slog.Any("error", err), slog.String("command_id", request.ID))
	}
}

func (m *Manager) commandTimeout() time.Duration {
	if m != nil && m.commandAckTTL > 0 {
		return m.commandAckTTL
	}
	return defaultCommandAckTTL
}

func (m *Manager) commandResultTTL() time.Duration {
	ttl := 4 * m.commandTimeout()
	if ttl < 30*time.Second {
		ttl = 30 * time.Second
	}
	if m != nil && m.stateTTL > ttl {
		return m.stateTTL
	}
	return ttl
}

func (m *Manager) waitCommandResult(ctx context.Context, request Command, pending <-chan error, timeout time.Duration, retryOwnerIDs ...string) error {
	waitCtx, cancelWait := context.WithTimeout(ctx, timeout)
	defer cancelWait()
	retryOwnerID := ""
	if len(retryOwnerIDs) > 0 {
		retryOwnerID = strings.TrimSpace(retryOwnerIDs[0])
	}
	pollEvery := timeout / 4
	if pollEvery < time.Millisecond {
		pollEvery = time.Millisecond
	}
	if pollEvery > 50*time.Millisecond {
		pollEvery = 50 * time.Millisecond
	}
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	retryEvery := timeout / 4
	if retryEvery < 10*time.Millisecond {
		retryEvery = 10 * time.Millisecond
	}
	if retryEvery > 250*time.Millisecond {
		retryEvery = 250 * time.Millisecond
	}
	var retry <-chan time.Time
	var retryTicker *time.Ticker
	if retryOwnerID != "" {
		retryTicker = time.NewTicker(retryEvery)
		retry = retryTicker.C
		defer retryTicker.Stop()
	}
	for {
		select {
		case err := <-pending:
			return err
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return errors.New("runtime command was not acknowledged")
		case <-poll.C:
			result, ok, loadErr := m.loadCommandResult(waitCtx, request.ID)
			if loadErr == nil && ok {
				return commandResultErrorFor(request, result)
			}
		case <-retry:
			if err := m.distributed.PublishCommand(waitCtx, retryOwnerID, request); err != nil && waitCtx.Err() == nil {
				m.logger.Debug("retry runtime command publish failed", slog.Any("error", err), slog.String("command_id", request.ID))
			}
		}
	}
}

func (m *Manager) loadCommandResult(ctx context.Context, commandID string) (Command, bool, error) {
	if m == nil || strings.TrimSpace(commandID) == "" {
		return Command{}, false, nil
	}
	if m.distributed != nil {
		return m.distributed.LoadCommandResult(ctx, commandID)
	}
	now, err := m.backend.Now(ctx)
	if err != nil {
		return Command{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.localCommandResults[strings.TrimSpace(commandID)]
	if !ok {
		return Command{}, false, nil
	}
	if !stored.expiresAt.After(now) {
		delete(m.localCommandResults, strings.TrimSpace(commandID))
		return Command{}, false, nil
	}
	return stored.result, true, nil
}

func (m *Manager) storeCommandResult(ctx context.Context, result Command) error {
	if m == nil || strings.TrimSpace(result.ID) == "" {
		return nil
	}
	if m.distributed != nil {
		return m.distributed.StoreCommandResult(ctx, result, m.commandResultTTL())
	}
	now, err := m.backend.Now(ctx)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(result.ID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if stored, ok := m.localCommandResults[id]; ok && stored.expiresAt.After(now) {
		return nil
	}
	m.localCommandResults[id] = localCommandResult{
		result:    result,
		expiresAt: now.Add(m.commandResultTTL()),
	}
	return nil
}

func (m *Manager) completePendingCommand(result Command) {
	m.mu.Lock()
	waiters := m.pendingCommands[strings.TrimSpace(result.ID)]
	if waiters != nil {
		delete(m.pendingCommands, strings.TrimSpace(result.ID))
	}
	m.mu.Unlock()
	if waiters == nil {
		return
	}
	for waiter := range waiters {
		waiter.result <- commandResultErrorFor(Command{PayloadHash: waiter.payloadHash}, result)
	}
}

func commandResultErrorFor(request, result Command) error {
	expected := strings.TrimSpace(request.PayloadHash)
	actual := strings.TrimSpace(result.PayloadHash)
	if expected != "" && actual != "" && expected != actual {
		return ErrCommandPayloadConflict
	}
	return commandResultError(result)
}

func commandResultError(result Command) error {
	if strings.TrimSpace(result.Error) == "" {
		return nil
	}
	switch result.ErrorCode {
	case "target_not_active":
		return fmt.Errorf("%w: %s", ErrCommandTargetNotActive, result.Error)
	case "command_expired":
		return fmt.Errorf("%w: %s", ErrCommandExpired, result.Error)
	case "context_canceled":
		return fmt.Errorf("%w: %s", context.Canceled, result.Error)
	case "deadline_exceeded":
		return fmt.Errorf("%w: %s", context.DeadlineExceeded, result.Error)
	case "command_busy":
		return fmt.Errorf("%w: %s", ErrCommandBusy, result.Error)
	case "payload_conflict":
		return fmt.Errorf("%w: %s", ErrCommandPayloadConflict, result.Error)
	default:
		return errors.New(result.Error)
	}
}

func commandTargetKey(cmd Command) string {
	return strings.Join([]string{
		strings.TrimSpace(cmd.BotID), strings.TrimSpace(cmd.SessionID), strings.TrimSpace(cmd.RunID),
		strings.TrimSpace(cmd.Generation), strings.TrimSpace(cmd.Type), strings.TrimSpace(cmd.TargetID),
	}, "\x00")
}

func (m *Manager) beginCommandTarget(cmd Command) bool {
	key := commandTargetKey(cmd)
	if key == "\x00\x00\x00\x00\x00" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.inflightCommandTargets[key]; exists {
		return false
	}
	m.inflightCommandTargets[key] = struct{}{}
	return true
}

func (m *Manager) endCommandTarget(cmd Command) {
	key := commandTargetKey(cmd)
	m.mu.Lock()
	delete(m.inflightCommandTargets, key)
	m.mu.Unlock()
}

func (m *Manager) beginCommandExecution(commandID string) (bool, chan struct{}) {
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		done := make(chan struct{})
		return true, done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if done := m.commandExecutions[commandID]; done != nil {
		return false, done
	}
	done := make(chan struct{})
	m.commandExecutions[commandID] = done
	return true, done
}

func (m *Manager) finishCommandExecution(commandID string, done chan struct{}) {
	commandID = strings.TrimSpace(commandID)
	m.mu.Lock()
	if commandID != "" && m.commandExecutions[commandID] == done {
		close(done)
		delete(m.commandExecutions, commandID)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	close(done)
}

func isDurableRoutedCommand(cmd Command) bool {
	switch strings.TrimSpace(cmd.Type) {
	case CommandAbort, CommandSteerWake, CommandToolApprovalResponse, CommandUserInputResponse, CommandHistoryReset:
		return strings.TrimSpace(cmd.ID) != ""
	default:
		return false
	}
}

func (m *Manager) admitCommand(cmd Command) bool {
	if !isDurableRoutedCommand(cmd) {
		return true
	}
	commandID := strings.TrimSpace(cmd.ID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.admittedCommands[commandID]; exists {
		return false
	}
	m.admittedCommands[commandID] = struct{}{}
	return true
}

func (m *Manager) releaseCommandAdmission(cmd Command) {
	if !isDurableRoutedCommand(cmd) {
		return
	}
	m.mu.Lock()
	delete(m.admittedCommands, strings.TrimSpace(cmd.ID))
	m.mu.Unlock()
}

package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestRunControlCommandContextPreservesOwnershipLossCause(t *testing.T) {
	type contextKey struct{}
	lifecycleCtx, lifecycleCancel := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, "run-scope"),
	)
	ctrl := &runControl{
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
	ctx, cancel := ctrl.commandContext(context.Background())
	defer cancel()
	if got := ctx.Value(contextKey{}); got != "run-scope" {
		t.Fatalf("command context value = %v, want run-scope", got)
	}

	ctrl.revokeOwnership(ErrRunOwnershipLost)
	ctrl.stopCommands()

	<-ctx.Done()
	if cause := context.Cause(ctx); !errors.Is(cause, ErrRunOwnershipLost) {
		t.Fatalf("command context cause = %v, want %v", cause, ErrRunOwnershipLost)
	}
}

// A command carries the acknowledgement deadline of the request that routed
// it. Relaying only cancellation would report every expiry as context.Canceled
// and hide expired commands from callers that branch on DeadlineExceeded.
func TestRunControlCommandContextKeepsParentDeadline(t *testing.T) {
	type contextKey struct{}
	lifecycleCtx, lifecycleCancel := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, "run-scope"),
	)
	defer lifecycleCancel()
	ctrl := &runControl{
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}

	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()
	ctx, cancel := ctrl.commandContext(parent)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("command context has no deadline")
	}
	if got := ctx.Value(contextKey{}); got != "run-scope" {
		t.Fatalf("command context value = %v, want run-scope", got)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("command context did not expire with its parent")
	}
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("command context error = %v, want context deadline exceeded", err)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("command context cause = %v, want context deadline exceeded", cause)
	}
}

func TestRunControlCommandContextPropagatesCancellationCauseBeforeDeadline(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	ctrl := &runControl{
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}

	sourceCtx, cancelSource := context.WithCancelCause(context.Background())
	parent, cancelDeadline := context.WithDeadline(sourceCtx, time.Now().Add(time.Minute))
	defer cancelDeadline()
	ctx, cancel := ctrl.commandContext(parent)
	defer cancel()

	// A DeadlineExceeded cause does not mean the deadline itself fired. This
	// cancellation must propagate immediately and retain Canceled as ctx.Err().
	cancelSource(context.DeadlineExceeded)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("command context did not propagate parent cancellation")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("command context error = %v, want context canceled", err)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("command context cause = %v, want context deadline exceeded", cause)
	}
}

type fakeDecisionStore struct {
	mu     sync.Mutex
	target DecisionTarget
	// extraTargets joins target in PendingRuntimeDecisions for multi-decision
	// recovery cases.
	extraTargets []DecisionTarget
}

func (f *fakeDecisionStore) ResolveRuntimeDecision(context.Context, string, string) (DecisionTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target, nil
}

func (f *fakeDecisionStore) PendingRuntimeDecisions(context.Context, string) ([]DecisionTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]DecisionTarget{f.target}, f.extraTargets...), nil
}

func (f *fakeDecisionStore) setStatus(status string) {
	f.mu.Lock()
	f.target.Status = status
	f.mu.Unlock()
}

func TestRouteDecisionResponseUsesDurableTargetAndReplaysAfterTerminal(t *testing.T) {
	t.Parallel()

	const (
		sessionID  = "session-decision-route"
		runID      = "run-decision-route"
		turnID     = "run-decision-route-turn"
		decisionID = "decision-route"
		generation = "generation-decision-route"
		token      = int64(7)
	)
	runs := newFakeLedger()
	runs.insertClaimed(runID, sessionID, token, "live-generation")
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, token); err != nil || !applied {
		t.Fatalf("park fake ledger run: applied=%v err=%v", applied, err)
	}
	backend := NewMemoryBackend()
	manager := NewManager(backend, Options{Ledger: runs})
	store := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: decisionID,
		BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: turnID,
		Status: "pending", FencingToken: token,
	}}
	manager.SetDecisionStore(store)

	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	ctrl := &runControl{
		botID: testBotID, sessionID: sessionID, runID: runID,
		generation: generation, fencingToken: token,
		lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel,
		converter: chatview.NewUIMessageStreamConverter(),
		ready:     make(chan struct{}),
	}
	ctrl.markReady()
	manager.controls[ctrl.key()] = ctrl
	now := time.Now().UTC()
	if _, changed, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: sessionID}, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot = EmptySnapshot(testBotID, sessionID)
		snapshot.Epoch = "epoch-decision-route"
		snapshot.CurrentRunView = &CurrentRunView{
			RunID: runID, TurnID: turnID, Generation: generation,
			Status: RunStatusWaitingDecision, StartedAt: now, UpdatedAt: now,
			// Deliberately empty: decision routing must not depend on the live
			// subscriber projection containing the pending request.
			Messages: []chatview.UIMessage{},
		}
		return snapshot, true, nil
	}); err != nil || !changed {
		t.Fatalf("seed live run: changed=%v err=%v", changed, err)
	}

	var executions atomic.Int32
	manager.SetCommandHandler(func(_ context.Context, command Command) error {
		executions.Add(1)
		if !command.DecisionResolved || command.FencingToken != token || command.TargetID != decisionID {
			t.Fatalf("routed command = %#v", command)
		}
		store.setStatus("submitted")
		return nil
	})
	payload, err := json.Marshal(map[string]any{
		"answers": []map[string]any{{"question_id": "q1", "option_ids": []string{"yes"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := DecisionResponse{
		ControlID: "control-1", Type: CommandUserInputResponse,
		DecisionID: decisionID, BotID: testBotID, SessionID: sessionID, RunID: runID,
		Payload: payload,
	}
	result, err := manager.RouteDecisionResponse(context.Background(), response)
	if err != nil || !result.Handled || !result.Applied {
		t.Fatalf("first route = %#v, err=%v", result, err)
	}

	// Simulate terminal cleanup. A retry with the same client identity must be
	// answered before either the decision row or live run is consulted.
	delete(manager.controls, ctrl.key())
	if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: sessionID}, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.CurrentRunView = nil
		return snapshot, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := manager.RouteDecisionResponse(context.Background(), response)
	if err != nil || !replayed.Handled || !replayed.Applied {
		t.Fatalf("terminal replay = %#v, err=%v", replayed, err)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("command executions = %d, want 1", got)
	}

	response.ControlID = "control-2"
	stale, err := manager.RouteDecisionResponse(context.Background(), response)
	if err != nil || !stale.Handled || stale.Applied {
		t.Fatalf("new terminal control = %#v, err=%v", stale, err)
	}
}

var _ ledger.Store = (*fakeLedger)(nil)

package sessionruntime

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/runtime/session/ledger"
	chatview "github.com/memohai/memoh/internal/agent/view"
)

type fakeDecisionStore struct {
	mu     sync.Mutex
	target DecisionTarget
}

func (f *fakeDecisionStore) ResolveRuntimeDecision(context.Context, string, string) (DecisionTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target, nil
}

func (f *fakeDecisionStore) PendingRuntimeDecision(context.Context, string) (DecisionTarget, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target, true, nil
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

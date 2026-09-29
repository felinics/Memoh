package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestRunControlCommandContextCancellation(t *testing.T) {
	for _, tc := range []struct {
		name               string
		deadline           time.Duration
		trigger            func(*runControl, context.CancelCauseFunc)
		wantErr, wantCause error
	}{
		{"ownership loss", 0, func(ctrl *runControl, _ context.CancelCauseFunc) {
			ctrl.revokeOwnership(ErrRunOwnershipLost)
			ctrl.stopCommands()
		}, context.Canceled, ErrRunOwnershipLost},
		{"deadline expiry", 20 * time.Millisecond, nil, context.DeadlineExceeded, context.DeadlineExceeded},
		// The cause can be DeadlineExceeded even when the future deadline has
		// not fired. Err must still be Canceled and propagation immediate.
		{"parent cancellation", time.Minute, func(_ *runControl, cancel context.CancelCauseFunc) {
			cancel(context.DeadlineExceeded)
		}, context.Canceled, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type contextKey struct{}
			lifecycle, stop := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "run-scope"))
			defer stop()
			ctrl := &runControl{lifecycleCtx: lifecycle, lifecycleCancel: stop}
			parent, cancelParent := context.WithCancelCause(context.Background())
			defer cancelParent(nil)
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, tc.deadline)
				defer cancel()
			}
			ctx, cancel := ctrl.commandContext(parent)
			defer cancel()
			if _, hasDeadline := ctx.Deadline(); hasDeadline != (tc.deadline > 0) {
				t.Fatalf("deadline propagated=%v, want %v", hasDeadline, tc.deadline > 0)
			}
			if got := ctx.Value(contextKey{}); got != "run-scope" {
				t.Fatalf("command context value=%v", got)
			}
			if tc.trigger != nil {
				tc.trigger(ctrl, cancelParent)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("command cancellation did not propagate")
			}
			if !errors.Is(ctx.Err(), tc.wantErr) || !errors.Is(context.Cause(ctx), tc.wantCause) {
				t.Fatalf("Err=%v Cause=%v, want %v / %v", ctx.Err(), context.Cause(ctx), tc.wantErr, tc.wantCause)
			}
		})
	}
}

// A visible tool block cannot authorize a control command. Only the durable
// decision router may supply the resolved target to owner-side execution.
func TestDecisionCommandRejectsProjectionOnlyTarget(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "projection-only-owner")
	handle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, testRunID, nil, func() {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-projection",
		UserInputID: "decision-projection", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	manager.SetCommandHandler(func(context.Context, Command) error { called = true; return nil })
	err = manager.applyRoutedCommand(context.Background(), Command{
		Type: CommandUserInputResponse, BotID: testBotID, SessionID: testSessionID,
		RunID: testRunID, Generation: handle.Generation, TargetID: "decision-projection",
	})
	if !errors.Is(err, ErrCommandTargetNotActive) || called {
		t.Fatalf("unresolved command: err=%v handler called=%v", err, called)
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
	runs.InsertClaimed(runID, sessionID, token, "live-generation")
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

// Exercise the actual durable decision ingress on both shared backends, with
// no decision present in the UI projection. The transport-only tests construct
// command envelopes directly and cannot substitute for this contract.
func runDistributedDecisionRouteContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	ctx := context.Background()
	runs := newFakeLedger()
	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{OwnerID: "decision-owner", Ledger: runs, Fence: &fakeFence{}, OwnerLeaseTTL: 2 * time.Second})
	remote := testRuntimeManagerWithOptions(t, backends[1], Options{OwnerID: "decision-remote", Ledger: runs, Fence: &fakeFence{}, OwnerLeaseTTL: 2 * time.Second})
	admission, err := owner.Admit(ctx, AdmitInput{
		BotID: testBotID, SessionID: "durable-decision", InvocationID: "invoke-decision", Payload: []byte(`{"text":"question"}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) { return RunAdmissionView{}, nil }, Cancel: func() {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.HandleAgentEvent(ctx, admission.Handle, native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "decision-durable", ToolCallID: "call-durable", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	store := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: "decision-durable", BotID: testBotID, SessionID: "durable-decision",
		RunID: admission.RunID, TurnID: admission.TurnID, Status: "pending", FencingToken: admission.Handle.FencingToken,
	}}
	owner.SetDecisionStore(store)
	remote.SetDecisionStore(store)
	if _, _, err := backends[0].Update(ctx, Key{BotID: testBotID, SessionID: "durable-decision"}, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.CurrentRunView.Messages = nil
		return snapshot, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	owner.SetCommandHandler(func(_ context.Context, cmd Command) error {
		if !cmd.DecisionResolved || cmd.FencingToken != admission.Handle.FencingToken {
			return errors.New("decision lost its durable ownership")
		}
		executions.Add(1)
		return nil
	})
	response := DecisionResponse{Type: CommandUserInputResponse, ControlID: "control-durable", DecisionID: "decision-durable", BotID: testBotID, SessionID: "durable-decision", RunID: admission.RunID, Payload: []byte(`{"answers":[{"question_id":"q1","text":"yes"}]}`)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := remote.RouteDecisionResponse(ctx, response)
			if err != nil || !result.Handled || !result.Applied {
				t.Errorf("decision result=%+v err=%v", result, err)
			}
		}()
	}
	wg.Wait()
	if executions.Load() != 1 {
		t.Fatalf("duplicate executions=%d", executions.Load())
	}
	if _, err := owner.FinishRun(ctx, admission.Handle, RunStatusAborted); err != nil {
		t.Fatal(err)
	}
	if result, err := remote.RouteDecisionResponse(ctx, response); err != nil || !result.Replayed || !result.Applied {
		t.Fatalf("terminal replay=%+v err=%v", result, err)
	}
	response.Payload = []byte(`{"answers":[{"question_id":"q1","text":"different"}]}`)
	if _, err := remote.RouteDecisionResponse(ctx, response); !errors.Is(err, ErrCommandPayloadConflict) {
		t.Fatalf("conflicting replay=%v", err)
	}
}

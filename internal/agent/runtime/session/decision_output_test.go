package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestDecisionOutputSurvivesEarlyAcknowledgement(t *testing.T) {
	for _, commandType := range []string{CommandUserInputResponse, CommandToolApprovalResponse} {
		t.Run(commandType, func(t *testing.T) {
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
				Type: commandType, ID: decisionID,
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

			t.Cleanup(func() { _ = manager.Close() })
			var executions atomic.Int32
			expected := []json.RawMessage{
				json.RawMessage(`{"type":"text_delta","delta":"收到答案"}`),
				json.RawMessage(`{"type":"user_input_request","userInputId":"next","status":"pending"}`),
				json.RawMessage(`{"type":"agent_end","userInputId":"next","status":"pending"}`),
			}
			manager.SetCommandHandler(func(ctx context.Context, command Command) error {
				executions.Add(1)
				// Publish before acknowledgement: subscribing afterwards would lose all of it.
				for i, raw := range expected {
					if err := manager.PublishDecisionOutput(ctx, command, int64(i+1), raw); err != nil {
						return err
					}
				}
				return manager.PublishDecisionOutput(ctx, command, int64(len(expected)+1), nil)
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response := DecisionResponse{ControlID: "control", Type: commandType, DecisionID: decisionID, BotID: testBotID, SessionID: sessionID, Payload: json.RawMessage(`{}`)}
			output := make(chan json.RawMessage, 8)
			result, err := manager.StreamDecisionResponse(ctx, response, output)
			if err != nil || !result.Applied {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			for _, want := range expected {
				select {
				case got := <-output:
					if string(got) != string(want) {
						t.Fatalf("got %s want %s", got, want)
					}
				default:
					t.Fatal("missing continuation event")
				}
			}
			replay, err := manager.StreamDecisionResponse(ctx, response, output)
			if err != nil || !replay.Replayed || executions.Load() != 1 || len(output) != 0 {
				t.Fatalf("replay=%+v executions=%d error=%v", replay, executions.Load(), err)
			}
		})
	}
}

func TestDecisionOutputTopicIsolation(t *testing.T) {
	backend := NewMemoryBackend()
	manager := NewManager(backend, Options{})
	defer func() { _ = manager.Close() }()
	ctx := context.Background()
	key := decisionOutputKey("bot", "answer-a")
	sub, err := backend.Subscribe(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, command := range []Command{{BotID: "other-bot", ID: "answer-a"}, {BotID: "bot", ID: "answer-b"}} {
		if err := manager.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-sub.C:
		t.Fatal("unrelated answer leaked into subscription")
	default:
	}
}

func TestDecisionOutputCanceledSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := NewManager(NewMemoryBackend(), Options{})
	defer func() { _ = manager.Close() }()
	_, err := manager.StreamDecisionResponse(ctx, DecisionResponse{BotID: "bot"}, make(chan json.RawMessage))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

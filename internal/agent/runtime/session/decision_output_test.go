package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
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
			// Publish a burst larger than the notification buffer before the reader
			// can consume any payload. Recovery must use the checkpoint.
			for i := 0; i < 130; i++ {
				expected = append(expected, json.RawMessage(`{"type":"text_delta","delta":"burst"}`))
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response := DecisionResponse{ControlID: "control", Type: commandType, DecisionID: decisionID, BotID: testBotID, SessionID: sessionID, Payload: json.RawMessage(`{}`)}
			output := make(chan json.RawMessage, len(expected)+1)
			result, err := manager.StreamDecisionResponse(ctx, response, output)
			if err != nil || !result.Applied {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			var accepted map[string]string
			if err := json.Unmarshal(<-output, &accepted); err != nil || accepted["type"] != "decision_accepted" {
				t.Fatalf("acceptance event: %v %v", accepted, err)
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

// A successful write with every notification lost must still recover via the
// same periodic snapshot reconciliation used by web subscribers.
type silentDecisionBackend struct{ Backend }

func (silentDecisionBackend) Publish(context.Context, Event) error { return nil }

func TestDecisionOutputRecoversWithoutEndNotification(t *testing.T) {
	backend := silentDecisionBackend{NewMemoryBackend()}
	manager := NewManager(backend, Options{})
	defer func() { _ = manager.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := Command{BotID: "bot", ID: "silent"}
	key := decisionOutputKey(command.BotID, command.ID)
	if err := manager.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{"type":"text_delta","delta":"one"}`)); err != nil {
		t.Fatal(err)
	}
	sub, err := manager.Subscribe(ctx, key.BotID, key.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := manager.PublishDecisionOutput(ctx, command, 2, nil); err != nil {
		t.Fatal(err)
	}
	output := make(chan json.RawMessage, 2)
	if err := manager.readDecisionOutput(ctx, DecisionResponse{}, "", key, sub, output); err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 {
		t.Fatalf("recovered %d events", len(output))
	}
}

func TestDecisionOutputStopsAfterOwnerRunTerminates(t *testing.T) {
	for _, status := range []string{RunStatusLost, RunStatusWaitingDecision} {
		t.Run(status, func(t *testing.T) {
			backend := NewMemoryBackend()
			manager := NewManager(backend, Options{OwnerLeaseTTL: time.Second})
			defer func() { _ = manager.Close() }()
			manager.SetDecisionStore(&fakeDecisionStore{target: DecisionTarget{ID: "next-question", RunID: "run", Status: "pending"}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := Command{BotID: "bot", ID: "orphan", RunID: "run"}
			key := decisionOutputKey(command.BotID, command.ID)
			if err := manager.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{"type":"text_delta","delta":"partial"}`)); err != nil {
				t.Fatal(err)
			}
			_, _, err := backend.Update(ctx, Key{BotID: "bot", SessionID: "session"}, func(s Snapshot, _ bool) (Snapshot, bool, error) {
				s = EmptySnapshot("bot", "session")
				s.Epoch = "epoch"
				s.CurrentRunView = &CurrentRunView{RunID: "run", Status: status}
				return s, true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			sub, err := manager.Subscribe(ctx, key.BotID, key.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			err = manager.readDecisionOutput(ctx, DecisionResponse{BotID: "bot", SessionID: "session", DecisionID: "old-question"}, "run", key, sub, make(chan json.RawMessage, 2))
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("owner loss should release reader: %v", err)
			}
		})
	}
}

func TestDecisionOutputRedisRecoveryOptional(t *testing.T) {
	url := os.Getenv("MEMOH_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set MEMOH_TEST_REDIS_URL for two-client Redis recovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("decision-output-recovery"), StateTTL: time.Minute}
	writer, err := NewRedisBackend(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewRedisBackend(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	producer := NewManager(silentDecisionBackend{writer}, Options{})
	consumer := NewManager(reader, Options{})
	defer func() { _ = producer.Close(); _ = consumer.Close() }()
	command := Command{BotID: testBotID, ID: "answer"}
	key := decisionOutputKey(command.BotID, command.ID)
	if err := producer.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{"type":"text_delta","delta":"first"}`)); err != nil {
		t.Fatal(err)
	}
	sub, err := consumer.Subscribe(ctx, key.BotID, key.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for i := 2; i <= 133; i++ {
		if err := producer.PublishDecisionOutput(ctx, command, int64(i), json.RawMessage(`{"type":"text_delta","delta":"burst"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := producer.PublishDecisionOutput(ctx, command, 134, nil); err != nil {
		t.Fatal(err)
	}
	// The publisher is gone. The reader still has its own Redis connection and
	// must finish from shared state without a single notification, including end.
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	out := make(chan json.RawMessage, 133)
	if err := consumer.readDecisionOutput(ctx, DecisionResponse{}, "", key, sub, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 133 {
		t.Fatalf("recovered %d events", len(out))
	}
}

func TestDecisionOutputRetentionLimitIsExplicit(t *testing.T) {
	b := NewMemoryBackend()
	m := NewManager(b, Options{})
	defer func() { _ = m.Close() }()
	command := Command{BotID: "bot", ID: "limit"}
	payload, _ := json.Marshal(strings.Repeat("x", decisionOutputMaxBytes))
	if err := m.PublishDecisionOutput(context.Background(), command, 1, payload); err == nil {
		t.Fatal("oversized checkpoint accepted")
	}
	s, ok, err := b.Load(context.Background(), decisionOutputKey(command.BotID, command.ID))
	if err != nil || !ok || s.DecisionOutput == nil || !s.DecisionOutput.Failed || len(s.DecisionOutput.Events) != 0 {
		t.Fatalf("limit did not leave recoverable failure: %+v %v", s, err)
	}
}

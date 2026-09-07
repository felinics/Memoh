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

func TestDecisionOutputConcurrentRetriesAndEarlyAcknowledgement(t *testing.T) {
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
			manager.SetDecisionStore(&concurrentDecisionStore{fakeDecisionStore: store, ready: make(chan struct{})})

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
				if !command.StreamOutput {
					t.Fatal("streaming admission did not enable output on the owner command")
				}
				rawCommand, err := json.Marshal(command)
				if err != nil {
					t.Fatal(err)
				}
				var routed Command
				if err := json.Unmarshal(rawCommand, &routed); err != nil || !routed.StreamOutput {
					t.Fatalf("routed output flag lost: %v", err)
				}

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
			output := make(chan json.RawMessage, 2*(len(expected)+1))
			type outcome struct {
				result DecisionResponseResult
				err    error
			}
			done := make(chan outcome, 2)
			for i := 0; i < 2; i++ {
				go func() {
					result, err := manager.StreamDecisionResponse(ctx, response, output)
					done <- outcome{result, err}
				}()
			}
			first, second := <-done, <-done
			if first.err != nil || second.err != nil || !first.result.Applied || !second.result.Applied {
				t.Fatalf("concurrent results: %+v %+v errs=%T/%v %T/%v", first, second, first.err, first.err, second.err, second.err)
			}
			if first.result.Replayed == second.result.Replayed || executions.Load() != 1 || len(output) != len(expected)+1 {
				t.Fatalf("replayed=%v/%v executions=%d events=%d, want one consumer and %d events",
					first.result.Replayed, second.result.Replayed, executions.Load(), len(output), len(expected)+1)
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
	sub, err := backend.Subscribe(ctx, decisionOutputRef("bot", "answer-a").topic())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, command := range []Command{{StreamOutput: true, BotID: "other-bot", ID: "answer-a"}, {StreamOutput: true, BotID: "bot", ID: "answer-b"}} {
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

// A successful append with every wakeup lost must still be delivered: the
// reader re-reads the log from its cursor on every reconciliation tick.
type silentDecisionBackend struct{ Backend }

func (silentDecisionBackend) Publish(context.Context, Event) error { return nil }

func TestDecisionOutputRecoversWithoutEndNotification(t *testing.T) {
	backend := silentDecisionBackend{NewMemoryBackend()}
	manager := NewManager(backend, Options{})
	defer func() { _ = manager.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := Command{StreamOutput: true, BotID: "bot", ID: "silent"}
	ref := decisionOutputRef(command.BotID, command.ID)
	if err := manager.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{"type":"text_delta","delta":"one"}`)); err != nil {
		t.Fatal(err)
	}
	sub, err := backend.Subscribe(ctx, ref.topic())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	output := make(chan json.RawMessage, 2)
	readDone := make(chan error, 1)
	go func() { readDone <- manager.readDecisionOutput(ctx, DecisionResponse{}, "", "", ref, sub, output) }()
	// The reader is already blocked on a wakeup that will never arrive.
	time.Sleep(100 * time.Millisecond)
	if err := manager.PublishDecisionOutput(ctx, command, 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if len(output) != 1 {
		t.Fatalf("recovered %d events", len(output))
	}
	if page, err := backend.ReadDecisionOutput(ctx, ref, 0); err != nil || !page.Done || page.Length != 0 {
		t.Fatalf("finished log was not reclaimed: %+v err=%v", page.DecisionOutputState, err)
	}
}

func TestDecisionOutputStopsAfterOwnerRunTerminates(t *testing.T) {
	for _, status := range []string{RunStatusLost} {
		t.Run(status, func(t *testing.T) {
			backend := NewMemoryBackend()
			manager := NewManager(backend, Options{OwnerLeaseTTL: time.Second})
			defer func() { _ = manager.Close() }()
			manager.SetDecisionStore(&fakeDecisionStore{target: DecisionTarget{ID: "next-question", RunID: "run", Status: "pending"}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := Command{StreamOutput: true, BotID: "bot", ID: "orphan", RunID: "run"}
			ref := decisionOutputRef(command.BotID, command.ID)
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
			sub, err := backend.Subscribe(ctx, ref.topic())
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			out := make(chan json.RawMessage, 2)
			err = manager.readDecisionOutput(ctx, DecisionResponse{BotID: "bot", SessionID: "session", DecisionID: "old-question"}, "run", "", ref, sub, out)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("owner loss should release reader: %v", err)
			}
			if len(out) != 1 {
				t.Fatalf("partial output before owner loss was not forwarded: %d", len(out))
			}
		})
	}
}

func TestDecisionOutputRedisRecoveryAndClaimOptional(t *testing.T) {
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
	command := Command{StreamOutput: true, BotID: testBotID, ID: "answer"}
	ref := decisionOutputRef(command.BotID, command.ID)
	if err := producer.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{"type":"text_delta","delta":"first"}`)); err != nil {
		t.Fatal(err)
	}
	// Independent clients must arbitrate through Redis, not a manager-local lock.
	type claimResult struct {
		claimed bool
		err     error
	}
	claims := make(chan claimResult, 2)
	start := make(chan struct{})
	for _, manager := range []*Manager{producer, consumer} {
		go func() {
			<-start
			claimed, err := manager.backend.ClaimDecisionOutput(ctx, ref)
			claims <- claimResult{claimed, err}
		}()
	}
	close(start)
	first, second := <-claims, <-claims
	if first.err != nil || second.err != nil || first.claimed == second.claimed {
		t.Fatalf("expected exactly one Redis output consumer: %+v %+v", first, second)
	}
	sub, err := reader.Subscribe(ctx, ref.topic())
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
	if claimed, err := reader.ClaimDecisionOutput(ctx, ref); err != nil || claimed {
		t.Fatalf("output updates or publisher close lost the claim: claimed=%v err=%v", claimed, err)
	}
	out := make(chan json.RawMessage, 133)
	if err := consumer.readDecisionOutput(ctx, DecisionResponse{}, "", "", ref, sub, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 133 {
		t.Fatalf("recovered %d events", len(out))
	}
	if page, err := reader.ReadDecisionOutput(ctx, ref, 0); err != nil || !page.Done || !page.Claimed || page.Length != 0 {
		t.Fatalf("finished Redis log was not reclaimed with its markers kept: %+v err=%v", page.DecisionOutputState, err)
	}
}

func TestDecisionOutputRetentionLimitIsExplicit(t *testing.T) {
	b := NewMemoryBackend()
	m := NewManager(b, Options{})
	defer func() { _ = m.Close() }()
	command := Command{StreamOutput: true, BotID: "bot", ID: "limit"}
	payload, _ := json.Marshal(strings.Repeat("x", decisionOutputLimits.MaxBytes))
	if err := m.PublishDecisionOutput(context.Background(), command, 1, payload); err == nil {
		t.Fatal("oversized checkpoint accepted")
	}
	page, err := b.ReadDecisionOutput(context.Background(), decisionOutputRef(command.BotID, command.ID), 0)
	if err != nil || !page.Exists || !page.Failed || page.Length != 0 {
		t.Fatalf("limit did not leave recoverable failure: %+v %v", page.DecisionOutputState, err)
	}
	// A failed log refuses further appends instead of resuming silently.
	if err := m.PublishDecisionOutput(context.Background(), command, 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if page, err := b.ReadDecisionOutput(context.Background(), decisionOutputRef(command.BotID, command.ID), 0); err != nil || page.Length != 0 {
		t.Fatalf("failed log accepted an append: %+v %v", page.DecisionOutputState, err)
	}
}

// Append is the only write; its idempotency and gap detection are what let the
// producer retry a seq and let the reader trust Length as a cursor bound.
func TestDecisionOutputLogAppendContract(t *testing.T) {
	ctx := context.Background()
	b := NewMemoryBackend()
	ref := decisionOutputRef("bot", "contract")
	limits := DecisionOutputLimits{MaxBytes: 1 << 10, MaxEvents: 3}
	if page, err := b.ReadDecisionOutput(ctx, ref, 0); err != nil || page.Exists {
		t.Fatalf("empty ref: %+v %v", page.DecisionOutputState, err)
	}
	if _, err := b.AppendDecisionOutput(ctx, ref, 2, json.RawMessage(`{}`), limits); !errors.Is(err, ErrDecisionOutputSequenceGap) {
		t.Fatalf("gap accepted: %v", err)
	}
	first, err := b.AppendDecisionOutput(ctx, ref, 1, json.RawMessage(`{"n":1}`), limits)
	if err != nil || !first.Applied || first.Length != 1 {
		t.Fatalf("first append: %+v %v", first, err)
	}
	replay, err := b.AppendDecisionOutput(ctx, ref, 1, json.RawMessage(`{"n":"other"}`), limits)
	if err != nil || replay.Applied || replay.Length != 1 {
		t.Fatalf("replayed seq must be a no-op: %+v %v", replay, err)
	}
	if _, err := b.AppendDecisionOutput(ctx, ref, 2, json.RawMessage(`{"n":2}`), limits); err != nil {
		t.Fatal(err)
	}
	page, err := b.ReadDecisionOutput(ctx, ref, 1)
	if err != nil || len(page.Events) != 1 || string(page.Events[0]) != `{"n":2}` || page.Length != 2 {
		t.Fatalf("read from cursor: %+v %v", page, err)
	}
	if claimed, err := b.ClaimDecisionOutput(ctx, ref); err != nil || !claimed {
		t.Fatalf("first claim: %v %v", claimed, err)
	}
	if claimed, err := b.ClaimDecisionOutput(ctx, ref); err != nil || claimed {
		t.Fatalf("second claim must lose: %v %v", claimed, err)
	}
	closed, err := b.AppendDecisionOutput(ctx, ref, 3, nil, limits)
	if err != nil || !closed.Applied || !closed.Done || !closed.Claimed {
		t.Fatalf("close: %+v %v", closed, err)
	}
	if after, err := b.AppendDecisionOutput(ctx, ref, 4, json.RawMessage(`{}`), limits); err != nil || after.Applied {
		t.Fatalf("append after close must be ignored: %+v %v", after, err)
	}
	if err := b.ReleaseDecisionOutput(ctx, ref); err != nil {
		t.Fatal(err)
	}
	// Entries are gone; the markers that guard against a second forwarder stay.
	if page, err := b.ReadDecisionOutput(ctx, ref, 0); err != nil || page.Length != 0 || len(page.Events) != 0 || !page.Done || !page.Claimed {
		t.Fatalf("released log: %+v %v", page.DecisionOutputState, err)
	}
	if claimed, err := b.ClaimDecisionOutput(ctx, ref); err != nil || claimed {
		t.Fatalf("release must not reopen the claim: %v %v", claimed, err)
	}
}

// A backend read failure must surface immediately, not wait for the caller's
// context: the channel needs to report the interruption while it still can.
type failingReadDecisionBackend struct {
	Backend
	fail atomic.Bool
}

func (b *failingReadDecisionBackend) ReadDecisionOutput(ctx context.Context, ref DecisionOutputRef, from int) (DecisionOutputPage, error) {
	if b.fail.Load() {
		return DecisionOutputPage{}, errors.New("log unavailable")
	}
	return b.Backend.ReadDecisionOutput(ctx, ref, from)
}

func TestDecisionOutputReadFailureStopsReader(t *testing.T) {
	backend := &failingReadDecisionBackend{Backend: NewMemoryBackend()}
	manager := NewManager(backend, Options{})
	defer func() { _ = manager.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := Command{StreamOutput: true, BotID: "bot", ID: "unreadable"}
	ref := decisionOutputRef(command.BotID, command.ID)
	sub, err := backend.Subscribe(ctx, ref.topic())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	readDone := make(chan error, 1)
	go func() {
		readDone <- manager.readDecisionOutput(ctx, DecisionResponse{}, "", "", ref, sub, make(chan json.RawMessage, 1))
	}()
	time.Sleep(50 * time.Millisecond)
	backend.fail.Store(true)
	if err := manager.PublishDecisionOutput(ctx, command, 1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readDone:
		if err == nil || errors.Is(err, context.DeadlineExceeded) || err.Error() != "log unavailable" {
			t.Fatalf("reader must return the read error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader kept waiting after a failed log read")
	}
}

// Both requests must pass the initial command-result lookup before either can
// execute. Sequential retries would only exercise the existing replay shortcut.
type concurrentDecisionStore struct {
	*fakeDecisionStore
	arrivals atomic.Int32
	ready    chan struct{}
}

func (s *concurrentDecisionStore) ResolveRuntimeDecision(ctx context.Context, kind, id string) (DecisionTarget, error) {
	if s.arrivals.Add(1) == 2 {
		close(s.ready)
	}
	select {
	case <-s.ready:
	case <-ctx.Done():
		return DecisionTarget{}, ctx.Err()
	}
	return s.fakeDecisionStore.ResolveRuntimeDecision(ctx, kind, id)
}

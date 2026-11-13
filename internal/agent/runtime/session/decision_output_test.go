package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			runs.InsertClaimed(runID, sessionID, token, "live-generation")
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
	for i, manager := range []*Manager{producer, consumer} {
		token := []string{"claim-producer", "claim-consumer"}[i]
		go func() {
			<-start
			outcome, err := manager.backend.ClaimDecisionOutput(ctx, ref, token, time.Minute)
			claims <- claimResult{outcome == DecisionOutputClaimAcquired, err}
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
	if outcome, err := reader.ClaimDecisionOutput(ctx, ref, "other-token", time.Minute); err != nil || outcome != DecisionOutputClaimBusy {
		t.Fatalf("output updates or publisher close lost the claim: outcome=%v err=%v", outcome, err)
	}
	out := make(chan json.RawMessage, 133)
	if err := consumer.readDecisionOutput(ctx, DecisionResponse{}, "", "", ref, sub, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 133 {
		t.Fatalf("recovered %d events", len(out))
	}
	if page, err := reader.ReadDecisionOutput(ctx, ref, 0); err != nil || !page.Done || !page.Claimed || !page.Released || page.Length != 0 {
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
	if outcome, err := b.ClaimDecisionOutput(ctx, ref, "token-1", time.Minute); err != nil || outcome != DecisionOutputClaimAcquired {
		t.Fatalf("first claim: %v %v", outcome, err)
	}
	if outcome, err := b.ClaimDecisionOutput(ctx, ref, "token-2", time.Minute); err != nil || outcome != DecisionOutputClaimBusy {
		t.Fatalf("second claim must report busy: %v %v", outcome, err)
	}
	closed, err := b.AppendDecisionOutput(ctx, ref, 3, nil, limits)
	if err != nil || !closed.Applied || !closed.Done || !closed.Claimed {
		t.Fatalf("close: %+v %v", closed, err)
	}
	if after, err := b.AppendDecisionOutput(ctx, ref, 4, json.RawMessage(`{}`), limits); err != nil || after.Applied {
		t.Fatalf("append after close must be ignored: %+v %v", after, err)
	}
	if err := b.ReleaseDecisionOutput(ctx, ref, "token-1"); err != nil {
		t.Fatal(err)
	}
	// Entries are gone; the markers that guard against a second forwarder stay.
	if page, err := b.ReadDecisionOutput(ctx, ref, 0); err != nil || page.Length != 0 || len(page.Events) != 0 || !page.Done || !page.Claimed || !page.Released {
		t.Fatalf("released log: %+v %v", page.DecisionOutputState, err)
	}
	if outcome, err := b.ClaimDecisionOutput(ctx, ref, "token-3", time.Minute); err != nil || outcome != DecisionOutputClaimUnavailable {
		t.Fatalf("release must not reopen the claim: %v %v", outcome, err)
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

type decisionOutputBackendCase struct {
	name string
	open func(t *testing.T) (Backend, Backend)
}

func decisionOutputBackendCases(t *testing.T) []decisionOutputBackendCase {
	t.Helper()
	cases := []decisionOutputBackendCase{{
		name: "memory",
		open: func(*testing.T) (Backend, Backend) { b := NewMemoryBackend(); return b, b },
	}}
	if url := os.Getenv("MEMOH_TEST_REDIS_URL"); url != "" {
		cases = append(cases, decisionOutputBackendCase{
			name: "redis",
			open: func(t *testing.T) (Backend, Backend) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				opts := RedisOptions{URL: url, KeyPrefix: uniqueRuntimeBackendPrefix("decision-output-suite"), StateTTL: time.Minute}
				first, err := NewRedisBackend(ctx, opts)
				if err != nil {
					t.Fatal(err)
				}
				second, err := NewRedisBackend(ctx, opts)
				if err != nil {
					_ = first.Close()
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
				return first, second
			},
		})
	}
	return cases
}

func seedDecisionManagerPair(
	t *testing.T, b1, b2 Backend, sessionID, runID, generation string, token int64, ownerStore, followerStore DecisionStore, opts Options,
) (*Manager, *Manager) {
	t.Helper()
	runs := newFakeLedger()
	runs.InsertClaimed(runID, sessionID, token, generation)
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, token); err != nil || !applied {
		t.Fatalf("park fake ledger run: applied=%v err=%v", applied, err)
	}
	opts.Ledger, opts.OwnerID, opts.Fence = runs, "owner-"+sessionID, &fakeFence{}
	owner := NewManager(b1, opts)
	owner.SetDecisionStore(ownerStore)
	now := time.Now().UTC()
	leaseDeadline := now.Add(time.Minute)
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	t.Cleanup(lifecycleCancel)
	ctrl := &runControl{
		botID: testBotID, sessionID: sessionID, runID: runID, generation: generation, fencingToken: token,
		lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, leaseValidUntil: leaseDeadline,
		converter: chatview.NewUIMessageStreamConverter(), ready: make(chan struct{}),
	}
	ctrl.markReady()
	owner.controls[ctrl.key()] = ctrl
	key := Key{BotID: testBotID, SessionID: sessionID}
	dist, isDist := b1.(DistributedBackend)
	seed := func(s Snapshot, _ bool) (Snapshot, bool, error) {
		s = EmptySnapshot(testBotID, sessionID)
		s.Epoch = "epoch-" + sessionID
		view := &CurrentRunView{
			RunID: runID, TurnID: runID + "-turn", Generation: generation, FencingToken: token,
			Status: RunStatusWaitingDecision, StartedAt: now, UpdatedAt: now, Messages: []chatview.UIMessage{},
		}
		if isDist {
			view.OwnerID, view.OwnerLeaseExpiresAt = owner.ownerID, &leaseDeadline
		}
		s.CurrentRunView = view
		return s, true, nil
	}
	var changed bool
	var err error
	if isDist {
		ref := RunRef{BotID: testBotID, SessionID: sessionID, RunID: runID, OwnerID: owner.ownerID, Generation: generation, FencingToken: token}
		_, changed, err = dist.StartRun(context.Background(), key, ref, seed)
	} else {
		_, changed, err = b1.Update(context.Background(), key, seed)
	}
	if err != nil || !changed {
		t.Fatalf("seed live run: changed=%v err=%v", changed, err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if !isDist {
		return owner, owner
	}
	if err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	fOpts := opts
	fOpts.OwnerID = "follower-" + owner.ownerID
	follower := NewManager(b2, fOpts)
	follower.SetDecisionStore(followerStore)
	if err := follower.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = follower.Close() })
	return owner, follower
}

type blockingResolveDecisionStore struct {
	*fakeDecisionStore
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (s *blockingResolveDecisionStore) ResolveRuntimeDecision(ctx context.Context, kind, id string) (DecisionTarget, error) {
	if s.once.CompareAndSwap(false, true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return DecisionTarget{}, ctx.Err()
		}
	}
	return s.fakeDecisionStore.ResolveRuntimeDecision(ctx, kind, id)
}

func TestDecisionOutputLeaseFencingExpiryRenewalAndSlowDrain(t *testing.T) {
	for _, bc := range decisionOutputBackendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			b1, b2 := bc.open(t)
			ctx := context.Background()
			ref := decisionOutputRef(testBotID, "lease-contract")
			if out, err := b1.ClaimDecisionOutput(ctx, ref, "tok-1", 80*time.Millisecond); err != nil || out != DecisionOutputClaimUnavailable {
				t.Fatalf("missing ref claim = (%v, %v)", out, err)
			}
			if err := b1.InitDecisionOutput(ctx, ref, DecisionOutputRun{SessionID: "s1", RunID: "r1", Generation: "g1"}); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				b    Backend
				tok  string
				want DecisionOutputClaimOutcome
			}{
				{b1, "tok-1", DecisionOutputClaimAcquired},
				{b1, "tok-1", DecisionOutputClaimAcquired},
				{b2, "tok-2", DecisionOutputClaimBusy},
			} {
				if out, err := tc.b.ClaimDecisionOutput(ctx, ref, tc.tok, 80*time.Millisecond); err != nil || out != tc.want {
					t.Fatalf("claim(%s) = (%v, %v), want %v", tc.tok, out, err, tc.want)
				}
			}
			_ = b2.UnclaimDecisionOutput(ctx, ref, "wrong-tok")
			if ok, err := b2.RenewDecisionOutputClaim(ctx, ref, "wrong-tok", 80*time.Millisecond); err != nil || ok {
				t.Fatalf("wrong renew = (%v, %v)", ok, err)
			}
			if ok, err := b1.RenewDecisionOutputClaim(ctx, ref, "tok-1", 80*time.Millisecond); err != nil || !ok {
				t.Fatalf("owner renew = (%v, %v)", ok, err)
			}
			time.Sleep(120 * time.Millisecond)
			if out, err := b2.ClaimDecisionOutput(ctx, ref, "tok-2", time.Minute); err != nil || out != DecisionOutputClaimAcquired {
				t.Fatalf("expired preemption = (%v, %v)", out, err)
			}
			_ = b1.ReleaseDecisionOutput(ctx, ref, "tok-1")
			_ = b2.UnclaimDecisionOutput(ctx, ref, "tok-2")
			if out, err := b1.ClaimDecisionOutput(ctx, ref, "tok-3", time.Minute); err != nil || out != DecisionOutputClaimAcquired {
				t.Fatalf("reclaim = (%v, %v)", out, err)
			}
			_ = b1.ReleaseDecisionOutput(ctx, ref, "tok-3")
			if out, err := b2.ClaimDecisionOutput(ctx, ref, "tok-4", time.Minute); err != nil || out != DecisionOutputClaimUnavailable {
				t.Fatalf("claim after release = (%v, %v)", out, err)
			}

			store := &fakeDecisionStore{target: DecisionTarget{
				Type: CommandUserInputResponse, ID: "dec-slow", BotID: testBotID,
				SessionID: "sess-slow", RunID: "run-slow", TurnID: "run-slow-turn", Status: "pending", FencingToken: 21,
			}}
			owner, follower := seedDecisionManagerPair(t, b1, b2, "sess-slow", "run-slow", "gen-slow", 21, store, store, Options{DecisionOutputClaimTTL: 90 * time.Millisecond})
			owner.SetCommandHandler(func(ctx context.Context, cmd Command) error {
				store.mu.Lock()
				store.target.Status, store.target.ControlID, store.target.PayloadHash = "submitted", cmd.ID, cmd.PayloadHash
				store.mu.Unlock()
				for i := 1; i <= 3; i++ {
					_ = owner.PublishDecisionOutput(ctx, cmd, int64(i), json.RawMessage(fmt.Sprintf(`{"type":"text_delta","delta":"p%d"}`, i)))
				}
				return owner.PublishDecisionOutput(ctx, cmd, 4, nil)
			})
			resp := DecisionResponse{ControlID: "ctrl-slow", Type: CommandUserInputResponse, DecisionID: "dec-slow", BotID: testBotID, SessionID: "sess-slow", Payload: json.RawMessage(`{"answer":"ok"}`)}
			slowOut, done := make(chan json.RawMessage), make(chan error, 1)
			go func() { _, err := owner.StreamDecisionResponse(ctx, resp, slowOut); done <- err }()
			<-slowOut
			<-slowOut
			time.Sleep(230 * time.Millisecond)
			if _, err := follower.StreamDecisionResponse(ctx, resp, make(chan json.RawMessage, 8)); !errors.Is(err, ErrDecisionOutputBusy) {
				t.Fatalf("competing reader during slow drain got err=%v, want ErrDecisionOutputBusy", err)
			}
			<-slowOut
			<-slowOut
			if err := <-done; err != nil {
				t.Fatalf("slow reader err = %v", err)
			}
		})
	}
}

func TestDecisionOutputStatelessFullReplayAndTwoPhaseDeferRelease(t *testing.T) {
	for _, bc := range decisionOutputBackendCases(t) {
		t.Run(bc.name, func(t *testing.T) {
			b1, b2 := bc.open(t)
			ctx := context.Background()
			baseStore := &fakeDecisionStore{target: DecisionTarget{
				Type: CommandUserInputResponse, ID: "dec-replay", BotID: testBotID,
				SessionID: "sess-replay", RunID: "run-replay", TurnID: "run-replay-turn", Status: "pending", FencingToken: 31,
			}}
			gateStore := &blockingResolveDecisionStore{fakeDecisionStore: baseStore, entered: make(chan struct{}), release: make(chan struct{})}
			owner, follower := seedDecisionManagerPair(t, b1, b2, "sess-replay", "run-replay", "gen-replay", 31, gateStore, baseStore, Options{})

			var executions atomic.Int32
			part1Published, allowPart2, bgDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			owner.SetCommandHandler(func(cmdCtx context.Context, cmd Command) error {
				executions.Add(1)
				baseStore.mu.Lock()
				baseStore.target.Status, baseStore.target.ControlID, baseStore.target.PayloadHash = "submitted", cmd.ID, cmd.PayloadHash
				baseStore.mu.Unlock()
				_ = owner.PublishDecisionOutput(cmdCtx, cmd, 1, json.RawMessage(`{"type":"text_delta","delta":"seg-1"}`))
				close(part1Published)
				<-allowPart2
				_ = owner.PublishDecisionOutput(cmdCtx, cmd, 2, json.RawMessage(`{"type":"text_delta","delta":"seg-2"}`))
				err := owner.PublishDecisionOutput(cmdCtx, cmd, 3, nil)
				close(bgDone)
				return err
			})

			preCtx, preCancel := context.WithCancel(ctx)
			preDone := make(chan error, 1)
			resp1 := DecisionResponse{
				ControlID: "ctrl-1", DeferRelease: true, Type: CommandUserInputResponse, DecisionID: "dec-replay",
				BotID: testBotID, SessionID: "sess-replay", Payload: json.RawMessage(`{"answers":[{"question_id":"q1","option_ids":["opt_b","opt_a"]}]}`),
			}
			go func() {
				_, err := owner.StreamDecisionResponse(preCtx, resp1, make(chan json.RawMessage, 8))
				preDone <- err
			}()
			<-gateStore.entered
			preCancel()
			close(gateStore.release)
			if err := <-preDone; !errors.Is(err, context.Canceled) || executions.Load() != 0 {
				t.Fatalf("pre-accept err=%v executions=%d", err, executions.Load())
			}

			midCtx, midCancel := context.WithCancel(ctx)
			midDone := make(chan error, 1)
			go func() {
				_, err := owner.StreamDecisionResponse(midCtx, resp1, make(chan json.RawMessage, 8))
				midDone <- err
			}()
			<-part1Published
			if _, err := follower.StreamDecisionResponse(ctx, resp1, make(chan json.RawMessage, 8)); !errors.Is(err, ErrDecisionOutputBusy) {
				t.Fatalf("coalesced DeferRelease caller got err=%v, want Busy", err)
			}
			midCancel()
			if err := <-midDone; !errors.Is(err, ErrDecisionOutputInterrupted) {
				t.Fatalf("mid-stream cancel err=%v", err)
			}
			close(allowPart2)
			<-bgDone
			if !follower.HasPendingDecisionOutput(ctx, CommandUserInputResponse, testBotID, "dec-replay", "ctrl-1") {
				t.Fatal("expected pending unreleased output")
			}

			retryResp := resp1
			retryResp.ControlID = "ctrl-retry-2"
			retryResp.Payload = json.RawMessage(`{"answers":[{"question_id":"q1","option_ids":["opt_a","opt_b"]}]}`)
			out2 := make(chan json.RawMessage, 8)
			if res2, err := follower.StreamDecisionResponse(ctx, retryResp, out2); err != nil || !res2.Applied || executions.Load() != 1 || len(out2) != 3 {
				t.Fatalf("stateless full replay: res=%+v executions=%d len=%d err=%v", res2, executions.Load(), len(out2), err)
			}
			if a, b, c := string(<-out2), string(<-out2), string(<-out2); !strings.Contains(a, "decision_accepted") || !strings.Contains(b, "seg-1") || !strings.Contains(c, "seg-2") {
				t.Fatalf("replayed = (%s, %s, %s)", a, b, c)
			}
			if !follower.HasPendingDecisionOutput(ctx, CommandUserInputResponse, testBotID, "dec-replay", "ctrl-1") {
				t.Fatal("DeferRelease=true must retain log until ReleaseOutput")
			}

			origRef := decisionOutputRef(testBotID, decisionControlCommandID(CommandUserInputResponse, testBotID, "dec-replay", "ctrl-1"))
			_, _ = b2.ClaimDecisionOutput(ctx, origRef, "active-hold", time.Minute)
			relResp := retryResp
			relResp.DeferRelease, relResp.ReleaseOutput = false, true
			_, _ = follower.StreamDecisionResponse(ctx, relResp, nil)
			if !follower.HasPendingDecisionOutput(ctx, CommandUserInputResponse, testBotID, "dec-replay", "ctrl-1") {
				t.Fatal("ReleaseOutput clobbered active reader lease")
			}
			_ = b2.UnclaimDecisionOutput(ctx, origRef, "active-hold")
			if resRel, err := follower.StreamDecisionResponse(ctx, relResp, nil); err != nil || !resRel.Handled || !resRel.Applied {
				t.Fatalf("ReleaseOutput = (%+v, %v)", resRel, err)
			}
			if follower.HasPendingDecisionOutput(ctx, CommandUserInputResponse, testBotID, "dec-replay", "ctrl-1") {
				t.Fatal("output remained pending after ReleaseOutput")
			}
			if afterRel, err := follower.StreamDecisionResponse(ctx, retryResp, make(chan json.RawMessage, 4)); err != nil || !afterRel.Handled || afterRel.Applied {
				t.Fatalf("after release = (%+v, %v)", afterRel, err)
			}
		})
	}
}

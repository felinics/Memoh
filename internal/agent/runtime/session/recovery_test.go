package sessionruntime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/runtimefence"
)

var errRecoveryReservation = errors.New("injected recovered-run reservation failure")

type retryingRecoveryBackend struct {
	DistributedBackend
	memory *MemoryBackend

	mu             sync.Mutex
	generation     string
	startCalls     int
	ref            RunRef
	indexed        LeaseCandidate
	releaseCalls   int
	releaseApplied int
}

func newRetryingRecoveryBackend(generation string, candidate LeaseCandidate) *retryingRecoveryBackend {
	return &retryingRecoveryBackend{
		memory:     NewMemoryBackend(),
		generation: generation,
		indexed:    candidate,
	}
}

func (b *retryingRecoveryBackend) Now(ctx context.Context) (time.Time, error) {
	return b.memory.Now(ctx)
}

func (b *retryingRecoveryBackend) Load(ctx context.Context, key Key) (Snapshot, bool, error) {
	return b.memory.Load(ctx, key)
}

func (b *retryingRecoveryBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	return b.memory.Update(ctx, key, update)
}

func (b *retryingRecoveryBackend) Publish(ctx context.Context, event Event) error {
	return b.memory.Publish(ctx, event)
}

func (b *retryingRecoveryBackend) Subscribe(ctx context.Context, key Key) (Subscription, error) {
	return b.memory.Subscribe(ctx, key)
}

func (b *retryingRecoveryBackend) Close() error {
	return b.memory.Close()
}

func (b *retryingRecoveryBackend) StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error) {
	b.mu.Lock()
	b.startCalls++
	call := b.startCalls
	b.mu.Unlock()
	if call == 1 {
		return Snapshot{}, false, errRecoveryReservation
	}
	snapshot, changed, err := b.memory.Update(ctx, key, update)
	if err != nil || !changed {
		return snapshot, changed, err
	}
	b.mu.Lock()
	b.ref = ref
	expiresAt := time.Now().Add(time.Hour)
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.OwnerLeaseExpiresAt != nil {
		expiresAt = *snapshot.CurrentRunView.OwnerLeaseExpiresAt
	}
	b.indexed = LeaseCandidate{
		Key: key, RunID: ref.RunID, FencingToken: ref.FencingToken, ExpiresAt: expiresAt,
	}
	b.mu.Unlock()
	return snapshot, true, nil
}

func (b *retryingRecoveryBackend) LoadRunRef(_ context.Context, key Key, runID string) (RunRef, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ref.RunID != runID || b.ref.BotID != key.BotID || b.ref.SessionID != key.SessionID {
		return RunRef{}, false, nil
	}
	return b.ref, true, nil
}

func (b *retryingRecoveryBackend) RenewLease(_ context.Context, key Key, runID, _ string, _ string, _ time.Time, expiresAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.indexed.Key != key || b.indexed.RunID != runID {
		return ErrRunOwnershipLost
	}
	b.indexed.ExpiresAt = expiresAt
	return nil
}

func (b *retryingRecoveryBackend) LivenessGeneration(context.Context) (string, error) {
	return b.generation, nil
}

func (b *retryingRecoveryBackend) ExpiredLeaseCandidates(_ context.Context, limit int64) ([]LeaseCandidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.indexed.RunID == "" || b.indexed.ExpiresAt.After(time.Now()) || limit == 0 {
		return nil, nil
	}
	return []LeaseCandidate{b.indexed}, nil
}

func (b *retryingRecoveryBackend) ReleaseLeaseCandidate(_ context.Context, candidate LeaseCandidate) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseCalls++
	if b.indexed.RunID != candidate.RunID || b.indexed.FencingToken != candidate.FencingToken {
		return false, nil
	}
	b.indexed = LeaseCandidate{}
	b.releaseApplied++
	return true, nil
}

func (*retryingRecoveryBackend) AcquireLeaderLease(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (*retryingRecoveryBackend) ReleaseLeaderLease(context.Context, string) error { return nil }

func (b *retryingRecoveryBackend) recoveryState() (int, RunRef, LeaseCandidate, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startCalls, b.ref, b.indexed, b.releaseCalls, b.releaseApplied
}

type waitingDecisionRecoveryFence struct {
	runs      *fakeLedger
	decisions *fakeDecisionStore

	mu       sync.Mutex
	reclaims [][2]int64
}

func (*waitingDecisionRecoveryFence) Activate(context.Context, string, string, int64) error {
	return nil
}

func (f *waitingDecisionRecoveryFence) ReclaimWaitingDecision(
	_ context.Context,
	botID, sessionID, runID, ownerID, liveGeneration string,
	previousToken, newToken int64,
	decisions []runtimefence.PreservedDecision,
) error {
	f.runs.Mu.Lock()
	f.decisions.mu.Lock()
	defer f.decisions.mu.Unlock()
	defer f.runs.Mu.Unlock()
	run := f.runs.Runs[runID]
	if run == nil || run.BotID != botID || run.SessionID != sessionID ||
		run.State != ledger.StateWaitingDecision || run.FencingToken != previousToken {
		return ErrRunOwnershipLost
	}
	expected := map[string]bool{f.decisions.target.ID: true}
	for _, extra := range f.decisions.extraTargets {
		expected[extra.ID] = true
	}
	if len(decisions) != len(expected) ||
		f.decisions.target.RunID != runID ||
		f.decisions.target.FencingToken != previousToken {
		return ErrCommandTargetMismatch
	}
	for _, preserved := range decisions {
		if !expected[preserved.ID] {
			return ErrCommandTargetMismatch
		}
	}
	run.OwnerID = ownerID
	run.FencingToken = newToken
	run.LiveGeneration = liveGeneration
	run.OwnerSince = time.Now().UTC()
	f.decisions.target.FencingToken = newToken
	for i := range f.decisions.extraTargets {
		f.decisions.extraTargets[i].FencingToken = newToken
	}
	f.mu.Lock()
	f.reclaims = append(f.reclaims, [2]int64{previousToken, newToken})
	f.mu.Unlock()
	return nil
}

func (f *waitingDecisionRecoveryFence) recordedReclaims() [][2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int64(nil), f.reclaims...)
}

func TestWaitingDecisionRecoveryRetriesAfterFenceCommitWhenLiveReservationFails(t *testing.T) {
	type contextKey struct{}
	const (
		runID      = "run-waiting-recovery"
		sessionID  = "session-waiting-recovery"
		decisionID = "decision-waiting-recovery"
	)
	key := Key{BotID: testBotID, SessionID: sessionID}
	runs := newFakeLedger()
	runs.InsertClaimed(runID, sessionID, 5, "generation-old")
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, 5); err != nil || !applied {
		t.Fatalf("park run: applied=%v err=%v", applied, err)
	}
	runs.Mu.Lock()
	runs.Token = 5
	runs.Mu.Unlock()
	decisions := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: decisionID,
		BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn",
		Status: "pending", FencingToken: 5,
	}}
	candidate := LeaseCandidate{
		Key: key, RunID: runID, FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
	}
	backend := newRetryingRecoveryBackend("generation-new", candidate)
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{
		OwnerID: "owner-recovery", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence,
	})
	t.Cleanup(func() {
		manager.forgetLocalControl(context.Background(), runID)
		_ = backend.Close()
	})
	manager.SetDecisionStore(decisions)
	reaper := newTestReaperWithLiveness(t, runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(manager.recoverWaitingDecision)
	reaperCtx := context.WithValue(context.Background(), contextKey{}, "reaper-scope")

	reaper.tick(reaperCtx)

	firstRun, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	decisions.mu.Lock()
	firstDecisionToken := decisions.target.FencingToken
	decisions.mu.Unlock()
	startCalls, ref, indexed, releaseCalls, releaseApplied := backend.recoveryState()
	if firstRun.State != ledger.StateWaitingDecision || firstRun.FencingToken != 6 || firstDecisionToken != 6 {
		t.Fatalf("durable handoff = run:%+v decision_token:%d, want waiting token 6", firstRun, firstDecisionToken)
	}
	if startCalls != 1 || ref.RunID != "" || manager.localControlForScope(testBotID, sessionID, runID) != nil {
		t.Fatalf("failed live reservation leaked ownership: starts=%d ref=%+v control=%v", startCalls, ref, manager.localControlForScope(testBotID, sessionID, runID))
	}
	if indexed.FencingToken != 5 || releaseCalls != 0 || releaseApplied != 0 {
		t.Fatalf("retry pointer changed after reservation failure: indexed=%+v release_calls=%d applied=%d", indexed, releaseCalls, releaseApplied)
	}

	reaper.tick(reaperCtx)

	secondRun, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	decisions.mu.Lock()
	secondDecisionToken := decisions.target.FencingToken
	decisions.mu.Unlock()
	startCalls, ref, indexed, releaseCalls, releaseApplied = backend.recoveryState()
	if secondRun.State != ledger.StateWaitingDecision || secondRun.FencingToken != 7 || secondDecisionToken != 7 {
		t.Fatalf("retried handoff = run:%+v decision_token:%d, want waiting token 7", secondRun, secondDecisionToken)
	}
	if startCalls != 2 || ref.FencingToken != 7 || manager.localControlForScope(testBotID, sessionID, runID) == nil {
		t.Fatalf("retried live reservation = starts:%d ref:%+v", startCalls, ref)
	}
	ctrl := manager.localControlForScope(testBotID, sessionID, runID)
	if got := ctrl.lifecycleCtx.Value(contextKey{}); got != "reaper-scope" {
		t.Fatalf("recovered run context value = %v, want reaper-scope", got)
	}
	if indexed.FencingToken != 7 || releaseCalls != 1 || releaseApplied != 0 {
		t.Fatalf("stale release changed successor lease: indexed=%+v release_calls=%d applied=%d", indexed, releaseCalls, releaseApplied)
	}
	if got := fence.recordedReclaims(); len(got) != 2 || got[0] != [2]int64{5, 6} || got[1] != [2]int64{6, 7} {
		t.Fatalf("reclaims = %+v, want [[5 6] [6 7]]", got)
	}
}

// A turn can park on an approval and a user input at once. Recovery used to
// read exactly one pending decision and error on the pair — the reaper then
// retried forever and the run stayed in waiting_decision with a dead owner.
// Both decisions must be preserved through the reclaimed fence.
func TestWaitingDecisionRecoveryPreservesParallelDecisions(t *testing.T) {
	type contextKey struct{}
	const (
		runID     = "run-parallel-recovery"
		sessionID = "session-parallel-recovery"
	)
	key := Key{BotID: testBotID, SessionID: sessionID}
	runs := newFakeLedger()
	runs.InsertClaimed(runID, sessionID, 5, "generation-old")
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, 5); err != nil || !applied {
		t.Fatalf("park run: applied=%v err=%v", applied, err)
	}
	runs.Mu.Lock()
	runs.Token = 5
	runs.Mu.Unlock()
	decisions := &fakeDecisionStore{
		target: DecisionTarget{
			Type: CommandToolApprovalResponse, ID: "decision-approval",
			BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn",
			Status: "pending", FencingToken: 5,
		},
		extraTargets: []DecisionTarget{{
			Type: CommandUserInputResponse, ID: "decision-input",
			BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn",
			Status: "pending", FencingToken: 5,
		}},
	}
	candidate := LeaseCandidate{
		Key: key, RunID: runID, FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute),
	}
	backend := newRetryingRecoveryBackend("generation-new", candidate)
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{
		OwnerID: "owner-parallel-recovery", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence,
	})
	t.Cleanup(func() {
		manager.forgetLocalControl(context.Background(), runID)
		_ = backend.Close()
	})
	manager.SetDecisionStore(decisions)
	reaper := newTestReaperWithLiveness(t, runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(manager.recoverWaitingDecision)
	reaper.tick(context.WithValue(context.Background(), contextKey{}, "reaper-scope"))

	run, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateWaitingDecision || run.FencingToken != 6 {
		t.Fatalf("recovered run = %+v, want waiting_decision at token 6", run)
	}
	if got := len(fence.recordedReclaims()); got != 1 {
		t.Fatalf("reclaims = %d, want exactly one covering both decisions", got)
	}
	decisions.mu.Lock()
	approvalToken := decisions.target.FencingToken
	inputToken := decisions.extraTargets[0].FencingToken
	decisions.mu.Unlock()
	if approvalToken != 6 || inputToken != 6 {
		t.Fatalf("preserved decision tokens = approval:%d input:%d, want both 6", approvalToken, inputToken)
	}
}

func TestWaitingDecisionRecoveryDoesNotReplayFrozenCapabilityApproval(t *testing.T) {
	const runID, sessionID = "run-inline-lost", "session-inline-lost"
	runs := newFakeLedger()
	runs.InsertClaimed(runID, sessionID, 5, "generation-old")
	if _, applied, err := runs.SetWaitingDecision(t.Context(), runID, 5); err != nil || !applied {
		t.Fatalf("park run: %v %v", applied, err)
	}
	decisions := &fakeDecisionStore{target: DecisionTarget{Type: CommandToolApprovalResponse, ID: "frozen-approval", BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn", Status: "pending", FencingToken: 5, InlineDecision: true}}
	candidate := LeaseCandidate{Key: Key{BotID: testBotID, SessionID: sessionID}, RunID: runID, FencingToken: 5, ExpiresAt: time.Now().Add(-time.Minute)}
	backend := newRetryingRecoveryBackend("generation-new", candidate)
	t.Cleanup(func() { _ = backend.Close() })
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{OwnerID: "owner-new", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence})
	manager.SetDecisionStore(decisions)
	reaper := newTestReaperWithLiveness(t, runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(manager.recoverWaitingDecision)
	reaper.tick(t.Context())
	run, err := runs.Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateLost || len(fence.recordedReclaims()) != 0 {
		t.Fatalf("lost frozen plan was reclaimed: %+v", run)
	}
}

// parkAskUser admits a run on manager and parks it on one ask_user decision
// the way a native producer does: the request event, then the producer's
// return leaves the run waiting_decision.
func parkAskUser(t *testing.T, manager *Manager, decisions *fakeDecisionStore, invocationID string) RunHandle {
	t.Helper()
	ctx := t.Context()
	admission, err := manager.Admit(ctx, AdmitInput{
		BotID: testBotID, SessionID: testSessionID, InvocationID: invocationID,
		Payload: []byte(`{"resume":{"version":1}}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decisions.mu.Lock()
	decisions.parked = []chatview.UIMessage{
		{ID: 0, Type: chatview.UIMessageText, Content: "Let me ask."},
		{ID: 1, Type: chatview.UIMessageTool, Name: "ask_user", ToolCallID: "call-" + invocationID, UserInput: &chatview.UIUserInput{UserInputID: "ask-" + invocationID, Status: "pending", CanRespond: true}},
	}
	decisions.target = DecisionTarget{
		Type: CommandUserInputResponse, ID: "ask-" + invocationID, BotID: testBotID,
		SessionID: testSessionID, RunID: admission.RunID, TurnID: admission.Handle.TurnID,
		Status: "pending", FencingToken: admission.Handle.FencingToken,
	}
	decisionID := decisions.target.ID
	decisions.mu.Unlock()
	if _, err := manager.HandleAgentEvent(ctx, admission.Handle, native.StreamEvent{
		Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-" + invocationID,
		UserInputID: decisionID, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.FinishRun(ctx, admission.Handle, ""); err != nil {
		t.Fatal(err)
	}
	run, err := manager.runs.Get(ctx, admission.RunID)
	if err != nil || run.State != ledger.StateWaitingDecision {
		t.Fatalf("park ask_user: state=%s err=%v", run.State, err)
	}
	return admission.Handle
}

// answerAfterRestart starts a fresh memory-backed manager over the same
// durable stores, waits for its reaper to take the parked run over, and
// answers the question through it.
func answerAfterRestart(t *testing.T, runs *fakeLedger, decisions *fakeDecisionStore, fence *waitingDecisionRecoveryFence, handle RunHandle) {
	t.Helper()
	ctx := t.Context()
	next := NewManager(NewMemoryBackend(), Options{Ledger: runs, Fence: fence, OwnerID: "after-restart"})
	next.SetDecisionStore(decisions)
	var handled atomic.Int32
	next.SetCommandHandler(func(context.Context, Command) error {
		handled.Add(1)
		decisions.setStatus("submitted")
		return nil
	})
	t.Cleanup(func() { _ = next.Close() })
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for next.localControlForScope(handle.BotID, handle.SessionID, handle.RunID) == nil {
		if time.Now().After(deadline) {
			run, _ := runs.Get(ctx, handle.RunID)
			t.Fatalf("parked run was not taken over after restart: %+v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, err := runs.Get(ctx, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateWaitingDecision || run.FencingToken <= handle.FencingToken || run.OwnerID != "after-restart" {
		t.Fatalf("recovered run = %+v, want waiting_decision owned by the new process under a newer token", run)
	}
	// The restarted backend kept nothing of the run, so it is shown from what
	// the run persisted: the question stays on screen and answerable.
	snapshot, ok, err := next.backend.Load(ctx, Key{BotID: handle.BotID, SessionID: handle.SessionID})
	if err != nil || !ok || snapshot.CurrentRunView == nil {
		t.Fatalf("recovered snapshot: ok=%v err=%v", ok, err)
	}
	view := snapshot.CurrentRunView
	if view.Status != RunStatusWaitingDecision || len(view.Messages) != 2 || view.Messages[1].UserInput == nil || !view.Messages[1].UserInput.CanRespond {
		t.Fatalf("recovered view = %+v, want the persisted question as an answerable card", view)
	}
	response := DecisionResponse{
		ControlID: "answer-after-restart", Type: CommandUserInputResponse,
		DecisionID: decisions.target.ID, BotID: handle.BotID, SessionID: handle.SessionID,
		RunID: handle.RunID, Payload: []byte(`{"text_answer":"runtime"}`),
	}
	result, err := next.RouteDecisionResponse(ctx, response)
	if err != nil || !result.Applied || handled.Load() != 1 {
		t.Fatalf("answer after restart = %+v err=%v handled=%d, want applied once", result, err, handled.Load())
	}
	replay, err := next.RouteDecisionResponse(ctx, response)
	if err != nil || !replay.Applied || !replay.Replayed || handled.Load() != 1 {
		t.Fatalf("repeated answer = %+v err=%v handled=%d, want one replayed application", replay, err, handled.Load())
	}
}

// A memory backend has no lease index: the generation stamped on the parked
// run is what the next process finds. Graceful shutdown must keep the run
// waiting, hand its fence over, and leave the question pending for the
// restarted server to continue.
func TestMemoryParkedDecisionSurvivesGracefulRestart(t *testing.T) {
	ctx := t.Context()
	runs := newFakeLedger()
	decisions := &fakeDecisionStore{}
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	old := NewManager(NewMemoryBackend(), Options{Ledger: runs, Fence: fence, OwnerID: "before-restart"})
	old.SetDecisionStore(decisions)
	finalized := 0
	old.SetDecisionFinalizer(func(context.Context, RunHandle) error {
		finalized++
		decisions.setStatus("canceled")
		return nil
	})
	handle := parkAskUser(t, old, decisions, "graceful-restart")

	if err := old.InterruptForShutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := runs.Get(ctx, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateWaitingDecision || run.FencingToken <= handle.FencingToken {
		t.Fatalf("after shutdown run = %+v, want waiting_decision under a handed-over token", run)
	}
	if finalized != 0 || decisions.target.Status != "pending" || decisions.target.FencingToken != run.FencingToken {
		t.Fatalf("after shutdown decision = %+v finalized=%d, want pending under the run's token", decisions.target, finalized)
	}

	answerAfterRestart(t, runs, decisions, fence, handle)
}

// A crash leaves the parked run with the dead process's generation and its
// original token. The restarted process takes it over the same way.
func TestMemoryParkedDecisionSurvivesProcessLoss(t *testing.T) {
	runs := newFakeLedger()
	decisions := &fakeDecisionStore{}
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	crashed := NewManager(NewMemoryBackend(), Options{Ledger: runs, Fence: fence, OwnerID: "crashed"})
	crashed.SetDecisionStore(decisions)
	handle := parkAskUser(t, crashed, decisions, "process-loss")
	t.Cleanup(func() { _ = crashed.Close() })

	answerAfterRestart(t, runs, decisions, fence, handle)
}

// Inline waiters live in the exiting process, so shutdown ends their run. The
// decisions the run leaves pending must close with it.
func TestShutdownClosesDecisionsOfRunItCannotHandOver(t *testing.T) {
	ctx := t.Context()
	runs := newFakeLedger()
	decisions := &fakeDecisionStore{}
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(NewMemoryBackend(), Options{Ledger: runs, Fence: fence})
	manager.SetDecisionStore(decisions)
	handle := parkAskUser(t, manager, decisions, "inline-waiter")
	decisions.mu.Lock()
	decisions.target.InlineDecision = true
	decisions.mu.Unlock()
	type cancellation struct {
		runID  string
		token  int64
		reason string
	}
	var canceled []cancellation
	manager.SetLostRunDecisionCanceller(func(_ context.Context, _, _, runID string, token int64, reason string) error {
		canceled = append(canceled, cancellation{runID: runID, token: token, reason: reason})
		return nil
	})

	if err := manager.InterruptForShutdown(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := runs.Get(ctx, handle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateLost {
		t.Fatalf("inline waiter run = %s, want lost", run.State)
	}
	if len(canceled) != 1 || canceled[0].runID != handle.RunID || canceled[0].token != handle.FencingToken || canceled[0].reason != string(ledger.StateLost) {
		t.Fatalf("decision cancellations = %+v, want one for the lost run under its token", canceled)
	}
}

// A request can outlive its run when the run's cleanup failed or an older
// server ended the run. Answering it closes it instead of acknowledging a
// response nothing will apply.
func TestRouteDecisionResponseClosesRequestOfTerminalRun(t *testing.T) {
	ctx := t.Context()
	runs := newFakeLedger()
	runs.InsertClaimed("run-ended", testSessionID, 3, "generation-old")
	if _, _, err := runs.Finalize(ctx, ledger.FinalizeParams{RunID: "run-ended", FencingToken: 3, State: ledger.StateLost}); err != nil {
		t.Fatal(err)
	}
	decisions := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: "ask-run-ended", BotID: testBotID, SessionID: testSessionID,
		RunID: "run-ended", TurnID: "run-ended-turn", Status: "pending", FencingToken: 3,
	}}
	manager := NewManager(NewMemoryBackend(), Options{Ledger: runs})
	manager.SetDecisionStore(decisions)
	var canceled []string
	manager.SetLostRunDecisionCanceller(func(_ context.Context, _, _, runID string, _ int64, _ string) error {
		canceled = append(canceled, runID)
		decisions.setStatus("canceled")
		return nil
	})

	result, err := manager.RouteDecisionResponse(ctx, DecisionResponse{
		ControlID: "answer-ended", Type: CommandUserInputResponse, DecisionID: "ask-run-ended",
		BotID: testBotID, SessionID: testSessionID, RunID: "run-ended", Payload: []byte(`{}`),
	})
	if err != nil || !result.Handled || result.Applied {
		t.Fatalf("answer to ended run = %+v err=%v, want handled and not applied", result, err)
	}
	if len(canceled) != 1 || canceled[0] != "run-ended" || decisions.target.Status != "canceled" {
		t.Fatalf("cancellations = %v status=%s, want the ended run's request closed", canceled, decisions.target.Status)
	}
}

// When a replaced Redis loses its lease index, the generation sweep finds the
// parked run by its stale generation, which the reclaim then replaces. A live
// reservation that fails afterwards has nothing to retry from, so the run ends
// and its question closes instead of staying parked with no owner.
func TestStaleGenerationRecoveryEndsRunItCannotReserve(t *testing.T) {
	const (
		runID     = "run-generation-recovery"
		sessionID = "session-generation-recovery"
	)
	runs := newFakeLedger()
	runs.InsertClaimed(runID, sessionID, 5, "generation-old")
	if _, applied, err := runs.SetWaitingDecision(context.Background(), runID, 5); err != nil || !applied {
		t.Fatalf("park run: applied=%v err=%v", applied, err)
	}
	runs.Mu.Lock()
	runs.Token = 5
	runs.Mu.Unlock()
	decisions := &fakeDecisionStore{target: DecisionTarget{
		Type: CommandUserInputResponse, ID: "decision-generation-recovery",
		BotID: testBotID, SessionID: sessionID, RunID: runID, TurnID: runID + "-turn",
		Status: "pending", FencingToken: 5,
	}}
	backend := newRetryingRecoveryBackend("generation-new", LeaseCandidate{})
	fence := &waitingDecisionRecoveryFence{runs: runs, decisions: decisions}
	manager := NewManager(backend, Options{
		OwnerID: "owner-recovery", OwnerLeaseTTL: time.Hour, Ledger: runs, Fence: fence,
	})
	t.Cleanup(func() { _ = backend.Close() })
	manager.SetDecisionStore(decisions)
	var canceled []int64
	manager.SetLostRunDecisionCanceller(func(_ context.Context, _, _, _ string, token int64, _ string) error {
		canceled = append(canceled, token)
		decisions.setStatus("canceled")
		return nil
	})
	reaper := newTestReaperWithLiveness(t, runs, backend, "generation-new")
	reaper.SetWaitingDecisionRecoverer(manager.recoverWaitingDecision)
	reaper.SetUnfinalizedDecisionCanceller(manager.cancelUnfinalizedDecisions)

	reaper.tick(context.Background())

	run, err := runs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != ledger.StateLost || run.FencingToken != 6 {
		t.Fatalf("unreserved run = %+v, want lost under the reclaimed token 6", run)
	}
	if len(canceled) != 1 || canceled[0] != 6 || decisions.target.Status != "canceled" {
		t.Fatalf("decision cancellations = %v status=%s, want one under token 6", canceled, decisions.target.Status)
	}
	if manager.localControlForScope(testBotID, sessionID, runID) != nil {
		t.Fatal("failed reservation left a local owner behind")
	}
}

package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

type fakeLedger = sessionledger.Store

func newFakeLedger() *fakeLedger { return sessionledger.New() }

type recordedActivation struct {
	botID     string
	sessionID string
	token     int64
}

type fakeFence struct {
	mu          sync.Mutex
	err         error
	activations []recordedActivation
}

func (f *fakeFence) Activate(_ context.Context, botID, sessionID string, token int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activations = append(f.activations, recordedActivation{botID: botID, sessionID: sessionID, token: token})
	return f.err
}

func (f *fakeFence) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeFence) recorded() []recordedActivation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedActivation(nil), f.activations...)
}

type admitFixture struct {
	manager *Manager
	runs    *fakeLedger
	fence   *fakeFence
	// executions counts how many times an admission actually ran, which is the
	// observable that duplicate submission must not increase.
	executions *int64
}

func newAdmitFixture(t *testing.T) admitFixture {
	t.Helper()
	runs := newFakeLedger()
	fence := &fakeFence{}
	manager := NewManager(NewMemoryBackend(), Options{
		OwnerID:       "owner-admit",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		Ledger:        runs,
		Fence:         fence,
	})
	t.Cleanup(func() {
		_ = manager.Close()
	})
	var executions int64
	return admitFixture{manager: manager, runs: runs, fence: fence, executions: &executions}
}

func (f admitFixture) input(invocationID, payload string) AdmitInput {
	return AdmitInput{
		BotID:        testBotID,
		SessionID:    testSessionID,
		InvocationID: invocationID,
		Payload:      []byte(payload),
		Execution: Execution{
			Admission: func(context.Context, RunHandle) (RunAdmissionView, error) {
				*f.executions++
				return RunAdmissionView{}, nil
			},
		},
	}
}

func (f admitFixture) finish(t *testing.T, admission Admission) {
	t.Helper()
	if err := f.manager.FinishRun(context.Background(), admission.Handle, RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if _, _, err := f.runs.Finalize(context.Background(), ledger.FinalizeParams{
		RunID:        admission.RunID,
		FencingToken: admission.Handle.FencingToken,
		State:        ledger.StateCompleted,
	}); err != nil {
		t.Fatalf("finalize ledger run: %v", err)
	}
}

// A first submission must come back owned, fenced and executing, with the token
// that the claim wrote visible on the handle: the caller needs it to fence its
// own durable writes (SR-BASE-001, SR-OWN-002).
func TestAdmitClaimsFencesAndStarts(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	admission, err := f.manager.Admit(context.Background(), f.input("inv-1", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if !admission.Started || admission.Replay {
		t.Fatalf("first admission should start a run: %+v", admission)
	}
	if admission.State != ledger.StateRunning {
		t.Fatalf("claimed run state = %q, want running", admission.State)
	}
	if admission.Handle.FencingToken != 1 || admission.Handle.RunID != admission.RunID {
		t.Fatalf("handle does not name the claimed run: %+v", admission.Handle)
	}
	if *f.executions != 1 {
		t.Fatalf("executions = %d, want 1", *f.executions)
	}

	activations := f.fence.recorded()
	if len(activations) != 1 {
		t.Fatalf("fence activations = %d, want 1", len(activations))
	}
	if activations[0].token != admission.Handle.FencingToken {
		t.Fatalf("fence token = %d, want the claim token %d", activations[0].token, admission.Handle.FencingToken)
	}
	if activations[0].sessionID != testSessionID || activations[0].botID != testBotID {
		t.Fatalf("fence activated for the wrong session: %+v", activations[0])
	}
}

// Every subscriber must agree on the run's turn, not just the caller that
// admitted it (SR-OBS-003). A snapshot naming only the run leaves a reconnecting
// client unable to line the run up against persisted history, which is the one
// thing it reconnects to do.
func TestAdmitPublishesTheTurnInTheObservableSnapshot(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	admission, err := f.manager.Admit(context.Background(), f.input("inv-turn", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if strings.TrimSpace(admission.TurnID) == "" {
		t.Fatal("admission allocated no turn id")
	}

	snapshot, err := f.manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil {
		t.Fatal("snapshot has no current run view")
	}
	if snapshot.CurrentRunView.TurnID != admission.TurnID {
		t.Errorf(
			"snapshot turn_id = %q, want the admitted %q",
			snapshot.CurrentRunView.TurnID,
			admission.TurnID,
		)
	}
	if snapshot.CurrentRunView.RunID != admission.RunID {
		t.Errorf("snapshot run_id = %q, want %q", snapshot.CurrentRunView.RunID, admission.RunID)
	}
}

// The cursor handed back is the session's position at the moment the run became
// observable. A caller that both starts runs and subscribes uses it to place the
// run in the stream it is already reading, so it has to be the same position a
// subscriber would see rather than an approximation of it (SR-OBS-002).
func TestAdmitReportsTheActivationCursor(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	first, err := f.manager.Admit(context.Background(), f.input("inv-cursor", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if first.Cursor.Epoch == "" || first.Cursor.Seq == 0 {
		t.Fatalf("cursor = %+v, want the position the run was published at", first.Cursor)
	}
	snapshot, err := f.manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if first.Cursor != (Cursor{Epoch: snapshot.Epoch, Seq: snapshot.Seq}) {
		t.Fatalf("cursor = %+v, want the live position %q/%d", first.Cursor, snapshot.Epoch, snapshot.Seq)
	}

	// A replay reserved nothing, so this call produced no position. Reporting
	// the current one would answer a question the caller did not ask, and the
	// subscription's snapshot answers it authoritatively.
	replay, err := f.manager.Admit(context.Background(), f.input("inv-cursor", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("replay admit: %v", err)
	}
	if replay.Cursor != (Cursor{}) {
		t.Fatalf("replay cursor = %+v, want none", replay.Cursor)
	}

	// The next run in the same session belongs to the same stream, so its cursor
	// orders after this one instead of restarting alongside it.
	f.finish(t, first)
	second, err := f.manager.Admit(context.Background(), f.input("inv-cursor-2", `{"text":"again"}`))
	if err != nil {
		t.Fatalf("second admit: %v", err)
	}
	if second.Cursor.Epoch != first.Cursor.Epoch || second.Cursor.Seq <= first.Cursor.Seq {
		t.Fatalf("second cursor = %+v, want a later position in epoch %q", second.Cursor, first.Cursor.Epoch)
	}
}

// SR-ADM-001: a retried submission resolves to the same admission and must not
// execute a second time.
func TestAdmitDuplicateInvocationDoesNotExecuteTwice(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	first, err := f.manager.Admit(context.Background(), f.input("inv-retry", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	second, err := f.manager.Admit(context.Background(), f.input("inv-retry", `{"text":"hi"}`))
	if err != nil {
		t.Fatalf("retry admit: %v", err)
	}
	if second.RunID != first.RunID || second.TurnID != first.TurnID {
		t.Fatalf("retry produced a different run: %+v vs %+v", second, first)
	}
	if !second.Replay || second.Started {
		t.Fatalf("retry of an owned run must not start execution: %+v", second)
	}
	if *f.executions != 1 {
		t.Fatalf("executions = %d, want 1", *f.executions)
	}
	if _, claims := f.runs.Counts(); claims != 1 {
		t.Fatalf("claims = %d, want 1", claims)
	}
}

// SR-ADM-001: one retry identity with different content is a conflict, never a
// second run.
func TestAdmitRejectsChangedPayloadForSameInvocation(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	if _, err := f.manager.Admit(context.Background(), f.input("inv-conflict", `{"text":"a"}`)); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	_, err := f.manager.Admit(context.Background(), f.input("inv-conflict", `{"text":"b"}`))
	if !errors.Is(err, ErrInvocationConflict) {
		t.Fatalf("changed payload error = %v, want ErrInvocationConflict", err)
	}
	if *f.executions != 1 {
		t.Fatalf("executions = %d, want 1", *f.executions)
	}
}

// SR-OWN-001: a second invocation arriving while one is active is answered
// busy, and nothing about it is persisted or executed.
func TestAdmitAnswersBusyForConcurrentInvocation(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	first, err := f.manager.Admit(context.Background(), f.input("inv-a", `{"text":"a"}`))
	if err != nil {
		t.Fatalf("first admit: %v", err)
	}
	_, err = f.manager.Admit(context.Background(), f.input("inv-b", `{"text":"b"}`))
	if !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("second invocation error = %v, want ErrSessionBusy", err)
	}
	if *f.executions != 1 {
		t.Fatalf("executions = %d, want 1", *f.executions)
	}
	if _, err := f.runs.GetByInvocation(context.Background(), testSessionID, "inv-b"); !errors.Is(err, ledger.ErrRunNotFound) {
		t.Fatalf("rejected invocation must not be persisted, got %v", err)
	}

	// The same rejected invocation succeeds once the session frees up, which is
	// what makes busy a retryable answer rather than a failure.
	f.finish(t, first)
	retried, err := f.manager.Admit(context.Background(), f.input("inv-b", `{"text":"b"}`))
	if err != nil {
		t.Fatalf("admit after release: %v", err)
	}
	if !retried.Started {
		t.Fatalf("retry after release should start: %+v", retried)
	}
	if retried.Handle.FencingToken <= first.Handle.FencingToken {
		t.Fatalf("successor token %d must exceed %d", retried.Handle.FencingToken, first.Handle.FencingToken)
	}
}

// A claim lost to a peer is an answer, not a failure: the run is progressing
// under an owner that is not this process, so nothing here may execute it.
func TestAdmitLostClaimReportsStateWithoutStarting(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	// Simulate the peer winning between this call's admission and its claim.
	f.runs.ClaimHook = func(runID string) {
		f.runs.Mu.Lock()
		defer f.runs.Mu.Unlock()
		if run, ok := f.runs.Runs[runID]; ok && run.State == ledger.StateAccepted {
			run.State = ledger.StateRunning
			run.OwnerID = "peer"
			run.FencingToken = 99
		}
	}

	admission, err := f.manager.Admit(context.Background(), f.input("inv-race", `{"text":"a"}`))
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if admission.Started {
		t.Fatal("a lost claim must not start execution")
	}
	if admission.State != ledger.StateRunning {
		t.Fatalf("state = %q, want the peer's running state", admission.State)
	}
	if *f.executions != 0 {
		t.Fatalf("executions = %d, want 0", *f.executions)
	}
	if len(f.fence.recorded()) != 0 {
		t.Fatal("a lost claim must not activate the persistence fence")
	}
}

// A claim this process cannot back with a persistence fence is worse than no
// claim: it would hold the session's only slot while unable to write. It must
// end the run under its own token instead.
func TestAdmitReleasesClaimWhenFenceActivationFails(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	f.fence.setErr(errors.New("fence is stale"))

	_, err := f.manager.Admit(context.Background(), f.input("inv-fence", `{"text":"a"}`))
	if err == nil {
		t.Fatal("admit should fail when the fence cannot be activated")
	}
	if *f.executions != 0 {
		t.Fatalf("executions = %d, want 0", *f.executions)
	}
	writes := f.runs.TerminalWrites()
	if len(writes) != 1 {
		t.Fatalf("terminal writes = %d, want 1", len(writes))
	}
	if writes[0].State != ledger.StateFailed || writes[0].FencingToken != 1 {
		t.Fatalf("terminal write = %+v, want failed under token 1", writes[0])
	}
	// The session is free again, so an unrelated submission is admitted rather
	// than told the session is busy.
	f.fence.setErr(nil)
	if _, err := f.manager.Admit(context.Background(), f.input("inv-next", `{"text":"b"}`)); err != nil {
		t.Fatalf("admit after released claim: %v", err)
	}
}

// The same release applies when live reservation is what fails.
func TestAdmitReleasesClaimWhenExecutionCannotStart(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	in := f.input("inv-reserve", `{"text":"a"}`)
	in.Execution.Admission = func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, errors.New("persist user turn failed")
	}

	if _, err := f.manager.Admit(context.Background(), in); err == nil {
		t.Fatal("admit should fail when the run cannot start")
	}
	writes := f.runs.TerminalWrites()
	if len(writes) != 1 || writes[0].State != ledger.StateFailed {
		t.Fatalf("terminal writes = %+v, want one failed write", writes)
	}
	if _, err := f.manager.Admit(context.Background(), f.input("inv-after", `{"text":"b"}`)); err != nil {
		t.Fatalf("admit after released claim: %v", err)
	}
}

// SR-DUR-001: an admission whose owner died before reserving anything is still
// claimable, so a retry of the same invocation adopts it instead of waiting for
// the reaper.
func TestAdmitAdoptsOwnerlessAdmission(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)
	in := f.input("inv-orphan", `{"text":"a"}`)
	f.runs.InsertOrphan("run-orphan", testSessionID, "inv-orphan", fingerprintPayload(in.Payload))

	admission, err := f.manager.Admit(context.Background(), in)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if admission.RunID != "run-orphan" {
		t.Fatalf("run id = %q, want the persisted admission", admission.RunID)
	}
	if !admission.Replay || !admission.Started {
		t.Fatalf("an orphan retry should replay and start: %+v", admission)
	}
	if *f.executions != 1 {
		t.Fatalf("executions = %d, want 1", *f.executions)
	}
}

// A ledger without its persistence fence is a wiring error, not a degraded
// mode: claiming would produce runs whose superseded owners are never fenced.
func TestAdmitRequiresPersistenceFence(t *testing.T) {
	t.Parallel()
	runs := newFakeLedger()
	manager := NewManager(NewMemoryBackend(), Options{OwnerID: "owner-nofence", Ledger: runs})
	t.Cleanup(func() { _ = manager.Close() })

	_, err := manager.Admit(context.Background(), AdmitInput{
		BotID:        testBotID,
		SessionID:    testSessionID,
		InvocationID: "inv-nofence",
		Payload:      []byte(`{"text":"a"}`),
		Execution: Execution{Admission: func(context.Context, RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{}, nil
		}},
	})
	if !errors.Is(err, ErrFenceUnavailable) {
		t.Fatalf("error = %v, want ErrFenceUnavailable", err)
	}
}

func TestAdmitRequiresExecutionBuilder(t *testing.T) {
	t.Parallel()
	f := newAdmitFixture(t)

	_, err := f.manager.Admit(context.Background(), AdmitInput{
		BotID:        testBotID,
		SessionID:    testSessionID,
		InvocationID: "inv-nobuilder",
		Payload:      []byte(`{"text":"a"}`),
	})
	if err == nil {
		t.Fatal("admit without an execution builder should fail")
	}
	if admits, _ := f.runs.Counts(); admits != 0 {
		t.Fatalf("admits = %d, want 0; validation must precede persistence", admits)
	}
}

func TestAdmittedRunParksAndResumesOnUserInputDecision(t *testing.T) {
	t.Parallel()

	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input(
		"invocation-user-input",
		`{"text":"ask before continuing"}`,
	))
	if err != nil {
		t.Fatalf("admit run: %v", err)
	}
	handle := admission.Handle

	if _, err := fixture.manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:        native.EventUserInputRequest,
		ToolName:    "ask_user",
		ToolCallID:  "call-input",
		UserInputID: "input-1",
		Status:      "pending",
	}); err != nil {
		t.Fatalf("publish user input request: %v", err)
	}
	if got := fixture.runs.State(admission.RunID); got != ledger.StateWaitingDecision {
		t.Fatalf("ledger state = %q, want waiting_decision", got)
	}

	// The native stream ends when ask_user defers. Neither its terminal event
	// nor the runner cleanup may release the active run.
	if _, err := fixture.manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentEnd,
	}); err != nil {
		t.Fatalf("publish deferred stream end: %v", err)
	}
	if err := fixture.manager.FinishRun(context.Background(), handle, "", ""); err != nil {
		t.Fatalf("finish deferred stream: %v", err)
	}
	snapshot, err := fixture.manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot waiting run: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusWaitingDecision {
		t.Fatalf("waiting run = %#v", snapshot.CurrentRunView)
	}

	if _, err := fixture.manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentStart,
	}); err != nil {
		t.Fatalf("resume decision run: %v", err)
	}
	if got := fixture.runs.State(admission.RunID); got != ledger.StateRunning {
		t.Fatalf("ledger state after resume = %q, want running", got)
	}
	if _, err := fixture.manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentEnd,
	}); err != nil {
		t.Fatalf("publish resumed stream end: %v", err)
	}
	if err := fixture.manager.FinishRun(context.Background(), handle, "", ""); err != nil {
		t.Fatalf("finish resumed run: %v", err)
	}
	if got := fixture.runs.State(admission.RunID); got != ledger.StateCompleted {
		t.Fatalf("ledger state after completion = %q, want completed", got)
	}
}

func TestConfigurationRunIsMarkedBeforeAdmissionAndStillExcludesOtherRuns(t *testing.T) {
	f := newAdmitFixture(t)
	input := f.input("config", `{"mode_id":"plan"}`)
	input.Execution.ConfigurationOnly = true
	input.Execution.Admission = func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		snapshot, err := f.manager.Snapshot(ctx, testBotID, testSessionID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.CurrentRunView == nil || !snapshot.CurrentRunView.ConfigurationOnly || snapshot.CurrentRunView.Status != RunStatusAdmitting {
			t.Fatalf("first frame looked like generation: %+v", snapshot.CurrentRunView)
		}
		return RunAdmissionView{}, nil
	}
	admission, err := f.manager.Admit(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.manager.Snapshot(t.Context(), testBotID, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.CurrentRunView.ConfigurationOnly || snapshot.CurrentRunView.Status != RunStatusRunning {
		t.Fatalf("active config frame: %+v", snapshot.CurrentRunView)
	}
	encoded, err := marshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := unmarshalSnapshot(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.CurrentRunView.ConfigurationOnly {
		t.Fatal("config marker lost in transport")
	}
	if _, err := f.manager.Admit(t.Context(), f.input("send", `{"text":"hi"}`)); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("configuration released exclusion early: %v", err)
	}
	f.finish(t, admission)
}

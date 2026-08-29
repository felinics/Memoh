package sessionruntime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
)

// fakeLedger is an in-memory ledger with the same guarantees the PostgreSQL
// adapter provides: one active run per session, fenced idempotent transitions,
// and a monotonic token sequence. It exists so admission ordering can be tested
// without a database; the adapter's own SQL is covered by its integration test.
type fakeLedger struct {
	mu   sync.Mutex
	runs map[string]*ledger.Run
	// bySession preserves insertion order so ActiveRun is deterministic.
	order []string
	token int64

	admitErr    error
	claimErr    error
	tokenErr    error
	finalizeErr error
	claimHook   func(runID string)

	admits    int
	claims    int
	finalized []ledger.FinalizeParams
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{runs: map[string]*ledger.Run{}}
}

func (f *fakeLedger) Admit(_ context.Context, params ledger.AdmitParams) (ledger.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admits++
	if f.admitErr != nil {
		return ledger.Run{}, false, f.admitErr
	}
	for _, id := range f.order {
		run := f.runs[id]
		if run.SessionID != params.SessionID {
			continue
		}
		if run.InvocationID == params.InvocationID {
			return *run, false, nil
		}
		if run.State.Active() {
			return ledger.Run{}, false, ledger.ErrSessionBusy
		}
	}
	position := int64(1)
	for _, id := range f.order {
		if f.runs[id].SessionID == params.SessionID {
			position++
		}
	}
	run := &ledger.Run{
		RunID:            params.RunID,
		BotID:            params.BotID,
		SessionID:        params.SessionID,
		InvocationID:     params.InvocationID,
		TurnID:           params.TurnID,
		TurnPosition:     position,
		State:            ledger.StateAccepted,
		Input:            params.Input,
		InputFingerprint: params.InputFingerprint,
		CreatedAt:        time.Now(),
	}
	f.runs[run.RunID] = run
	f.order = append(f.order, run.RunID)
	return *run, true, nil
}

func (f *fakeLedger) Get(_ context.Context, runID string) (ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return *run, nil
}

func (f *fakeLedger) GetByInvocation(_ context.Context, sessionID, invocationID string) (ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		if run := f.runs[id]; run.SessionID == sessionID && run.InvocationID == invocationID {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *fakeLedger) ActiveRun(_ context.Context, sessionID string) (ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		if run := f.runs[id]; run.SessionID == sessionID && run.State.Active() {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *fakeLedger) LatestRun(_ context.Context, sessionID string) (ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.order) - 1; i >= 0; i-- {
		if run := f.runs[f.order[i]]; run.SessionID == sessionID {
			return *run, nil
		}
	}
	return ledger.Run{}, ledger.ErrRunNotFound
}

func (f *fakeLedger) NextFencingToken(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenErr != nil {
		return 0, f.tokenErr
	}
	f.token++
	return f.token, nil
}

func (f *fakeLedger) Claim(_ context.Context, params ledger.ClaimParams) (ledger.Run, bool, error) {
	if f.claimHook != nil {
		f.claimHook(params.RunID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if f.claimErr != nil {
		return ledger.Run{}, false, f.claimErr
	}
	run, ok := f.runs[params.RunID]
	if !ok {
		return ledger.Run{}, false, ledger.ErrRunNotFound
	}
	if run.State != ledger.StateAccepted || run.FencingToken >= params.FencingToken {
		return ledger.Run{}, false, nil
	}
	run.State = ledger.StateRunning
	run.OwnerID = params.OwnerID
	run.FencingToken = params.FencingToken
	run.LiveGeneration = params.LiveGeneration
	run.OwnerSince = time.Now()
	return *run, true, nil
}

func (f *fakeLedger) SetWaitingDecision(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return f.transition(runID, token, ledger.StateWaitingDecision)
}

func (f *fakeLedger) Resume(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return f.transition(runID, token, ledger.StateRunning)
}

func (f *fakeLedger) transition(runID string, token int64, state ledger.State) (ledger.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.FencingToken != token || run.State.Terminal() {
		return ledger.Run{}, false, nil
	}
	run.State = state
	return *run, true, nil
}

func (f *fakeLedger) Finalize(_ context.Context, params ledger.FinalizeParams) (ledger.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalizeErr != nil {
		return ledger.Run{}, false, f.finalizeErr
	}
	run, ok := f.runs[params.RunID]
	if !ok || run.FencingToken != params.FencingToken || run.State.Terminal() {
		return ledger.Run{}, false, nil
	}
	state := params.State
	errorCode := params.ErrorCode
	errorMessage := params.ErrorMessage
	if state == ledger.StateLost && !run.AbortRequestedAt.IsZero() {
		state = ledger.StateAborted
		errorCode = ""
		errorMessage = ""
	}
	run.State = state
	run.ErrorCode = errorCode
	run.ErrorMessage = errorMessage
	f.finalized = append(f.finalized, params)
	return *run, true, nil
}

func (f *fakeLedger) RequestAbort(_ context.Context, runID string) (ledger.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.State.Terminal() {
		return ledger.Run{}, false, nil
	}
	run.AbortRequestedAt = time.Now()
	return *run, true, nil
}

// StaleGenerationRuns mirrors the adapter's keyset sweep: active rows that were
// claimed by an incarnation other than the current one, ordered so a cursor can
// page through them.
func (f *fakeLedger) StaleGenerationRuns(_ context.Context, query ledger.StaleGenerationQuery) ([]ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []ledger.Run
	for _, id := range f.order {
		run := *f.runs[id]
		if !run.State.Active() || run.LiveGeneration == "" || run.LiveGeneration == query.CurrentGeneration {
			continue
		}
		if run.LiveGeneration < query.After.LiveGeneration ||
			(run.LiveGeneration == query.After.LiveGeneration && run.RunID <= query.After.RunID) {
			continue
		}
		matched = append(matched, run)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].LiveGeneration != matched[j].LiveGeneration {
			return matched[i].LiveGeneration < matched[j].LiveGeneration
		}
		return matched[i].RunID < matched[j].RunID
	})
	if query.Limit > 0 && len(matched) > int(query.Limit) {
		matched = matched[:query.Limit]
	}
	return matched, nil
}

func (f *fakeLedger) OrphanedRuns(_ context.Context, query ledger.OrphanQuery) ([]ledger.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-query.MinAge)
	var matched []ledger.Run
	for _, id := range f.order {
		run := *f.runs[id]
		if run.State != ledger.StateAccepted || run.OwnerID != "" || !run.CreatedAt.Before(cutoff) {
			continue
		}
		matched = append(matched, run)
	}
	if query.Limit > 0 && len(matched) > int(query.Limit) {
		matched = matched[:query.Limit]
	}
	return matched, nil
}

// insertOrphan records an admission that committed under a process that died
// before it could claim anything.
func (f *fakeLedger) insertOrphan(runID, sessionID, invocationID, fingerprint string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := &ledger.Run{
		RunID:            runID,
		BotID:            testBotID,
		SessionID:        sessionID,
		InvocationID:     invocationID,
		TurnID:           runID + "-turn",
		TurnPosition:     1,
		State:            ledger.StateAccepted,
		InputFingerprint: fingerprint,
		CreatedAt:        time.Now().Add(-time.Hour),
	}
	f.runs[run.RunID] = run
	f.order = append(f.order, run.RunID)
}

// insertClaimed records a run that some owner took and never finished, which is
// what the reaper finds after that owner disappears.
func (f *fakeLedger) insertClaimed(runID, sessionID string, token int64, generation string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := &ledger.Run{
		RunID:          runID,
		BotID:          testBotID,
		SessionID:      sessionID,
		InvocationID:   runID + "-inv",
		TurnID:         runID + "-turn",
		TurnPosition:   1,
		State:          ledger.StateRunning,
		OwnerID:        "owner-gone",
		FencingToken:   token,
		LiveGeneration: generation,
		OwnerSince:     time.Now().Add(-time.Minute),
		CreatedAt:      time.Now().Add(-time.Minute),
	}
	f.runs[run.RunID] = run
	f.order = append(f.order, run.RunID)
}

func (f *fakeLedger) state(runID string) ledger.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return ""
	}
	return run.State
}

func (f *fakeLedger) errorCode(runID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok {
		return ""
	}
	return run.ErrorCode
}

func (f *fakeLedger) setFinalizeErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizeErr = err
}

func (f *fakeLedger) counts() (admits, claims int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admits, f.claims
}

func (f *fakeLedger) terminalWrites() []ledger.FinalizeParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ledger.FinalizeParams(nil), f.finalized...)
}

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
	if _, claims := f.runs.counts(); claims != 1 {
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
	f.runs.claimHook = func(runID string) {
		f.runs.mu.Lock()
		defer f.runs.mu.Unlock()
		if run, ok := f.runs.runs[runID]; ok && run.State == ledger.StateAccepted {
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
	writes := f.runs.terminalWrites()
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
	writes := f.runs.terminalWrites()
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
	f.runs.insertOrphan("run-orphan", testSessionID, "inv-orphan", fingerprintPayload(in.Payload))

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
	if admits, _ := f.runs.counts(); admits != 0 {
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
	if got := fixture.runs.state(admission.RunID); got != ledger.StateWaitingDecision {
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
	if got := fixture.runs.state(admission.RunID); got != ledger.StateRunning {
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
	if got := fixture.runs.state(admission.RunID); got != ledger.StateCompleted {
		t.Fatalf("ledger state after completion = %q, want completed", got)
	}
}

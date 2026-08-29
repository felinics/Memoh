package compaction

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestRunCompactionSyncReportsScopedNoop(t *testing.T) {
	// An already-compact/fresh session compacts nothing. The result must be a
	// scoped no-op, so a caller (the HTTP endpoint) never has to fall back to an
	// unscoped bot-wide log that could belong to another session.
	q := &fakeQueries{}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)

	res, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 100))
	if err != nil {
		t.Fatalf("no-op must not error: %v", err)
	}
	if res.Status != StatusNoop || res.MessageCount != 0 || res.Summary != "" {
		t.Fatalf("no-op result = %+v, want noop/0/empty", res)
	}
	if stub.calls != 0 || q.created {
		t.Fatalf("no-op must not call the model or create a log (calls=%d created=%v)", stub.calls, q.created)
	}
}

func TestRunCompactionSyncReportsScopedSummaryOnSuccess(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "SUMMARY-OK"}
	svc := newMachineryService(q)

	res, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 450))
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK || res.Summary != "SUMMARY-OK" || res.MessageCount != len(q.markedIDs) {
		t.Fatalf("success result = %+v, want ok/SUMMARY-OK/%d", res, len(q.markedIDs))
	}
}

func TestRunCompactionSyncFailureSurfacesError(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	cfg := machineryConfig(&stubModel{}, 450)
	cfg.HTTPClient = &http.Client{Transport: &failingModel{}}

	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err == nil {
		t.Fatal("a summarizer failure must surface an error, not a stale result")
	}
	if res.Status == StatusOK {
		t.Fatalf("failed result must not report ok: %+v", res)
	}
}

func TestRunCompactionFailureCooldownSkipsImmediateRetry(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	now := time.Now()
	svc.nowFn = func() time.Time { return now }

	cfg := machineryConfig(&stubModel{}, 450)
	fail := &failingModel{}
	cfg.HTTPClient = &http.Client{Transport: fail}

	if _, err := svc.RunCompactionSync(context.Background(), cfg); err == nil {
		t.Fatal("first attempt must run and fail")
	}
	if fail.calls != 1 {
		t.Fatalf("calls = %d, want 1", fail.calls)
	}

	if _, err := svc.RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("cooldown skip must not surface an error: %v", err)
	}
	if fail.calls != 1 {
		t.Fatalf("immediate retry within cooldown must be skipped, calls=%d", fail.calls)
	}

	now = now.Add(compactionFailureCooldown + time.Second)
	if _, err := svc.RunCompactionSync(context.Background(), cfg); err == nil {
		t.Fatal("attempt after cooldown must run and fail again")
	}
	if fail.calls != 2 {
		t.Fatalf("attempt after cooldown should run, calls=%d", fail.calls)
	}
}

func TestRunCompactionManualRequestBypassesFailureCooldown(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	now := time.Now()
	svc.nowFn = func() time.Time { return now }

	autoCfg := machineryConfig(&stubModel{}, 450)
	autoCfg.HTTPClient = &http.Client{Transport: &failingModel{}}
	if _, err := svc.RunCompactionSync(context.Background(), autoCfg); err == nil {
		t.Fatal("first automatic attempt must run and fail, arming the cooldown")
	}

	// The user fixes the model and presses compact within the cooldown window.
	// A manual request must actually run (not be skipped and reported as done):
	// it compacts and reports a real result instead of a false success.
	manualStub := &stubModel{summary: "recovered by manual run"}
	manualCfg := autoCfg
	manualCfg.Manual = true
	manualCfg.HTTPClient = &http.Client{Transport: manualStub}
	if _, err := svc.RunCompactionSync(context.Background(), manualCfg); err != nil {
		t.Fatalf("manual compaction must run despite cooldown: %v", err)
	}
	if manualStub.calls != 1 {
		t.Fatalf("manual request must call the model, not skip on cooldown (calls=%d)", manualStub.calls)
	}
	if !q.created || len(q.markedIDs) == 0 || q.completed.Status != "ok" {
		t.Fatalf("manual run must do real work: created=%v marked=%d status=%q", q.created, len(q.markedIDs), q.completed.Status)
	}

	// An automatic request in the same window still respects the cooldown: the
	// manual success above cleared it, so this one runs — proving cooldown is a
	// shared per-session state that manual participates in, not a bypass leak.
	autoRetry := &failingModel{}
	autoRetryCfg := autoCfg
	autoRetryCfg.HTTPClient = &http.Client{Transport: autoRetry}
	if _, err := svc.RunCompactionSync(context.Background(), autoRetryCfg); err == nil {
		t.Fatal("automatic retry after a successful manual run should proceed and fail")
	}
	if autoRetry.calls != 1 {
		t.Fatalf("manual success must clear the shared cooldown for automatic runs too (calls=%d)", autoRetry.calls)
	}
}

func TestRunCompactionManualFailureStillSurfacesError(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	now := time.Now()
	svc.nowFn = func() time.Time { return now }

	autoCfg := machineryConfig(&stubModel{}, 450)
	autoCfg.HTTPClient = &http.Client{Transport: &failingModel{}}
	if _, err := svc.RunCompactionSync(context.Background(), autoCfg); err == nil {
		t.Fatal("automatic attempt must fail to arm the cooldown")
	}

	// A manual request that also fails must surface the real error, never a
	// silent nil that callers render as "done".
	manualFail := &failingModel{}
	manualCfg := autoCfg
	manualCfg.Manual = true
	manualCfg.HTTPClient = &http.Client{Transport: manualFail}
	if _, err := svc.RunCompactionSync(context.Background(), manualCfg); err == nil {
		t.Fatal("a failing manual compaction must return an error, not a false success")
	}
	if manualFail.calls != 1 {
		t.Fatalf("manual request must attempt the model despite cooldown (calls=%d)", manualFail.calls)
	}
}

func TestRunCompactionFailureCooldownClearsOnSuccess(t *testing.T) {
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	now := time.Now()
	svc.nowFn = func() time.Time { return now }

	sessionCfg := machineryConfig(&stubModel{}, 450)

	failCfg := sessionCfg
	failCfg.HTTPClient = &http.Client{Transport: &failingModel{}}
	if _, err := svc.RunCompactionSync(context.Background(), failCfg); err == nil {
		t.Fatal("expected initial failure")
	}

	now = now.Add(compactionFailureCooldown + time.Second)
	successStub := &stubModel{summary: "recovered"}
	successCfg := sessionCfg
	successCfg.HTTPClient = &http.Client{Transport: successStub}
	if _, err := svc.RunCompactionSync(context.Background(), successCfg); err != nil {
		t.Fatalf("attempt after cooldown should succeed: %v", err)
	}
	if successStub.calls != 1 {
		t.Fatalf("success attempt should have called the model once, got %d", successStub.calls)
	}

	retryFail := &failingModel{}
	retryCfg := sessionCfg
	retryCfg.HTTPClient = &http.Client{Transport: retryFail}
	if _, err := svc.RunCompactionSync(context.Background(), retryCfg); err == nil {
		t.Fatal("expected failure from immediate retry model")
	}
	if retryFail.calls != 1 {
		t.Fatalf("success must have cleared the cooldown, allowing an immediate retry; calls=%d", retryFail.calls)
	}
}

type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

func TestRunCompactionSyncInterruptedRunDoesNotArmCooldown(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ctx  func(t *testing.T) context.Context
	}{
		{"caller canceled", func(t *testing.T) context.Context {
			t.Helper()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{"caller deadline exceeded", func(t *testing.T) context.Context {
			t.Helper()
			ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
			t.Cleanup(cancel)
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeQueries{uncompacted: machineryCorpus(t)}
			svc := newMachineryService(q)
			stub := &stubModel{summary: "recovered summary"}
			cfg := machineryConfig(stub, 200)

			interruptedCtx := tc.ctx(t)
			interrupted := cfg
			interrupted.HTTPClient = &http.Client{Transport: errTransport{err: interruptedCtx.Err()}}
			if _, err := svc.RunCompactionSync(interruptedCtx, interrupted); err == nil {
				t.Fatal("interrupted run must surface an error")
			}

			res, err := svc.RunCompactionSync(context.Background(), cfg)
			if err != nil {
				t.Fatalf("run after interruption: %v", err)
			}
			if res.Status != StatusOK {
				t.Fatalf("status after interruption = %q, want %q (cooldown must not be armed)", res.Status, StatusOK)
			}
		})
	}
}

func TestRunCompactionSyncClientTimeoutStillArmsCooldown(t *testing.T) {
	t.Parallel()

	// The HTTP client's own timeout fires with the caller's context still
	// live: the model is genuinely too slow, so the cooldown must arm —
	// otherwise the sync backstop re-attempts the same slow call every turn.
	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	stub := &stubModel{summary: "healthy summary"}
	cfg := machineryConfig(stub, 200)

	slow := cfg
	slow.HTTPClient = &http.Client{Transport: errTransport{err: context.DeadlineExceeded}}
	if _, err := svc.RunCompactionSync(context.Background(), slow); err == nil {
		t.Fatal("timed-out run must surface an error")
	}

	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run during cooldown: %v", err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("status during cooldown = %q, want %q", res.Status, StatusNoop)
	}
	if stub.calls != 0 {
		t.Fatalf("model called %d times during cooldown, want 0", stub.calls)
	}
}

func TestRunCompactionSyncModelFailureStillArmsCooldown(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	svc := newMachineryService(q)
	stub := &stubModel{summary: "healthy summary"}
	cfg := machineryConfig(stub, 200)

	failing := cfg
	failing.HTTPClient = &http.Client{Transport: errTransport{err: errors.New("upstream 500")}}
	if _, err := svc.RunCompactionSync(context.Background(), failing); err == nil {
		t.Fatal("failing run must surface an error")
	}

	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run during cooldown: %v", err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("status during cooldown = %q, want %q", res.Status, StatusNoop)
	}
	if stub.calls != 0 {
		t.Fatalf("model called %d times during cooldown, want 0", stub.calls)
	}
}

func TestRunCompactionSyncSurfacesCompletionPersistenceFailure(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t), completeErr: errors.New("db down")}
	svc := newMachineryService(q)
	stub := &stubModel{summary: "durable summary"}

	res, err := svc.RunCompactionSync(context.Background(), machineryConfig(stub, 200))
	if err == nil {
		t.Fatal("completion persistence failure must surface an error")
	}
	if res.Status == StatusOK {
		t.Fatal("result must not claim ok when the summary was never persisted")
	}
}

func TestDoCompactionSharesPromptBudgetWithPriorContext(t *testing.T) {
	t.Parallel()

	rows := make([]sqlc.ListUncompactedMessagesBySessionRow, 0, 13)
	for i := 0; i < 12; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		rows = append(rows, textRow(t, role, 100))
	}
	rows = append(rows, mkRow(t, "user", `"current"`, 40))

	run := func(t *testing.T, withPrior bool) int {
		t.Helper()
		stub := &stubModel{summary: "condensed"}
		cfg := machineryConfig(stub, 100)
		cfg.MaxCompactTokens = 1000
		var priorLogs []sqlc.BotHistoryMessageCompact
		if withPrior {
			priorLogs = []sqlc.BotHistoryMessageCompact{priorLogFor(t, cfg, strings.Repeat("p", 1200))}
		}
		q := &fakeQueries{uncompacted: rows, priorLogs: priorLogs}
		svc := newMachineryService(q)
		res, err := svc.RunCompactionSync(context.Background(), cfg)
		if err != nil {
			t.Fatalf("RunCompactionSync: %v", err)
		}
		if res.Status != StatusOK {
			t.Fatalf("status = %q, want %q", res.Status, StatusOK)
		}
		return len(q.markedIDs)
	}

	without := run(t, false)
	with := run(t, true)
	if with >= without {
		t.Fatalf("prior context must carve out of the entries budget: marked %d with prior, %d without", with, without)
	}
}

func TestDoCompactionSacrificesPriorContextForOversizedEntries(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 900),
		textRow(t, "user", 100),
		mkRow(t, "user", `"current"`, 40),
	}
	prior := strings.Repeat("history so far ", 70) // ~262 tokens, within the 1/4 allowance
	stub := &stubModel{summary: "condensed"}
	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 1000
	q := &fakeQueries{
		uncompacted: rows,
		priorLogs:   []sqlc.BotHistoryMessageCompact{priorLogFor(t, cfg, prior)},
	}
	svc := newMachineryService(q)
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, StatusOK)
	}
	if len(q.markedIDs) != 1 || q.markedIDs[0] != rows[0].ID {
		t.Fatalf("marked = %v, want the oversized first markable turn", q.markedIDs)
	}
	// The oversized kept entries exceed their budget share, so the reference
	// prior context must shrink (truncate) to keep the combined prompt within
	// MaxCompactTokens instead of stacking on top of it.
	if strings.Contains(stub.prompt, prior) {
		t.Fatal("full prior context must not ride along with oversized entries")
	}
	// stub.prompt concatenates the fixed system prompt (~190 tokens) and the
	// user-prompt wrapper (~90 tokens) on top of the budgeted prior+entries;
	// an additive-budget regression overshoots this bound by the full prior.
	if got := estimateBytesAsTokens(stub.prompt); got > 1000+320 {
		t.Fatalf("combined prompt ~%d tokens, want within MaxCompactTokens plus the fixed overhead", got)
	}
}

func TestDoCompactionCountsPriorSeparatorsInSharedBudget(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "condensed"}
	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 1000
	priors := make([]sqlc.BotHistoryMessageCompact, 0, 100)
	for i := 0; i < 100; i++ {
		priors = append(priors, priorLogFor(t, cfg, "abcd"))
	}
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 917),
		mkRow(t, "user", `"current"`, 40),
	}
	q := &fakeQueries{uncompacted: rows, priorLogs: priors}
	svc := newMachineryService(q)
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("RunCompactionSync = %v, %v", res, err)
	}
	if got := estimateBytesAsTokens(stub.prompt); got > 1000+320 {
		t.Fatalf("combined prompt ~%d tokens: prior separators must count toward the shared budget", got)
	}
}

func TestDoCompactionTruncatesEntriesPastTheTotalCap(t *testing.T) {
	t.Parallel()

	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		textRow(t, "user", 1202),
		mkRow(t, "user", `"current"`, 40),
	}
	q := &fakeQueries{uncompacted: rows}
	stub := &stubModel{summary: "condensed"}
	svc := newMachineryService(q)

	cfg := machineryConfig(stub, 100)
	cfg.MaxCompactTokens = 1000
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, StatusOK)
	}
	if len(q.markedIDs) != 1 || q.markedIDs[0] != rows[0].ID {
		t.Fatalf("marked = %v, want the oversized row", q.markedIDs)
	}
	if !strings.Contains(stub.prompt, truncationMarker) {
		t.Fatal("an entry larger than the whole budget must be truncated, not sent verbatim")
	}
	if got := estimateBytesAsTokens(stub.prompt); got > 1000+320 {
		t.Fatalf("combined prompt ~%d tokens, want within the total cap plus fixed overhead", got)
	}
}

func TestRunCompactionSyncAttachesToOwnerDuringCooldown(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	svc.recordCompactionFailure(cfg.SessionID)
	run, ok := svc.beginSessionCompaction(cfg.SessionID)
	if !ok {
		t.Fatal("manual owner acquisition must succeed")
	}

	got := make(chan Result, 1)
	go func() {
		res, err := svc.RunCompactionSync(context.Background(), cfg) // auto path, cooldown armed
		if err != nil {
			t.Errorf("waiter: %v", err)
		}
		got <- res
	}()
	awaitWaiter(t, run) // cooldown must not hide the running owner

	want := Result{Status: StatusOK, Summary: "manual retry summary", MessageCount: 2}
	svc.endSessionCompaction(cfg.SessionID, run, want, nil)
	if res := <-got; res != want {
		t.Fatalf("auto sync must reuse the cooldown-bypassing owner's result, got %#v", res)
	}
}

func TestRunCompactionPanicArmsCooldownAndReleasesSlot(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t), listPanic: true}
	stub := &stubModel{summary: "recovered"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = svc.RunCompactionSync(context.Background(), cfg)
	}()
	if r := <-recovered; r == nil {
		t.Fatal("the panic must propagate, not be swallowed")
	}

	q.listPanic = false
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run after panic: %v", err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("a panicked run must arm the cooldown, got %q", res.Status)
	}

	manual := cfg
	manual.Manual = true
	res, err = svc.RunCompactionSync(context.Background(), manual)
	if err != nil {
		t.Fatalf("manual run after panic: %v", err)
	}
	if res.Status != StatusOK {
		t.Fatalf("the slot must be released after a panic, got %q", res.Status)
	}
}

func TestManualRunBypassesACooldownNoopOwner(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "manual summary"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	svc.recordCompactionFailure(cfg.SessionID)
	run, ok := svc.beginSessionCompaction(cfg.SessionID) // an automatic run holding the slot
	if !ok {
		t.Fatal("owner acquisition must succeed")
	}

	manual := cfg
	manual.Manual = true
	got := make(chan Result, 1)
	go func() {
		res, err := svc.RunCompactionSync(context.Background(), manual)
		if err != nil {
			t.Errorf("manual: %v", err)
		}
		got <- res
	}()
	awaitWaiter(t, run)

	// The automatic owner publishes its cooldown noop; the manual request must
	// retry and actually run instead of inheriting the skip.
	svc.endSessionCompaction(cfg.SessionID, run, Result{Status: StatusNoop}, nil)
	res := <-got
	if res.Status != StatusOK {
		t.Fatalf("manual run inherited the owner's noop, got %#v", res)
	}
	if stub.calls != 1 {
		t.Fatalf("model called %d times, want 1", stub.calls)
	}
}

func TestCanceledManualWaiterDoesNotRetryThroughANoopOwner(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 450)

	run, ok := svc.beginSessionCompaction(cfg.SessionID)
	if !ok {
		t.Fatal("owner acquisition must succeed")
	}

	manual := cfg
	manual.Manual = true
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Result, 1)
	go func() {
		res, err := svc.RunCompactionSync(ctx, manual)
		if err != nil {
			t.Errorf("canceled manual waiter: %v", err)
		}
		got <- res
	}()
	awaitWaiter(t, run)

	cancel()
	svc.endSessionCompaction(cfg.SessionID, run, Result{Status: StatusNoop}, nil)
	if res := <-got; res.Status != StatusNoop {
		t.Fatalf("canceled manual waiter must degrade to noop, got %#v", res)
	}
	if stub.calls != 0 || len(q.markedIDs) != 0 {
		t.Fatalf("canceled manual waiter ran compaction (calls=%d marked=%d)", stub.calls, len(q.markedIDs))
	}
}

// TestWaitForOwnerManualBranches pins the owner-done arm deterministically:
// the racing integration test above almost always wakes on ctx.Done(), so the
// post-noop context recheck is driven directly through the seam.
func TestWaitForOwnerManualBranches(t *testing.T) {
	t.Parallel()

	closedOwner := func(res Result, err error) *inflightRun {
		run := &inflightRun{done: make(chan struct{}), res: res, err: err}
		close(run.done)
		return run
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	manual := TriggerConfig{Manual: true}

	// doneHidden forces the owner-done select arm while Err() still reports
	// cancellation, pinning the post-noop context recheck deterministically
	// (with both channels ready the select would pick randomly).
	doneHidden := doneHiddenContext{canceled}
	res, retry, err := waitForOwner(doneHidden, manual, closedOwner(Result{Status: StatusNoop}, nil))
	if retry || err != nil || res.Status != StatusNoop {
		t.Fatalf("canceled manual caller must not retry through a noop owner: res=%#v err=%v retry=%v", res, err, retry)
	}

	res, retry, err = waitForOwner(canceled, manual, closedOwner(Result{Status: StatusNoop}, nil))
	if retry || err != nil || res.Status != StatusNoop {
		t.Fatalf("either select arm must degrade a canceled manual caller to noop: res=%#v err=%v retry=%v", res, err, retry)
	}

	_, retry, err = waitForOwner(context.Background(), manual, closedOwner(Result{Status: StatusNoop}, nil))
	if !retry || err != nil {
		t.Fatalf("live manual caller must retry through a noop owner: err=%v retry=%v", err, retry)
	}

	want := Result{Status: StatusOK, Summary: "s", MessageCount: 1}
	res, retry, err = waitForOwner(doneHiddenContext{canceled}, manual, closedOwner(want, nil))
	if retry || err != nil || res != want {
		t.Fatalf("a completed owner's ok result must be reused even after cancel: res=%#v err=%v retry=%v", res, err, retry)
	}

	res, retry, err = waitForOwner(context.Background(), TriggerConfig{}, closedOwner(Result{Status: StatusNoop}, nil))
	if retry || err != nil || res.Status != StatusNoop {
		t.Fatalf("automatic callers never retry: res=%#v err=%v retry=%v", res, err, retry)
	}
}

type panickingHookProvider struct {
	armed   atomic.Bool
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *panickingHookProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	p.calls.Add(1)
	if p.armed.Load() {
		if p.entered != nil {
			select {
			case p.entered <- struct{}{}:
			default:
			}
			<-p.release
		}
		panic("boom: injected hook provider panic")
	}
	return nil, errors.New("no workspace client")
}

func (s *Service) inflightRunFor(sessionID string) *inflightRun {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	return s.inflight[sessionID]
}

func TestPreHookPanicArmsCooldownAndReleasesSlot(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	provider := &panickingHookProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	provider.armed.Store(true)
	svc.SetHookService(hooks.NewService(slog.New(slog.DiscardHandler), provider))
	cfg := machineryConfig(stub, 450)

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = svc.RunCompactionSync(context.Background(), cfg)
	}()
	<-provider.entered // the owner is inside the pre-hook, holding the slot

	waiterErr := make(chan error, 1)
	go func() {
		_, err := svc.RunCompactionSync(context.Background(), cfg)
		waiterErr <- err
	}()
	run := svc.inflightRunFor(cfg.SessionID)
	if run == nil {
		t.Fatal("owner run must be registered while inside the pre-hook")
	}
	awaitWaiter(t, run)

	close(provider.release)
	if r := <-recovered; r == nil {
		t.Fatal("a pre-hook panic must propagate, not be swallowed")
	}
	if err := <-waiterErr; err == nil {
		t.Fatal("a waiter attached to a panicking owner must receive a failure, not a zero-value success")
	}

	provider.armed.Store(false)
	before := provider.calls.Load()
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("auto run after pre-hook panic: %v", err)
	}
	if res.Status != StatusNoop {
		t.Fatalf("a pre-hook panic must arm the cooldown, got %q", res.Status)
	}
	if provider.calls.Load() != before {
		t.Fatal("the cooldown noop must not reach the pre-hook provider")
	}

	manual := cfg
	manual.Manual = true
	if _, err := svc.RunCompactionSync(context.Background(), manual); err == nil {
		t.Fatal("manual bypass must reach the failing pre-hook, proving the slot was released")
	}
	if stub.calls != 0 {
		t.Fatalf("model called %d times behind the failing pre-hook, want 0", stub.calls)
	}
}

// TestPostCompactHookPanicDoesNotPoisonASuccessfulRun pins the post-hook
// panic containment: the compaction itself succeeded and its outcome is
// already persisted, so a panicking PostCompact hook must not escape the
// recovery defer (crashing async triggers), must not turn the published
// result into a lie, and must not arm the failure cooldown. The panicking
// hook service is swapped in mid-run, after the pre-hook already passed.
func TestPostCompactHookPanicDoesNotPoisonASuccessfulRun(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: machineryCorpus(t)}
	stub := &stubModel{summary: "SUMMARY-OK"}
	svc := newMachineryService(q)
	provider := &panickingHookProvider{}
	provider.armed.Store(true)
	q.onComplete = func() {
		svc.SetHookService(hooks.NewService(slog.New(slog.DiscardHandler), provider))
	}
	cfg := machineryConfig(stub, 450)

	recovered := make(chan any, 1)
	results := make(chan Result, 1)
	errs := make(chan error, 1)
	go func() {
		defer func() { recovered <- recover() }()
		res, err := svc.RunCompactionSync(context.Background(), cfg)
		results <- res
		errs <- err
	}()
	if r := <-recovered; r != nil {
		t.Fatalf("a post-hook panic must be contained, but escaped: %v", r)
	}
	if err := <-errs; err != nil {
		t.Fatalf("a post-hook panic must not fail the completed compaction: %v", err)
	}
	if res := <-results; res.Status != StatusOK || res.Summary != "SUMMARY-OK" {
		t.Fatalf("completed compaction result lost to a post-hook panic: %#v", res)
	}
	if provider.calls.Load() == 0 {
		t.Fatal("the post-hook must actually have run (and panicked)")
	}

	q.onComplete = nil
	svc.SetHookService(nil)
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("a contained post-hook panic must not arm the cooldown: res=%#v err=%v", res, err)
	}
}

// TestExpiredFailureCooldownEntryIsDropped keeps failedAt from growing with
// sessions that failed once and never came back: an entry found expired is
// deleted on the spot instead of lingering until a later success or restart.
func TestExpiredFailureCooldownEntryIsDropped(t *testing.T) {
	t.Parallel()

	svc := newMachineryService(&fakeQueries{})
	now := time.Now()
	svc.nowFn = func() time.Time { return now }

	svc.recordCompactionFailure("session-1")
	if !svc.inFailureCooldown("session-1") {
		t.Fatal("fresh failure must be in cooldown")
	}

	now = now.Add(compactionFailureCooldown + time.Second)
	if svc.inFailureCooldown("session-1") {
		t.Fatal("cooldown must expire")
	}
	svc.inflightMu.Lock()
	_, lingering := svc.failedAt["session-1"]
	svc.inflightMu.Unlock()
	if lingering {
		t.Fatal("expired cooldown entry must be dropped from failedAt")
	}
}

type doneHiddenContext struct{ context.Context }

func (doneHiddenContext) Done() <-chan struct{} { return nil }

func priorLogFor(t *testing.T, cfg TriggerConfig, summary string) sqlc.BotHistoryMessageCompact {
	t.Helper()
	return sqlc.BotHistoryMessageCompact{
		ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
		BotID:     pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true},
		SessionID: pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true},
		Status:    "ok",
		Summary:   summary,
	}
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimefence"
)

// After a history clear the run a client sees must be gone: from the live
// projection, from a subscriber that was watching when the history went, and
// from a runtime that lost its live state and falls back to the ledger. The
// clear is the one DELETE /bots/{bot_id}/messages performs: the runtime reset
// gate, the deletion inside it, then the release.

type historyResetScope string

const (
	historyResetSession historyResetScope = "session"
	historyResetBot     historyResetScope = "bot"
)

// clearHistory runs the clear; during, when set, runs inside the reset before
// the history is deleted.
func (h wsStepHistoryHarness) clearHistory(t *testing.T, scope historyResetScope, during func()) {
	t.Helper()
	if err := h.clearHistoryErr(context.Background(), scope, during); err != nil {
		t.Fatal(err)
	}
}

func (h wsStepHistoryHarness) clearHistoryErr(ctx context.Context, scope historyResetScope, during func()) error {
	var (
		resetCtx context.Context
		release  func()
		err      error
	)
	if scope == historyResetBot {
		resetCtx, release, err = h.manager.BeginBotHistoryReset(ctx, h.botID)
	} else {
		resetCtx, release, err = h.manager.BeginSessionHistoryReset(ctx, h.botID, h.sessionID)
	}
	if err != nil {
		return fmt.Errorf("begin %s history reset: %w", scope, err)
	}
	defer release()
	if during != nil {
		during()
	}
	if scope == historyResetBot {
		err = h.messages.DeleteByBot(resetCtx, h.botID)
	} else {
		err = h.messages.DeleteBySession(resetCtx, h.sessionID)
	}
	if err != nil {
		return fmt.Errorf("delete %s history: %w", scope, err)
	}
	return nil
}

// restarted is a second runtime over the same PostgreSQL ledger whose live
// backend holds nothing: a process restart on the memory backend, or Redis
// state loss.
func (h wsStepHistoryHarness) restarted(t *testing.T) *sessionruntime.Manager {
	t.Helper()
	queries := postgresstore.NewQueriesWithPool(h.pool, dbsqlc.New(h.pool))
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-restarted-" + uuid.NewString(),
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Minute,
		Ledger:        ledger.NewPostgres(dbsqlc.New(h.pool), h.pool),
		Fence:         runtimefence.NewActivator(queries),
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func mustSnapshot(t *testing.T, manager *sessionruntime.Manager, botID, sessionID string) sessionruntime.Snapshot {
	t.Helper()
	snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	return snapshot
}

func describeRun(run *sessionruntime.CurrentRunView) string {
	if run == nil {
		return "<none>"
	}
	return run.RunID + " status=" + run.Status + " error_code=" + run.ErrorCode
}

// webRuntimeView follows a session the way the Web runtime client
// (apps/web/src/store/chat/runtime-client.ts) does, so a test sees the run the
// page shows rather than the frames it was sent: a snapshot applies only while
// the client awaits one or when it moves the cursor forward, and a dropped or
// closed stream makes the client subscribe again and await the next snapshot.
// The socket refuses to subscribe to a deleted session, and the client then
// keeps what it shows.
type webRuntimeView struct {
	t                *testing.T
	manager          *sessionruntime.Manager
	pool             *pgxpool.Pool
	botID, sessionID string
	sub              sessionruntime.Subscription
	awaiting         bool
	epoch            string
	seq              int64
	run              *sessionruntime.CurrentRunView
}

func (h wsStepHistoryHarness) watch(t *testing.T) *webRuntimeView {
	t.Helper()
	v := &webRuntimeView{t: t, manager: h.manager, pool: h.pool, botID: h.botID, sessionID: h.sessionID}
	v.resubscribe()
	t.Cleanup(func() { v.sub.Close() })
	return v
}

func (v *webRuntimeView) resubscribe() {
	v.t.Helper()
	if v.sub.Close != nil {
		v.sub.Close()
	}
	v.awaiting = true
	var deleted bool
	if err := v.pool.QueryRow(context.Background(), `SELECT deleted_at IS NOT NULL FROM bot_sessions WHERE id = $1`, v.sessionID).Scan(&deleted); err != nil {
		v.t.Fatal(err)
	}
	if deleted {
		v.sub = sessionruntime.Subscription{Close: func() {}}
		return
	}
	sub, err := v.manager.Subscribe(context.Background(), v.botID, v.sessionID)
	if err != nil {
		v.t.Fatalf("Subscribe() error = %v", err)
	}
	v.sub = sub
}

func (v *webRuntimeView) apply(event sessionruntime.Event, open bool) {
	v.t.Helper()
	switch {
	case !open || event.Type == sessionruntime.EventRuntimeDropped:
		if !v.awaiting || !open {
			v.resubscribe()
		}
	case event.Type == sessionruntime.EventRuntimeSnapshot && event.Snapshot != nil:
		if !v.awaiting && event.Epoch == v.epoch && event.Seq <= v.seq {
			return
		}
		v.awaiting = false
		v.epoch, v.seq, v.run = event.Epoch, event.Seq, event.Snapshot.CurrentRunView
	case event.Type == sessionruntime.EventRuntimeDelta && event.Delta != nil && !v.awaiting:
		switch {
		case event.Epoch != v.epoch || event.Seq > v.seq+1:
			v.resubscribe()
			return
		case event.Seq <= v.seq:
			return
		}
		v.seq = event.Seq
		if event.Delta.CurrentRunView != nil {
			v.run = event.Delta.CurrentRunView
		}
		if patch := event.Delta.Run; patch != nil && v.run != nil && v.run.RunID == patch.RunID {
			run := *v.run
			if patch.Status != nil {
				run.Status = *patch.Status
			}
			if patch.ErrorCode != nil {
				run.ErrorCode = *patch.ErrorCode
			}
			v.run = &run
		}
	}
}

// follow applies what the stream delivers for d, or until settled returns
// true, and reports whether it did.
func (v *webRuntimeView) follow(d time.Duration, settled func() bool) bool {
	v.t.Helper()
	deadline := time.After(d)
	for settled == nil || !settled() {
		select {
		case event, open := <-v.sub.C:
			v.apply(event, open)
		case <-deadline:
			return settled == nil
		}
	}
	return true
}

// drain applies everything the client has been sent so far.
func (v *webRuntimeView) drain() { v.follow(200*time.Millisecond, nil) }

// awaitReload lets a reload the reset triggered reach the client before the
// test goes on; a reset that does not trigger one leaves nothing to wait for.
func (v *webRuntimeView) awaitReload() {
	epoch, seq := v.epoch, v.seq
	v.follow(time.Second, func() bool { return !v.awaiting && (v.epoch != epoch || v.seq != seq) })
}

// awaitRun follows the stream until the client shows runID.
func (v *webRuntimeView) awaitRun(runID string) {
	v.t.Helper()
	if !v.follow(5*time.Second, func() bool { return !v.awaiting && v.run != nil && v.run.RunID == runID }) {
		v.t.Fatalf("client shows %s, want run %s", describeRun(v.run), runID)
	}
}

// awaitCleared follows the stream until the client shows no run, then checks
// nothing that follows brings the cleared run back.
func (v *webRuntimeView) awaitCleared(staleRunID string) {
	v.t.Helper()
	if !v.follow(5*time.Second, func() bool { return !v.awaiting && v.run == nil }) {
		v.t.Fatalf("client still shows %s after the clear", describeRun(v.run))
	}
	v.follow(300*time.Millisecond, nil)
	if v.run != nil && v.run.RunID == staleRunID {
		v.t.Fatalf("cleared run came back to the client: %s", describeRun(v.run))
	}
}

func TestPostgresHistoryResetClearsRuntimeSnapshot(t *testing.T) {
	for _, mode := range []wsStepHistoryModel{wsStepHistorySuccess, wsStepHistoryAuthFailure} {
		for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
			t.Run(string(mode)+"/"+string(scope), func(t *testing.T) {
				assertHistoryResetClearsRuntimeSnapshot(t, newWSStepHistoryHarness(t, mode), scope, nil)
			})
		}
	}
}

func TestPostgresRedisHistoryResetClearsRuntimeSnapshot(t *testing.T) {
	redisURL := os.Getenv("MEMOH_TEST_REDIS_URL")
	if redisURL == "" {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("MEMOH_TEST_REDIS_URL required")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL")
	}
	for _, mode := range []wsStepHistoryModel{wsStepHistorySuccess, wsStepHistoryAuthFailure} {
		for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
			t.Run(string(mode)+"/"+string(scope), func(t *testing.T) {
				prefix := "history-reset-test:" + uuid.NewString() + ":"
				backend, err := sessionruntime.NewRedisBackend(context.Background(), sessionruntime.RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
				if err != nil {
					t.Fatalf("NewRedisBackend() error = %v", err)
				}
				h := newWSStepHistoryHarnessOn(t, mode, backend)
				// Redis outlives the process: a new runtime on the same keys
				// must agree with the one that cleared them.
				reopen := func(t *testing.T) *sessionruntime.Manager {
					t.Helper()
					persisted, err := sessionruntime.NewRedisBackend(context.Background(), sessionruntime.RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
					if err != nil {
						t.Fatalf("NewRedisBackend() error = %v", err)
					}
					queries := postgresstore.NewQueriesWithPool(h.pool, dbsqlc.New(h.pool))
					manager := sessionruntime.NewManager(persisted, sessionruntime.Options{
						OwnerID: "owner-reopened-" + uuid.NewString(), StateTTL: time.Minute, OwnerLeaseTTL: time.Minute,
						Ledger: ledger.NewPostgres(dbsqlc.New(h.pool), h.pool), Fence: runtimefence.NewActivator(queries),
					})
					t.Cleanup(func() { _ = manager.Close() })
					return manager
				}
				assertHistoryResetClearsRuntimeSnapshot(t, h, scope, reopen)
			})
		}
	}
}

func assertHistoryResetClearsRuntimeSnapshot(t *testing.T, h wsStepHistoryHarness, scope historyResetScope, reopen func(*testing.T) *sessionruntime.Manager) {
	t.Helper()
	ctx := context.Background()
	view := h.watch(t)

	h.run(t, nil)
	before := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
	if before == nil {
		t.Fatal("finished run is missing from the live snapshot before the clear")
	}

	view.drain()
	// The client reloads while the reset is under way, before the history is
	// gone; what it reads then must not be what it is left with.
	h.clearHistory(t, scope, view.awaitReload)

	if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
		t.Errorf("live snapshot after clearing history still holds %s", describeRun(got))
	}
	view.awaitCleared(before.RunID)
	if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got != nil {
		t.Errorf("ledger fallback after clearing history reports %s", describeRun(got))
	}
	if reopen != nil {
		if got := mustSnapshot(t, reopen(t), h.botID, h.sessionID).CurrentRunView; got != nil {
			t.Errorf("reopened live backend after clearing history holds %s", describeRun(got))
		}
	}
	if rows, err := h.messages.ListBySession(ctx, h.sessionID); err != nil || len(rows) != 0 {
		t.Fatalf("history after the clear = (%d rows, %v), want empty", len(rows), err)
	}

	// The session keeps working: the next turn is reported live and by the
	// ledger fallback alike.
	next := h.run(t, nil)
	if len(next.history) == 0 {
		t.Fatal("turn after the clear wrote no history")
	}
	after := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
	if after == nil || after.RunID == before.RunID {
		t.Fatalf("live run after the next turn = %s, want a new run", describeRun(after))
	}
	view.awaitRun(after.RunID)
	if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != after.RunID {
		t.Fatalf("ledger fallback after the next turn = %s, want %s", describeRun(got), describeRun(after))
	}
	if reopen != nil {
		if got := mustSnapshot(t, reopen(t), h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != after.RunID {
			t.Fatalf("reopened live backend after the next turn = %s, want %s", describeRun(got), describeRun(after))
		}
	}
}

// failingUpdateBackend is the memory backend with writes, and a clock, that
// can be made to fail the way a Redis outage fails them.
type failingUpdateBackend struct {
	*sessionruntime.MemoryBackend
	fail, failClock atomic.Bool
}

func (b *failingUpdateBackend) Update(ctx context.Context, key sessionruntime.Key, update sessionruntime.SnapshotUpdate) (sessionruntime.Snapshot, bool, error) {
	if b.fail.Load() {
		return sessionruntime.Snapshot{}, false, errors.New("runtime backend write failed")
	}
	return b.MemoryBackend.Update(ctx, key, update)
}

func (b *failingUpdateBackend) Now(ctx context.Context) (time.Time, error) {
	if b.failClock.Load() {
		return time.Time{}, errors.New("runtime backend unreachable")
	}
	return b.MemoryBackend.Now(ctx)
}

// A reset that cannot clear the live projection must not let the history go:
// the clear fails before anything is deleted, the reset lease is given back,
// and the next clear succeeds.
func TestPostgresHistoryResetFailsClosedWhenSnapshotCannotBeCleared(t *testing.T) {
	for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
		t.Run(string(scope), func(t *testing.T) {
			backend := &failingUpdateBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
			h := newWSStepHistoryHarnessOn(t, wsStepHistorySuccess, backend)
			h.run(t, nil)
			ctx := context.Background()
			before := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
			if before == nil {
				t.Fatal("finished run is missing from the live snapshot")
			}

			backend.fail.Store(true)
			var err error
			if scope == historyResetBot {
				_, _, err = h.manager.BeginBotHistoryReset(ctx, h.botID)
			} else {
				_, _, err = h.manager.BeginSessionHistoryReset(ctx, h.botID, h.sessionID)
			}
			backend.fail.Store(false)
			if err == nil {
				t.Fatal("history reset began although the live projection could not be cleared")
			}
			if rows, err := h.messages.ListBySession(ctx, h.sessionID); err != nil || len(rows) == 0 {
				t.Fatalf("history after the failed reset = (%d rows, %v), want it kept", len(rows), err)
			}
			if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != before.RunID {
				t.Fatalf("live run after the failed reset = %s, want %s", describeRun(got), describeRun(before))
			}

			h.clearHistory(t, scope, nil)
			if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
				t.Fatalf("live snapshot after the retried clear still holds %s", describeRun(got))
			}
		})
	}
}

// Clearing history while a run streams stops the run first. The run's own
// terminal write still lands, and nothing of it is left once the clear
// returns.
func TestPostgresHistoryResetDuringActiveRun(t *testing.T) {
	for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
		t.Run(string(scope), func(t *testing.T) {
			h := newWSStepHistoryHarness(t, wsStepHistoryBlock)
			// The ACP pool installs the owner-local closer in production; this
			// session has no ACP runtime to close.
			h.manager.SetHistoryResetHandler(func(context.Context, sessionruntime.ResetScope) error { return nil })
			resetDone := make(chan error, 1)
			got := h.run(t, func(ctx context.Context, _ string) {
				resetDone <- h.clearHistoryErr(ctx, scope, nil)
			})
			select {
			case err := <-resetDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("history reset did not finish")
			}
			if got.sessionRun[0] != "aborted" {
				t.Fatalf("session_runs state = %q, want aborted", got.sessionRun[0])
			}
			if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
				t.Errorf("live snapshot after clearing an active run holds %s", describeRun(got))
			}
			if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got != nil {
				t.Errorf("ledger fallback after clearing an active run reports %s", describeRun(got))
			}
			if rows, err := h.messages.ListBySession(context.Background(), h.sessionID); err != nil || len(rows) != 0 {
				t.Fatalf("history after clearing an active run = (%d rows, %v), want empty", len(rows), err)
			}
		})
	}
}

// A clear that cannot reach the runtime backend while it stops a streaming
// run fails as that backend's failure, not as a busy conversation. The run
// streams on and the lease is given back, so the retried clear stops the run
// and goes through.
func TestPostgresHistoryResetDrainBackendFailure(t *testing.T) {
	for _, scope := range []historyResetScope{historyResetSession, historyResetBot} {
		t.Run(string(scope), func(t *testing.T) {
			backend := &failingUpdateBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
			h := newWSStepHistoryHarnessOn(t, wsStepHistoryBlock, backend)
			h.manager.SetHistoryResetHandler(func(context.Context, sessionruntime.ResetScope) error { return nil })
			var failed error
			var during *sessionruntime.CurrentRunView
			retried := make(chan error, 1)
			got := h.run(t, func(ctx context.Context, _ string) {
				backend.failClock.Store(true)
				failed = h.clearHistoryErr(ctx, scope, nil)
				backend.failClock.Store(false)
				if snapshot, err := h.manager.Snapshot(ctx, h.botID, h.sessionID); err == nil {
					during = snapshot.CurrentRunView
				}
				retried <- h.clearHistoryErr(ctx, scope, nil)
			})
			select {
			case err := <-retried:
				if err != nil {
					t.Fatalf("retried clear = %v", err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("retried clear did not finish")
			}
			if failed == nil || sessionruntime.IsHistoryResetBusy(failed) || errs.FaultOf(failed) != apperror.FaultDependency {
				t.Fatalf("clear with the backend down = %v (fault %q), want a dependency failure", failed, errs.FaultOf(failed))
			}
			if during == nil || during.Status != sessionruntime.RunStatusRunning {
				t.Fatalf("run after the failed clear = %s, want it still streaming", describeRun(during))
			}
			if got.sessionRun[0] != "aborted" {
				t.Fatalf("session_runs state = %q, want aborted by the retried clear", got.sessionRun[0])
			}
			if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
				t.Errorf("live snapshot after the retried clear holds %s", describeRun(got))
			}
			if rows, err := h.messages.ListBySession(context.Background(), h.sessionID); err != nil || len(rows) != 0 {
				t.Fatalf("history after the retried clear = (%d rows, %v), want empty", len(rows), err)
			}
		})
	}
}

// A run that never got a fencing token cannot be ordered against a clear by
// its token; that takes a process dying between admission and claim, and the
// reaper marking the admission lost. The clear retires such a run with the
// history it belonged to, while an admission made after the clear is still
// reported.
func TestPostgresHistoryResetRetiresOrphanedAdmission(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistorySuccess)
	h.run(t, nil)
	ctx := context.Background()
	runs := ledger.NewPostgres(dbsqlc.New(h.pool), h.pool)
	admit := func(input string) ledger.Run {
		t.Helper()
		run, _, err := runs.Admit(ctx, ledger.AdmitParams{
			RunID: uuid.NewString(), BotID: h.botID, SessionID: h.sessionID,
			InvocationID: uuid.NewString(), TurnID: uuid.NewString(),
			Input: []byte(`{"kind":"message","text":"` + input + `"}`), InputFingerprint: input,
		})
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		return run
	}
	const input = "orphaned admission input that was cleared"
	orphan := admit(input)
	// The reaper's repair of an admission whose process died before claiming it.
	if _, applied, err := runs.Finalize(ctx, ledger.FinalizeParams{
		RunID: orphan.RunID, State: ledger.StateLost, ErrorCode: "runtime_admission_orphaned",
	}); err != nil || !applied {
		t.Fatalf("mark orphaned admission lost = (%v, %v)", applied, err)
	}

	h.clearHistory(t, historyResetSession, nil)

	for name, manager := range map[string]*sessionruntime.Manager{"live": h.manager, "restarted": h.restarted(t)} {
		snapshot := mustSnapshot(t, manager, h.botID, h.sessionID)
		if run := snapshot.CurrentRunView; run != nil {
			t.Errorf("%s snapshot after the clear reports %s, want no run", name, describeRun(run))
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		for _, cleared := range []string{input, directLifecyclePrompt, wsStepHistoryPartialText} {
			if strings.Contains(string(raw), cleared) {
				t.Errorf("%s snapshot after the clear carries cleared content %q: %s", name, cleared, raw)
			}
		}
	}

	pending := admit("admission after the clear")
	if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != pending.RunID {
		t.Fatalf("ledger fallback after a new admission = %s, want %s", describeRun(got), pending.RunID)
	}
}

// A subscriber that read the cleared run from the ledger while the reset was
// under way catches up on its own within the reconcile interval, even when
// the reset never gets to restart the projections (its release pass skipped,
// timed out, or its process died after the deletion committed), and even when
// the session went with its history, as an overwrite import or an ACP session
// deletion takes it, so the client cannot subscribe to it again.
func TestPostgresHistoryResetSubscriberHealsWithoutReleasePass(t *testing.T) {
	for _, deleteSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "history cleared", true: "session deleted"}[deleteSession], func(t *testing.T) {
			h := newWSStepHistoryHarness(t, wsStepHistoryAuthFailure)
			ctx := context.Background()
			view := h.watch(t)
			h.run(t, nil)
			before := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
			if before == nil {
				t.Fatal("failed run is missing from the live snapshot")
			}
			view.drain()
			epoch := view.epoch

			resetCtx, release, err := h.manager.BeginSessionHistoryReset(ctx, h.botID, h.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if !view.follow(5*time.Second, func() bool {
				return !view.awaiting && view.epoch != epoch && view.run != nil && view.run.RunID == before.RunID
			}) {
				t.Fatalf("client did not reload the run from the ledger when the reset began; it shows %s", describeRun(view.run))
			}
			if err := h.messages.DeleteBySession(resetCtx, h.sessionID); err != nil {
				t.Fatal(err)
			}
			if deleteSession {
				if err := dbsqlc.New(h.pool).SoftDeleteSession(resetCtx, dbpkg.ParseUUIDOrEmpty(h.sessionID)); err != nil {
					t.Fatal(err)
				}
			}
			// No release: the client must be brought to the cleared ledger
			// without it.
			view.awaitCleared(before.RunID)
			if deleteSession {
				return
			}

			release()
			h.run(t, nil)
			next := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
			if next == nil || next.RunID == before.RunID {
				t.Fatalf("live run after the next turn = %s, want a new run", describeRun(next))
			}
			view.awaitRun(next.RunID)
		})
	}
}

// A bot-wide clear reaches every session of the bot, not only the first.
func TestPostgresHistoryResetClearsEveryBotSession(t *testing.T) {
	first := newWSStepHistoryHarness(t, wsStepHistorySuccess)
	ctx := context.Background()
	second := first
	second.sessionID = uuid.NewString()
	if _, err := first.pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'telegram', 'model')
	`, second.sessionID, first.botID); err != nil {
		t.Fatalf("create second session: %v", err)
	}
	sessions := []wsStepHistoryHarness{first, second}
	views := make([]*webRuntimeView, len(sessions))
	runs := make([]string, len(sessions))
	for i, h := range sessions {
		views[i] = h.watch(t)
		h.run(t, nil)
		run := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
		if run == nil {
			t.Fatalf("session %d: finished run is missing from the live snapshot", i)
		}
		runs[i] = run.RunID
	}
	for _, view := range views {
		view.drain()
	}

	first.clearHistory(t, historyResetBot, nil)

	restarted := first.restarted(t)
	for i, h := range sessions {
		if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
			t.Errorf("session %d: live snapshot after the bot clear holds %s", i, describeRun(got))
		}
		if got := mustSnapshot(t, restarted, h.botID, h.sessionID).CurrentRunView; got != nil {
			t.Errorf("session %d: ledger fallback after the bot clear reports %s", i, describeRun(got))
		}
		views[i].awaitCleared(runs[i])
	}
}

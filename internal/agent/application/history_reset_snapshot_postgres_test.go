package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
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

// drainSubscription consumes everything a subscriber has been sent so far.
func drainSubscription(sub sessionruntime.Subscription) {
	for {
		select {
		case <-sub.C:
		case <-time.After(200 * time.Millisecond):
			return
		}
	}
}

// awaitSnapshotEvent lets a reload the reset triggered reach the subscriber
// before the test goes on; a reset that does not trigger one leaves nothing
// to wait for.
func awaitSnapshotEvent(t *testing.T, sub sessionruntime.Subscription) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case event, ok := <-sub.C:
			if !ok {
				t.Fatal("subscription closed during the reset")
			}
			if event.Type == sessionruntime.EventRuntimeSnapshot {
				return
			}
		case <-deadline:
			return
		}
	}
}

// awaitClearedSubscription reads a subscriber's stream until it carries a
// snapshot without a run, then checks nothing that follows brings one back.
func awaitClearedSubscription(t *testing.T, sub sessionruntime.Subscription, staleRunID string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	cleared := false
	for !cleared {
		select {
		case event, ok := <-sub.C:
			if !ok {
				t.Fatal("subscription closed before the cleared snapshot arrived")
			}
			if event.Type == sessionruntime.EventRuntimeSnapshot && event.Snapshot != nil && event.Snapshot.CurrentRunView == nil {
				cleared = true
			}
		case <-deadline:
			t.Fatal("subscriber never received a snapshot without the cleared run")
		}
	}
	settle := time.After(300 * time.Millisecond)
	for {
		select {
		case event, ok := <-sub.C:
			if !ok {
				return
			}
			if event.Snapshot != nil && event.Snapshot.CurrentRunView != nil && event.Snapshot.CurrentRunView.RunID == staleRunID {
				t.Fatalf("cleared run came back to the subscriber: %s", describeRun(event.Snapshot.CurrentRunView))
			}
			if event.Delta != nil && event.RunID == staleRunID {
				t.Fatalf("cleared run streamed a delta after the reset: %+v", event)
			}
		case <-settle:
			return
		}
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
	sub, err := h.manager.Subscribe(ctx, h.botID, h.sessionID)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer sub.Close()

	h.run(t, nil)
	before := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView
	if before == nil {
		t.Fatal("finished run is missing from the live snapshot before the clear")
	}

	drainSubscription(sub)
	// The subscriber reloads while the reset is under way, before the history
	// is gone; what it reads then must not be what it is left with.
	h.clearHistory(t, scope, func() { awaitSnapshotEvent(t, sub) })

	if got := mustSnapshot(t, h.manager, h.botID, h.sessionID).CurrentRunView; got != nil {
		t.Errorf("live snapshot after clearing history still holds %s", describeRun(got))
	}
	awaitClearedSubscription(t, sub, before.RunID)
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
	if got := mustSnapshot(t, h.restarted(t), h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != after.RunID {
		t.Fatalf("ledger fallback after the next turn = %s, want %s", describeRun(got), describeRun(after))
	}
	if reopen != nil {
		if got := mustSnapshot(t, reopen(t), h.botID, h.sessionID).CurrentRunView; got == nil || got.RunID != after.RunID {
			t.Fatalf("reopened live backend after the next turn = %s, want %s", describeRun(got), describeRun(after))
		}
	}
}

// failingUpdateBackend is the memory backend with writes that can be made to
// fail, the way a Redis outage fails them.
type failingUpdateBackend struct {
	*sessionruntime.MemoryBackend
	fail atomic.Bool
}

func (b *failingUpdateBackend) Update(ctx context.Context, key sessionruntime.Key, update sessionruntime.SnapshotUpdate) (sessionruntime.Snapshot, bool, error) {
	if b.fail.Load() {
		return sessionruntime.Snapshot{}, false, errors.New("runtime backend write failed")
	}
	return b.MemoryBackend.Update(ctx, key, update)
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

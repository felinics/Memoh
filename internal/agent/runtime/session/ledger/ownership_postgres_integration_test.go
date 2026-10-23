package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// A process that dies without graceful shutdown leaves its active run in the
// ledger. The memory backend's generation is per process, so the next process
// finds the run under a generation it does not own and the reaper's stale
// generation sweep is what declares it lost.
func TestPostgresMemoryRestartReapsPreviousProcessRun(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	runs := ledger.NewPostgres(dbsqlc.New(pool), pool)

	// The previous process is abandoned without CloseContext and never starts
	// a reaper, so nothing in it marks its own run.
	previous := newMemoryRestartManager(store, runs, "previous-process")
	first, err := previous.Admit(ctx, restartAdmitInput(botID, sessionID, "before-restart"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Started {
		t.Fatalf("first run not started: %+v", first)
	}

	next := newMemoryRestartManager(store, runs, "next-process")
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.CloseContext(context.WithoutCancel(ctx)) })

	run := waitForRunState(t, ctx, runs, first.RunID, ledger.StateLost)
	if run.ErrorCode != "runtime_live_backend_lost" {
		t.Fatalf("error code = %q, want runtime_live_backend_lost", run.ErrorCode)
	}

	successor, err := next.Admit(ctx, restartAdmitInput(botID, sessionID, "after-restart"))
	if err != nil {
		t.Fatalf("admit after restart: %v", err)
	}
	if !successor.Started {
		t.Fatalf("successor not started after previous run was reaped: %+v", successor)
	}
}

// Once the reaper has declared a run lost, the owner's token must no longer
// pass the session persistence fence. Before a successor activates, nothing
// else would stop the owner's late history writes.
func TestPostgresLostRunFencesOwnerWrites(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	runs := ledger.NewPostgres(dbsqlc.New(pool), pool)

	previous := newMemoryRestartManager(store, runs, "previous-process")
	first, err := previous.Admit(ctx, restartAdmitInput(botID, sessionID, "owner-writes"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Started {
		t.Fatalf("first run not started: %+v", first)
	}
	ownerCtx := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: first.Handle.FencingToken})
	if err := runtimefence.InTransaction(ownerCtx, store, botID, sessionID, func(dbstore.Queries) error { return nil }); err != nil {
		t.Fatalf("owner write before lost = %v", err)
	}

	next := newMemoryRestartManager(store, runs, "next-process")
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.CloseContext(context.WithoutCancel(ctx)) })
	waitForRunState(t, ctx, runs, first.RunID, ledger.StateLost)

	err = runtimefence.InTransaction(ownerCtx, store, botID, sessionID, func(dbstore.Queries) error { return nil })
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("owner write after lost = %v, want ErrStale", err)
	}
}

func newMemoryRestartManager(store dbstore.Queries, runs ledger.Store, ownerID string) *sessionruntime.Manager {
	return sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       ownerID,
		OwnerLeaseTTL: time.Second,
		Ledger:        runs,
		Fence:         runtimefence.NewActivator(store),
	})
}

func restartAdmitInput(botID, sessionID, invocationID string) sessionruntime.AdmitInput {
	return sessionruntime.AdmitInput{
		BotID:        botID,
		SessionID:    sessionID,
		InvocationID: invocationID,
		Payload:      []byte(`{"query":"work"}`),
		Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{}, nil
		}},
	}
}

func waitForRunState(t *testing.T, ctx context.Context, runs ledger.Store, runID string, want ledger.State) ledger.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		run, err := runs.Get(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == want {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s state = %s, want %s", runID, run.State, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The reaper and an owner finishing normally race on the same row. A run that
// completes must leave the session fence where it is, so the owner's own
// history writes are never shut out by its own completion.
func TestPostgresCompletedRunDoesNotAdvanceSessionFence(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	runs := ledger.NewPostgres(dbsqlc.New(pool), pool)

	owner := newMemoryRestartManager(store, runs, "owner")
	first, err := owner.Admit(ctx, restartAdmitInput(botID, sessionID, "completes"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Started {
		t.Fatalf("run not started: %+v", first)
	}
	fenceBefore := sessionFenceToken(t, ctx, pool, botID, sessionID)
	if _, applied, err := runs.Finalize(ctx, ledger.FinalizeParams{RunID: first.RunID, FencingToken: first.Handle.FencingToken, State: ledger.StateCompleted}); err != nil || !applied {
		t.Fatalf("complete = (%v, %v)", applied, err)
	}
	if got := sessionFenceToken(t, ctx, pool, botID, sessionID); got != fenceBefore {
		t.Fatalf("fence moved on normal completion: before=%d after=%d", fenceBefore, got)
	}
}

func sessionFenceToken(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID, sessionID string) int64 {
	t.Helper()
	var token int64
	if err := pool.QueryRow(ctx, "SELECT runtime_fencing_token FROM bot_sessions WHERE id=$1 AND bot_id=$2", sessionID, botID).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

// The reaper may replay a lost decision for a run that was already reaped and
// deleted. That is not an error: the transition is simply not applied.
func TestPostgresLostFinalizeOfMissingRunIsNotApplied(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	runs := ledger.NewPostgres(dbsqlc.New(pool), pool)
	_, applied, err := runs.Finalize(ctx, ledger.FinalizeParams{RunID: "00000000-0000-0000-0000-000000000001", FencingToken: 1, State: ledger.StateLost})
	if err != nil || applied {
		t.Fatalf("lost finalize of missing run = (%v, %v), want (false, nil)", applied, err)
	}
}

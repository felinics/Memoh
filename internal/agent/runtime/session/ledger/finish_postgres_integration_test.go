package ledger_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPostgresLedgerPreparedFinishSurvivesCompetingFinalization(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := ledger.NewPostgres(dbsqlc.New(pool), pool)
	runID, token := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)

	prepared, applied, err := store.PrepareFinish(ctx, ledger.PrepareFinishParams{
		RunID: runID, FencingToken: token, State: ledger.StateCompleted,
	})
	if err != nil || !applied {
		t.Fatalf("prepare completed finish = (%#v, %v, %v)", prepared, applied, err)
	}
	if prepared.State != ledger.StateFinishing || prepared.ProposedState != ledger.StateCompleted || prepared.FinishProposedAt.IsZero() {
		t.Fatalf("prepared run = %#v", prepared)
	}

	// A retry or racing caller cannot rewrite the accepted result.
	replayed, applied, err := store.PrepareFinish(ctx, ledger.PrepareFinishParams{
		RunID: runID, FencingToken: token, State: ledger.StateFailed,
		ErrorCode: "late_failure", ErrorMessage: "late failure",
	})
	if err != nil || !applied {
		t.Fatalf("replay finish proposal = (%#v, %v, %v)", replayed, applied, err)
	}
	if replayed.ProposedState != ledger.StateCompleted || replayed.ProposedErrorCode != "" || replayed.ProposedErrorMessage != "" {
		t.Fatalf("replayed proposal changed first outcome: %#v", replayed)
	}

	// Reapers and graceful shutdown pass lost when the owner disappears. Once
	// finishing is durable, Finalize must resolve that request to the proposal.
	finalized, applied, err := store.Finalize(ctx, ledger.FinalizeParams{
		RunID: runID, FencingToken: token, State: ledger.StateLost,
		ErrorCode: "runtime_owner_lease_expired", ErrorMessage: "runtime owner lease expired",
	})
	if err != nil || !applied {
		t.Fatalf("finalize prepared run = (%#v, %v, %v)", finalized, applied, err)
	}
	if finalized.State != ledger.StateCompleted || finalized.ErrorCode != "" || finalized.ErrorMessage != "" {
		t.Fatalf("prepared outcome was overwritten: %#v", finalized)
	}
}

func TestPostgresLedgerAbortIntentWinsBeforeFinishProposal(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := ledger.NewPostgres(dbsqlc.New(pool), pool)
	runID, token := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)

	if run, applied, err := store.RequestAbort(ctx, runID); err != nil || !applied || run.AbortRequestedAt.IsZero() {
		t.Fatalf("request abort = (%#v, %v, %v)", run, applied, err)
	}
	prepared, applied, err := store.PrepareFinish(ctx, ledger.PrepareFinishParams{
		RunID: runID, FencingToken: token, State: ledger.StateCompleted,
	})
	if err != nil || !applied {
		t.Fatalf("prepare after abort = (%#v, %v, %v)", prepared, applied, err)
	}
	if prepared.ProposedState != ledger.StateAborted {
		t.Fatalf("proposal after abort = %q, want aborted", prepared.ProposedState)
	}
	finalized, applied, err := store.Finalize(ctx, ledger.FinalizeParams{
		RunID: runID, FencingToken: token, State: ledger.StateCompleted,
	})
	if err != nil || !applied || finalized.State != ledger.StateAborted {
		t.Fatalf("finalize after abort = (%#v, %v, %v)", finalized, applied, err)
	}
}

func createClaimedLedgerRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	botID, sessionID string,
) (string, int64) {
	t.Helper()
	token, err := dbsqlc.New(pool).NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate fencing token: %v", err)
	}
	runID := uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, owner_since,
			fencing_token, live_generation
		) VALUES ($1, $2, $3, $4, $5, 1, 'running', '{}'::jsonb, $6, $7, now(), $8, $9)
	`, runID, botID, sessionID, uuid.NewString(), uuid.NewString(), "ledger-finish-"+runID, "owner-ledger-finish", token, "generation-ledger-finish"); err != nil {
		t.Fatalf("create claimed run: %v", err)
	}
	return runID, token
}

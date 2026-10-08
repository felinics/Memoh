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
		ErrorCode: "late_failure",
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
		ErrorCode: "runtime_owner_lease_expired",
	})
	if err != nil || !applied {
		t.Fatalf("finalize prepared run = (%#v, %v, %v)", finalized, applied, err)
	}
	if finalized.State != ledger.StateCompleted || finalized.ErrorCode != "" || finalized.ErrorMessage != "" {
		t.Fatalf("prepared outcome was overwritten: %#v", finalized)
	}
}

// The ledger records a failure by its code and writes no error text, in the
// proposal or in the terminal row. A proposal an earlier release wrote a
// message into keeps it through finalize, which stays first-write-wins.
func TestPostgresLedgerWritesNoErrorText(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	store := ledger.NewPostgres(dbsqlc.New(pool), pool)

	proposedID, proposedToken := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)
	if _, applied, err := store.PrepareFinish(ctx, ledger.PrepareFinishParams{
		RunID: proposedID, FencingToken: proposedToken, State: ledger.StateFailed, ErrorCode: "agent.provider_overloaded",
	}); err != nil || !applied {
		t.Fatalf("prepare failed finish = (%v, %v)", applied, err)
	}
	if _, applied, err := store.Finalize(ctx, ledger.FinalizeParams{
		RunID: proposedID, FencingToken: proposedToken, State: ledger.StateLost, ErrorCode: "runtime_owner_lease_expired",
	}); err != nil || !applied {
		t.Fatalf("finalize proposed run = (%v, %v)", applied, err)
	}
	assertLedgerErrorColumns(t, ctx, pool, proposedID, "failed", "agent.provider_overloaded", nil, nil)

	directID, directToken := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)
	if _, applied, err := store.Finalize(ctx, ledger.FinalizeParams{
		RunID: directID, FencingToken: directToken, State: ledger.StateFailed, ErrorCode: "runtime_fence_activation_failed",
	}); err != nil || !applied {
		t.Fatalf("finalize run = (%v, %v)", applied, err)
	}
	assertLedgerErrorColumns(t, ctx, pool, directID, "failed", "runtime_fence_activation_failed", nil, nil)

	legacyID, legacyToken := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)
	const legacyText = "written by an earlier release"
	if _, err := pool.Exec(ctx, `
		UPDATE session_runs
		SET state = 'finishing', proposed_terminal_state = 'failed', proposed_error_code = 'runtime_run_failed',
			proposed_error_message = $2, finish_proposed_at = now()
		WHERE run_id = $1
	`, legacyID, legacyText); err != nil {
		t.Fatalf("seed legacy proposal: %v", err)
	}
	if _, applied, err := store.Finalize(ctx, ledger.FinalizeParams{
		RunID: legacyID, FencingToken: legacyToken, State: ledger.StateLost, ErrorCode: "runtime_owner_lease_expired",
	}); err != nil || !applied {
		t.Fatalf("finalize legacy proposal = (%v, %v)", applied, err)
	}
	text := legacyText
	assertLedgerErrorColumns(t, ctx, pool, legacyID, "failed", "runtime_run_failed", &text, &text)
}

func assertLedgerErrorColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID, wantState, wantCode string, wantProposedMessage, wantMessage *string) {
	t.Helper()
	var (
		state, code              string
		proposedMessage, message *string
	)
	if err := pool.QueryRow(ctx,
		"SELECT state, error_code, proposed_error_message, error_message FROM session_runs WHERE run_id = $1", runID,
	).Scan(&state, &code, &proposedMessage, &message); err != nil {
		t.Fatalf("load run %s: %v", runID, err)
	}
	if state != wantState || code != wantCode {
		t.Fatalf("run %s = state:%q code:%q, want state:%q code:%q", runID, state, code, wantState, wantCode)
	}
	if !sameText(proposedMessage, wantProposedMessage) || !sameText(message, wantMessage) {
		t.Fatalf("run %s messages = proposed:%v final:%v, want proposed:%v final:%v",
			runID, textValue(proposedMessage), textValue(message), textValue(wantProposedMessage), textValue(wantMessage))
	}
}

func sameText(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func textValue(text *string) string {
	if text == nil {
		return "NULL"
	}
	return *text
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

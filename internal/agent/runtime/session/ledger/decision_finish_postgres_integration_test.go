package ledger_test

import (
	"testing"

	"github.com/google/uuid"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Finishing must see both lost terminal notifications and expired pending
// requests. Routing queries intentionally expose a narrower, respondable set.
func TestPostgresFinishReadsAllRunDecisions(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	runID, otherRunID := uuid.NewString(), uuid.NewString()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for i, status := range []string{"pending", "rejected", "expired"} {
		if _, err := conn.Exec(ctx, `INSERT INTO tool_approval_requests
   (bot_id,session_id,run_id,tool_call_id,tool_name,operation,tool_input,short_id,status)
   VALUES ($1,$2,$3,$4,'exec','exec','{}',$5,$6)`, botID, sessionID, runID, uuid.NewString(), i+1, status); err != nil {
			t.Fatal(err)
		}
	}
	for i, status := range []string{"pending", "submitted", "expired"} {
		if _, err := conn.Exec(ctx, `INSERT INTO user_input_requests
   (bot_id,session_id,run_id,tool_call_id,input_json,short_id,status,expires_at)
   VALUES ($1,$2,$3,$4,'{}',$5,$6,now()-interval '1 minute')`, botID, sessionID, runID, uuid.NewString(), i+1, status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO user_input_requests (bot_id,session_id,run_id,tool_call_id,input_json,short_id)
  VALUES ($1,$2,$3,$4,'{}',4)`, botID, sessionID, otherRunID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	queries := dbsqlc.New(conn)
	id := dbpkg.ParseUUIDOrEmpty(runID)
	approvals, err := queries.ListToolApprovalsByRun(ctx, id)
	if err != nil || len(approvals) != 3 {
		t.Fatalf("finish approvals=%d err=%v", len(approvals), err)
	}
	inputs, err := queries.ListUserInputsByRun(ctx, id)
	if err != nil || len(inputs) != 3 {
		t.Fatalf("finish inputs=%d err=%v", len(inputs), err)
	}
	pending, err := queries.ListPendingUserInputsByRun(ctx, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("routing semantics changed: %d err=%v", len(pending), err)
	}
	// An unrelated team must not gain access by knowing the globally unique run ID.
	if _, err := conn.Exec(ctx, `SELECT set_config('memoh.team_id',$1,false)`, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `RESET memoh.team_id`) }()
	approvals, err = queries.ListToolApprovalsByRun(ctx, id)
	if err != nil || len(approvals) != 0 {
		t.Fatalf("approval team isolation: %d err=%v", len(approvals), err)
	}
	inputs, err = queries.ListUserInputsByRun(ctx, id)
	if err != nil || len(inputs) != 0 {
		t.Fatalf("input team isolation: %d err=%v", len(inputs), err)
	}
}

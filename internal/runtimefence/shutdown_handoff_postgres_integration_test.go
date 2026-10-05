package runtimefence_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresWaitingDecisionHandoffIsAtomic(t *testing.T) {
	for _, scenario := range []string{"pending", "answered", "expired", "aborted", "newer_fence", "deleted", "bot_reset", "session_reset", "wrong_team"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool := openRuntimeFencePostgres(t, ctx)
			botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
			store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
			fence := nextRuntimeFence(t, ctx, dbsqlc.New(pool), botID, sessionID)
			createRuntimeFenceRun(t, ctx, pool, fence)
			if err := runtimefence.Activate(ctx, store, fence); err != nil {
				t.Fatal(err)
			}
			inputs := userinput.NewService(nil, store)
			oldCtx := runtimefence.WithContext(ctx, fence)
			first := createRuntimeFenceUserInput(t, oldCtx, inputs, botID, sessionID, "first", nil)
			second := createRuntimeFenceUserInput(t, oldCtx, inputs, botID, sessionID, "second", nil)
			var runID string
			if err := pool.QueryRow(ctx, `UPDATE session_runs SET state='waiting_decision' WHERE session_id=$1 RETURNING run_id`, sessionID).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			// Put the conflict on the SECOND decision, so a failure proves the first
			// decision's already executed token update rolls back with the transaction.
			switch scenario {
			case "answered":
				_, _ = pool.Exec(ctx, `UPDATE user_input_requests SET status='submitted',result_json='{"answer":"accepted"}' WHERE id=$1`, second.ID)
			case "expired":
				_, _ = pool.Exec(ctx, `UPDATE user_input_requests SET expires_at=now()-interval '1 minute' WHERE id=$1`, second.ID)
			case "aborted":
				_, _ = pool.Exec(ctx, `UPDATE session_runs SET abort_requested_at=now() WHERE run_id=$1`, runID)
			case "newer_fence":
				successor := nextRuntimeFence(t, ctx, dbsqlc.New(pool), botID, sessionID)
				_, _ = pool.Exec(ctx, `UPDATE bot_sessions SET runtime_fencing_token=$2 WHERE id=$1`, sessionID, successor.Token)
			case "bot_reset":
				grantBotResetLease(t, ctx, pool, botID)
			case "session_reset":
				grantSessionResetLease(t, ctx, pool, botID, sessionID)
			case "deleted":
				_, _ = pool.Exec(ctx, `UPDATE bot_sessions SET deleted_at=now() WHERE id=$1`, sessionID)
			case "wrong_team":
				cfg := pool.Config().Copy()
				cfg.MaxConns = 1
				cfg.AfterConnect = nil
				cfg.ConnConfig.RuntimeParams["memoh.team_id"] = uuid.NewString()
				scoped, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer scoped.Close()
				store = postgresstore.NewQueriesWithPool(scoped, dbsqlc.New(scoped))
			}
			next := nextRuntimeFence(t, ctx, dbsqlc.New(pool), botID, sessionID)
			err := runtimefence.NewActivator(store).ReclaimWaitingDecision(ctx, botID, sessionID, runID, "new-owner", "live", fence.Token, next.Token, []runtimefence.PreservedDecision{{Kind: runtimefence.DecisionUserInput, ID: first.ID}, {Kind: runtimefence.DecisionUserInput, ID: second.ID}})
			if scenario == "pending" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("conflicting handoff succeeded")
			}
			wantToken := fence.Token
			if scenario == "pending" {
				wantToken = next.Token
			}
			for _, id := range []string{first.ID, second.ID} {
				var token int64
				if err := pool.QueryRow(ctx, `SELECT runtime_fencing_token FROM user_input_requests WHERE id=$1`, id).Scan(&token); err != nil {
					t.Fatal(err)
				}
				if token != wantToken {
					t.Fatalf("decision %s token=%d want=%d", id, token, wantToken)
				}
			}
			var state string
			var token int64
			if err := pool.QueryRow(ctx, `SELECT state,fencing_token FROM session_runs WHERE run_id=$1`, runID).Scan(&state, &token); err != nil {
				t.Fatal(err)
			}
			if state != "waiting_decision" || token != wantToken {
				t.Fatalf("run changed on failed handoff: %s %d", state, token)
			}
			if scenario == "pending" {
				if _, err := inputs.CreatePending(oldCtx, userinput.CreatePendingInput{BotID: botID, SessionID: sessionID, ToolCallID: "stale", Input: map[string]any{"questions": []any{map[string]any{"text": "late", "kind": "text"}}}}); !errors.Is(err, runtimefence.ErrStale) {
					t.Fatalf("old writer result=%v", err)
				}
			}
		})
	}
}

func TestPostgresWaitingDecisionHandoffHasOneWinner(t *testing.T) {
	ctx := t.Context()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	q := dbsqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, q)
	old := nextRuntimeFence(t, ctx, q, botID, sessionID)
	createRuntimeFenceRun(t, ctx, pool, old)
	if err := runtimefence.Activate(ctx, store, old); err != nil {
		t.Fatal(err)
	}
	input := createRuntimeFenceUserInput(t, runtimefence.WithContext(ctx, old), userinput.NewService(nil, store), botID, sessionID, "pending", nil)
	var runID string
	if err := pool.QueryRow(ctx, `UPDATE session_runs SET state='waiting_decision' WHERE session_id=$1 RETURNING run_id`, sessionID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	tokens := make([]int64, 8)
	for i := range tokens {
		tokens[i] = nextRuntimeFence(t, ctx, q, botID, sessionID).Token
	}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for _, token := range tokens {
		wg.Go(func() {
			err := runtimefence.NewActivator(store).ReclaimWaitingDecision(ctx, botID, sessionID, runID, "new", "live", old.Token, token, []runtimefence.PreservedDecision{{Kind: runtimefence.DecisionUserInput, ID: input.ID}})
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, runtimefence.ErrStale) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("handoff winners=%d", winners.Load())
	}
}

// Use a non-owner, non-bypass role and reuse one physical connection while
// switching tenant scope. This is the hosted deployment contract, independent
// of the OSS composition root's singleton-team connection hook.
func TestPostgresWaitingDecisionHandoffWithForcedRLS(t *testing.T) {
	ctx := t.Context()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	q := dbsqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, q)
	old := nextRuntimeFence(t, ctx, q, botID, sessionID)
	createRuntimeFenceRun(t, ctx, pool, old)
	if err := runtimefence.Activate(ctx, store, old); err != nil {
		t.Fatal(err)
	}
	input := createRuntimeFenceUserInput(t, runtimefence.WithContext(ctx, old), userinput.NewService(nil, store), botID, sessionID, "pending", nil)
	var runID, teamID string
	if err := pool.QueryRow(ctx, `UPDATE session_runs SET state='waiting_decision' WHERE session_id=$1 RETURNING run_id,team_id`, sessionID).Scan(&runID, &teamID); err != nil {
		t.Fatal(err)
	}
	role := "handoff_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, query := range []string{"CREATE ROLE " + role + " NOLOGIN NOSUPERUSER NOBYPASSRLS", "GRANT USAGE ON SCHEMA public TO " + role, "GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO " + role, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO " + role} {
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP OWNED BY "+role)
		_, _ = pool.Exec(context.Background(), "DROP ROLE "+role)
	})
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+role)
		return err
	}
	scoped, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	activate := runtimefence.NewActivator(postgresstore.NewQueriesWithPool(scoped, dbsqlc.New(scoped)))
	token := nextRuntimeFence(t, ctx, q, botID, sessionID).Token
	preserved := []runtimefence.PreservedDecision{{Kind: runtimefence.DecisionUserInput, ID: input.ID}}
	var pid int32
	if err := scoped.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{uuid.NewString(), "", teamID} {
		if _, err := scoped.Exec(ctx, `SELECT set_config('memoh.team_id',$1,false)`, scope); err != nil {
			t.Fatal(err)
		}
		err := activate.ReclaimWaitingDecision(ctx, botID, sessionID, runID, "new", "live", old.Token, token, preserved)
		if scope == teamID {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("unbound or wrong tenant reclaimed run")
		}
		var currentPID int32
		if err := scoped.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&currentPID); err != nil {
			t.Fatal(err)
		}
		if currentPID != pid {
			t.Fatal("test did not reuse physical connection")
		}
	}
	var bypass, super bool
	if err := scoped.QueryRow(ctx, `SELECT rolbypassrls,rolsuper FROM pg_roles WHERE rolname=current_user`).Scan(&bypass, &super); err != nil || bypass || super {
		t.Fatalf("test role bypasses RLS: %v %v %v", bypass, super, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname IN ('bot_sessions','session_runs','user_input_requests') AND relrowsecurity AND relforcerowsecurity`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("FORCE RLS tables=%d err=%v", count, err)
	}
}

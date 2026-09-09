package acp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresRuntimeFenceStaleACPHandleCannotCancelCurrentDecisions(t *testing.T) {
	ctx := context.Background()
	pool := openACPRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createACPRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, queries)
	approvals := &recordingPostgresApprovalService{Service: toolapproval.NewService(nil, store, nil)}
	inputs := &recordingPostgresUserInputService{Service: userinput.NewService(nil, store)}

	tokenA, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate owner A token: %v", err)
	}
	fenceA := runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: tokenA}
	createACPRuntimeFenceRun(t, ctx, pool, fenceA)
	if err := runtimefence.Activate(ctx, store, fenceA); err != nil {
		t.Fatalf("activate owner A token: %v", err)
	}
	tokenB, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate owner B token: %v", err)
	}
	fenceB := runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: tokenB}
	finishACPRuntimeFenceRun(t, ctx, pool, fenceA)
	createACPRuntimeFenceRun(t, ctx, pool, fenceB)
	if err := runtimefence.Activate(ctx, store, fenceB); err != nil {
		t.Fatalf("activate owner B token: %v", err)
	}
	ownerB := runtimefence.WithContext(ctx, fenceB)

	approval, err := approvals.CreatePending(ownerB, toolapproval.CreatePendingInput{
		BotID: fenceB.BotID, SessionID: fenceB.SessionID, ToolCallID: "approval-current-owner", ToolName: "write",
		ToolInput: map[string]any{"path": "current-owner.txt"},
	})
	if err != nil {
		t.Fatalf("create current owner approval: %v", err)
	}
	input, err := inputs.CreatePending(ownerB, userinput.CreatePendingInput{
		BotID: fenceB.BotID, SessionID: fenceB.SessionID, ToolCallID: "input-current-owner", ToolName: userinput.ToolNameAskUser,
		Input: map[string]any{"questions": []any{map[string]any{
			"text": "Choose", "kind": userinput.QuestionKindSingleSelect,
			"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}},
		}}},
	})
	if err != nil {
		t.Fatalf("create current owner user input: %v", err)
	}

	runtimePool := newSessionPool(nil, nil, nil)
	runtimePool.SetToolApprovalService(approvals)
	runtimePool.SetUserInputService(inputs)
	stale := &runtimeHandle{
		id:               "rt-stale-postgres-owner",
		botID:            fenceA.BotID,
		boundSession:     fenceA.SessionID,
		status:           stateIdle,
		lastActive:       time.Now(),
		persistenceFence: fenceA,
		hadPrompt:        true,
	}
	runtimePool.mu.Lock()
	runtimePool.runtimes[stale.id] = stale
	runtimePool.bySession[stale.boundSession] = stale.id
	runtimePool.mu.Unlock()
	if err := runtimePool.closeHandle(stale); err != nil {
		t.Fatalf("close stale ACP handle: %v", err)
	}
	for name, fences := range map[string][]runtimefence.Fence{
		"approval":   approvals.recordedCleanupFences(),
		"user input": inputs.recordedCleanupFences(),
	} {
		if len(fences) != 2 {
			t.Fatalf("%s cleanup calls = %d, want pre and final cleanup", name, len(fences))
		}
		for _, fence := range fences {
			if fence != fenceA {
				t.Fatalf("%s cleanup fence = %#v, want %#v", name, fence, fenceA)
			}
		}
	}

	gotApproval, err := approvals.Get(ctx, approval.ID)
	if err != nil || gotApproval.Status != toolapproval.StatusPending {
		t.Fatalf("current approval after stale ACP cleanup = (%#v, %v)", gotApproval, err)
	}
	gotInput, err := inputs.Get(ctx, input.ID)
	if err != nil || gotInput.Status != userinput.StatusPending {
		t.Fatalf("current user input after stale ACP cleanup = (%#v, %v)", gotInput, err)
	}
}

type recordingPostgresApprovalService struct {
	*toolapproval.Service
	mu     sync.Mutex
	fences []runtimefence.Fence
}

func (s *recordingPostgresApprovalService) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]toolapproval.Request, error) {
	fence, _ := runtimefence.FromContext(ctx)
	s.mu.Lock()
	s.fences = append(s.fences, fence)
	s.mu.Unlock()
	return s.Service.CancelPendingForSession(ctx, botID, sessionID, reason)
}

func (s *recordingPostgresApprovalService) recordedCleanupFences() []runtimefence.Fence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtimefence.Fence(nil), s.fences...)
}

type recordingPostgresUserInputService struct {
	*userinput.Service
	mu     sync.Mutex
	fences []runtimefence.Fence
}

func (s *recordingPostgresUserInputService) CancelPendingForSession(ctx context.Context, botID, sessionID, reason string) ([]userinput.Request, error) {
	fence, _ := runtimefence.FromContext(ctx)
	s.mu.Lock()
	s.fences = append(s.fences, fence)
	s.mu.Unlock()
	return s.Service.CancelPendingForSession(ctx, botID, sessionID, reason)
}

func (s *recordingPostgresUserInputService) recordedCleanupFences() []runtimefence.Fence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtimefence.Fence(nil), s.fences...)
}

func openACPRuntimeFencePostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("ACP postgres runtime fence test is required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run ACP runtime fence integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("create ACP runtime fence pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping ACP runtime fence postgres: %v", err)
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", err)
		}
	}
	return pool
}

func createACPRuntimeFenceFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	userID := uuid.New()
	botUUID := uuid.New()
	sessionUUID := uuid.New()
	name := fmt.Sprintf("acp-runtime-fence-%s", uuid.NewString())
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user`, userID, name); err != nil {
		t.Fatalf("create ACP runtime fence user: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)", botUUID, userID, name); err != nil {
		t.Fatalf("create ACP runtime fence bot: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')", sessionUUID, botUUID); err != nil {
		t.Fatalf("create ACP runtime fence session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE id = $1", botUUID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})
	botID, err := dbpkg.ParseUUID(botUUID.String())
	if err != nil {
		t.Fatalf("parse ACP runtime fence bot id: %v", err)
	}
	sessionID, err := dbpkg.ParseUUID(sessionUUID.String())
	if err != nil {
		t.Fatalf("parse ACP runtime fence session id: %v", err)
	}
	return botID, sessionID
}

func createACPRuntimeFenceRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fence runtimefence.Fence) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, fencing_token,
			owner_since, live_generation
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', '{}'::jsonb, $7, $8, $9, now(), $10)
	`,
		uuid.New(), fence.BotID, fence.SessionID, uuid.NewString(), uuid.New(), fence.Token,
		"acp-runtime-fence-test", fmt.Sprintf("owner-%d", fence.Token), fence.Token,
		fmt.Sprintf("generation-%d", fence.Token),
	); err != nil {
		t.Fatalf("create ACP runtime fence run: %v", err)
	}
}

func finishACPRuntimeFenceRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fence runtimefence.Fence) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE session_runs
		SET state = 'lost', updated_at = now()
		WHERE session_id = $1 AND fencing_token = $2
	`, fence.SessionID, fence.Token); err != nil {
		t.Fatalf("finish ACP runtime fence run: %v", err)
	}
}

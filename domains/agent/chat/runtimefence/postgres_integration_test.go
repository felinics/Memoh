package runtimefence_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/runtimefence"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	decisionpostgres "github.com/memohai/memoh/domains/agent/decision/postgres"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	apiassembly "github.com/memohai/memoh/domains/api/assembly"
	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/dbtest"
)

var (
	runtimeFenceMigrationOnce sync.Once
	runtimeFenceMigrationErr  error
)

func TestPostgresRuntimeFenceActivationLeavesUnfencedDecisionsAlone(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	cluster := newDecisionCluster(t, pool)
	approvals := toolapproval.NewService(nil, cluster.Approval(), nil)
	inputs := userinput.NewService(nil, cluster.Input())

	approval, err := approvals.CreatePending(ctx, toolapproval.CreatePendingInput{
		BotID: botID, SessionID: sessionID, ToolCallID: "unfenced-approval", ToolName: "write",
		ToolInput: map[string]any{"path": "channel.txt"},
	})
	if err != nil {
		t.Fatalf("create unfenced approval: %v", err)
	}
	input := createRuntimeFenceUserInput(t, ctx, inputs, botID, sessionID, "unfenced-input", nil)
	fence := nextRuntimeFence(t, ctx, agentsqlc.New(pool), botID, sessionID)
	if err := runtimefence.Activate(ctx, cluster.RuntimeFence(), fence); err != nil {
		t.Fatalf("activate runtime fence: %v", err)
	}

	gotApproval, err := approvals.Get(ctx, approval.ID)
	if err != nil || gotApproval.Status != toolapproval.StatusPending {
		t.Fatalf("unfenced approval after activation = (%#v, %v)", gotApproval, err)
	}
	gotInput, err := inputs.Get(ctx, input.ID)
	if err != nil || gotInput.Status != userinput.StatusPending {
		t.Fatalf("unfenced user input after activation = (%#v, %v)", gotInput, err)
	}

	ownerCtx := runtimefence.WithContext(ctx, fence)
	runtimeApproval, err := approvals.CreatePending(ownerCtx, toolapproval.CreatePendingInput{
		BotID: botID, SessionID: sessionID, ToolCallID: "fenced-approval", ToolName: "write",
		ToolInput: map[string]any{"path": "runtime.txt"},
	})
	if err != nil {
		t.Fatalf("create fenced approval: %v", err)
	}
	runtimeInput := createRuntimeFenceUserInput(t, ownerCtx, inputs, botID, sessionID, "fenced-input", nil)
	nextFence := nextRuntimeFence(t, ctx, agentsqlc.New(pool), botID, sessionID)
	if err := runtimefence.Activate(ctx, cluster.RuntimeFence(), nextFence); err != nil {
		t.Fatalf("activate successor runtime fence: %v", err)
	}
	if got, err := approvals.Get(ctx, runtimeApproval.ID); err != nil || got.Status != toolapproval.StatusCancelled {
		t.Fatalf("fenced approval after takeover = (%#v, %v)", got, err)
	}
	if got, err := inputs.Get(ctx, runtimeInput.ID); err != nil || got.Status != userinput.StatusCanceled {
		t.Fatalf("fenced user input after takeover = (%#v, %v)", got, err)
	}
	if got, err := approvals.Get(ctx, approval.ID); err != nil || got.Status != toolapproval.StatusPending {
		t.Fatalf("unfenced approval after successor activation = (%#v, %v)", got, err)
	}
	if got, err := inputs.Get(ctx, input.ID); err != nil || got.Status != userinput.StatusPending {
		t.Fatalf("unfenced input after successor activation = (%#v, %v)", got, err)
	}
}

func TestPostgresRuntimeFenceSerializesToolApprovalShortIDs(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	cluster := newDecisionCluster(t, pool)
	approvals := toolapproval.NewService(nil, cluster.Approval(), nil)

	const count = 16
	start := make(chan struct{})
	results := make(chan toolapproval.Request, count)
	errs := make(chan error, count)
	var workers sync.WaitGroup
	workers.Add(count)
	for i := 0; i < count; i++ {
		i := i
		go func() {
			defer workers.Done()
			<-start
			request, err := approvals.CreatePending(ctx, toolapproval.CreatePendingInput{
				BotID: botID, SessionID: sessionID, ToolCallID: fmt.Sprintf("parallel-approval-%02d", i), ToolName: "write",
				ToolInput: map[string]any{"index": i},
			})
			if err != nil {
				errs <- err
				return
			}
			results <- request
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent approval creation: %v", err)
		}
	}
	shortIDs := make([]int, 0, count)
	for request := range results {
		shortIDs = append(shortIDs, request.ShortID)
	}
	if len(shortIDs) != count {
		t.Fatalf("created approvals = %d, want %d", len(shortIDs), count)
	}
	sort.Ints(shortIDs)
	for i, shortID := range shortIDs {
		if shortID != i+1 {
			t.Fatalf("sorted short ids = %v, want contiguous 1..%d", shortIDs, count)
		}
	}
}

func TestPostgresRuntimeFenceLocksBotBeforeSessionWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := agentsqlc.New(pool)
	cluster := newDecisionCluster(t, pool)
	fencePersistence := cluster.RuntimeFence()
	fence := nextRuntimeFence(t, ctx, queries, botID, sessionID)
	if err := runtimefence.Activate(ctx, fencePersistence, fence); err != nil {
		t.Fatalf("activate runtime fence: %v", err)
	}
	ownerCtx := runtimefence.WithContext(ctx, fence)
	botUUID := mustRuntimeFencePGUUID(t, botID)

	writerLocked := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- runtimefence.InTransaction(ownerCtx, fencePersistence, botID, sessionID, func(runtimefence.Store) error {
			close(writerLocked)
			select {
			case <-releaseWriter:
			case <-ownerCtx.Done():
				return ownerCtx.Err()
			}
			return nil
		})
	}()
	select {
	case <-writerLocked:
	case <-ctx.Done():
		t.Fatal("timed out waiting for fenced writer locks")
	}

	deleteConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire bot delete connection: %v", err)
	}
	defer deleteConn.Release()
	var deletePID int32
	if err := deleteConn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&deletePID); err != nil {
		t.Fatalf("load bot delete backend pid: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := deleteConn.Exec(ctx, "DELETE FROM api.bots WHERE id = $1", botUUID)
		deleteDone <- deleteErr
	}()
	waitForPostgresLockWait(t, ctx, pool, deletePID, deleteDone)
	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("fenced child write: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete bot after fenced child write: %v", err)
	}
}

func waitForPostgresLockWait(t *testing.T, ctx context.Context, pool *pgxpool.Pool, backendPID int32, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity
    WHERE pid = $1 AND wait_event_type = 'Lock'
)`, backendPID).Scan(&waiting); err != nil {
			t.Fatalf("inspect bot delete lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("bot delete completed before waiting on the fenced writer: %v", err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for bot delete lock wait")
		case <-ticker.C:
		}
	}
}

func TestPostgresRuntimeFenceClaimedInputRemainsRespondableAfterExpiry(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgres(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := agentsqlc.New(pool)
	cluster := newDecisionCluster(t, pool)
	inputs := userinput.NewService(nil, cluster.Input())
	expiresAt := time.Now().Add(250 * time.Millisecond)
	target := createRuntimeFenceUserInput(t, ctx, inputs, botID, sessionID, "claimed-expiring-input", &expiresAt)
	fence := nextRuntimeFence(t, ctx, queries, botID, sessionID)
	if err := runtimefence.ActivateWithOptions(ctx, cluster.RuntimeFence(), fence, runtimefence.ActivationOptions{
		PreserveDecision: &runtimefence.PreservedDecision{Kind: runtimefence.DecisionUserInput, ID: target.ID},
	}); err != nil {
		t.Fatalf("claim expiring input: %v", err)
	}
	time.Sleep(time.Until(expiresAt) + 50*time.Millisecond)
	ownerCtx := runtimefence.WithContext(ctx, fence)
	resolved, err := inputs.ResolveTarget(ownerCtx, userinput.ResolveInput{
		BotID: botID, SessionID: sessionID, ExplicitID: target.ID,
	})
	if err != nil || resolved.ID != target.ID {
		t.Fatalf("resolve claimed expired input = (%#v, %v)", resolved, err)
	}
	question := target.UIPayload.Questions[0]
	option := question.Options[0]
	submitted, err := inputs.Submit(ownerCtx, userinput.SubmitInput{
		RequestID: target.ID,
		Answers:   []agentdomain.QuestionAnswer{{QuestionID: question.ID, OptionIDs: []string{option.ID}}},
	})
	if err != nil || submitted.Status != userinput.StatusSubmitted {
		t.Fatalf("submit claimed expired input = (%#v, %v)", submitted, err)
	}
	if _, err := inputs.ResolveTarget(ctx, userinput.ResolveInput{
		BotID: botID, SessionID: sessionID, ExplicitID: target.ID,
	}); !errors.Is(err, userinput.ErrNotFound) {
		t.Fatalf("resolved input remains respondable without fence: %v", err)
	}
}

func openRuntimeFencePostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("PostgreSQL runtime fencing contracts are required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run PostgreSQL runtime fencing contracts")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("create PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		runtimeFenceMigrationOnce.Do(func() {
			runtimeFenceMigrationErr = dbtest.MigratePostgresLegacyUp(dsn)
		})
		if runtimeFenceMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", runtimeFenceMigrationErr)
		}
	}
	return pool
}

func newDecisionCluster(t *testing.T, pool *pgxpool.Pool) *decisionpostgres.Cluster {
	t.Helper()
	cluster, err := decisionpostgres.New(
		pool,
		func(tx pgx.Tx) decisionpostgres.BotSessionWriteLocker {
			return apiassembly.NewBotSessionLockerFromTx(tx)
		},
		decisionpostgres.AllowAllChannelIdentities(),
	)
	if err != nil {
		t.Fatalf("create decision postgres cluster: %v", err)
	}
	return cluster
}

func createRuntimeFenceFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	sessionID := uuid.New()
	name := "rtf-" + uuid.NewString()[:12]
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO iam.users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO iam.team_members (user_id, role)
		SELECT id, 'admin' FROM created_user`, userID, name); err != nil {
		t.Fatalf("create user fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO api.bots (id, owner_user_id, name) VALUES ($1, $2, $3)", botID, userID, name); err != nil {
		t.Fatalf("create bot fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO agent.bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')", sessionID, botID); err != nil {
		t.Fatalf("create session fixture: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM api.bots WHERE id = $1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM iam.users WHERE id = $1", userID)
	})
	return botID.String(), sessionID.String()
}

func mustRuntimeFencePGUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}

func nextRuntimeFence(t *testing.T, ctx context.Context, queries *agentsqlc.Queries, botID, sessionID string) runtimefence.Fence {
	t.Helper()
	token, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate runtime fence token: %v", err)
	}
	return runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: token}
}

func createRuntimeFenceUserInput(t *testing.T, ctx context.Context, inputs *userinput.Service, botID, sessionID, callID string, expiresAt *time.Time) userinput.Request {
	t.Helper()
	request, err := inputs.CreatePending(ctx, userinput.CreatePendingInput{
		BotID: botID, SessionID: sessionID, ToolCallID: callID, ToolName: userinput.ToolNameAskUser,
		Input: map[string]any{"questions": []any{map[string]any{
			"text": "Continue?", "kind": userinput.QuestionKindSingleSelect,
			"options": []any{map[string]any{"label": "Yes"}, map[string]any{"label": "No"}},
		}}},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("create user input: %v", err)
	}
	return request
}

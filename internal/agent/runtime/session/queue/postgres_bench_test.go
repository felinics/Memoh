package queue_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// These benchmarks measure the durable queue's hot read path (the UI list
// endpoint issues PendingSteer + PendingFollowUp) and the direct enqueue
// transaction. They need TEST_POSTGRES_DSN like the integration tests.

func seedPending(ctx context.Context, b *testing.B, queries *postgresstore.Queries, botID, sessionID string, steers, followUps int) {
	b.Helper()
	store := queue.NewPostgresStore
	for i := 0; i < steers; i++ {
		if err := queries.InTx(ctx, func(q dbstore.Queries) error {
			_, err := store(q).EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"steer"}`))
			return err
		}); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < followUps; i++ {
		if err := queries.InTx(ctx, func(q dbstore.Queries) error {
			_, err := store(q).EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"follow"}`))
			return err
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func benchQueries(ctx context.Context, b *testing.B) (*postgresstore.Queries, string, string) {
	b.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		b.Skip("set TEST_POSTGRES_DSN")
	}
	pool := openQueuePostgres(b, ctx)
	botID, sessionID, _ := createQueueFixture(b, ctx, pool)
	return postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool)), botID, sessionID
}

func BenchmarkListSessionQueues_SmallPending(b *testing.B) {
	ctx := context.Background()
	queries, botID, sessionID := benchQueries(ctx, b)
	seedPending(ctx, b, queries, botID, sessionID, 2, 3)
	store := queue.NewPostgresStore(queries)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := store.PendingQueues(ctx, sessionID, queue.DefaultPendingListLimit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListSessionQueues_LargeBacklog(b *testing.B) {
	ctx := context.Background()
	queries, botID, sessionID := benchQueries(ctx, b)
	seedPending(ctx, b, queries, botID, sessionID, 200, 800)
	store := queue.NewPostgresStore(queries)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := store.PendingQueues(ctx, sessionID, queue.DefaultPendingListLimit); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListSessionQueues_Parallel(b *testing.B) {
	ctx := context.Background()
	queries, botID, sessionID := benchQueries(ctx, b)
	seedPending(ctx, b, queries, botID, sessionID, 2, 3)
	store := queue.NewPostgresStore(queries)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, err := store.PendingQueues(ctx, sessionID, queue.DefaultPendingListLimit); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkEnqueueFollowUp_DirectTx(b *testing.B) {
	ctx := context.Background()
	queries, botID, sessionID := benchQueries(ctx, b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := queries.InTx(ctx, func(q dbstore.Queries) error {
			_, err := queue.NewPostgresStore(q).EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), fmt.Sprintf("bench-%d-%s", i, uuid.NewString()), []byte(`{"text":"follow"}`))
			return err
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnqueueFollowUp_ParallelSameSession(b *testing.B) {
	ctx := context.Background()
	queries, botID, sessionID := benchQueries(ctx, b)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := queries.InTx(ctx, func(q dbstore.Queries) error {
				_, err := queue.NewPostgresStore(q).EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"follow"}`))
				return err
			}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkCommitStep_ToolLoop measures the per-model-step coordinator
// transaction with no pending steer: lock session, step-commit lookup, lock
// run, claim attempt, commit insert. This runs once per model step and is the
// queue's real hot path.
func BenchmarkCommitStep_ToolLoop(b *testing.B) {
	ctx := context.Background()
	pool := openQueuePostgres(b, ctx)
	botID, sessionID, runID := createQueueFixture(b, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	coordinator := queue.NewPostgresCoordinator(queries, queries.InTx)
	run := sessionruntime.RunHandle{BotID: botID, SessionID: sessionID, RunID: runID, OwnerID: "owner", FencingToken: 1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := coordinator.CommitStep(ctx, queue.CommitStepRequest{
			Run: run, StepIndex: int64(i), CommitHash: fmt.Sprintf("h-%d", i), Kind: queue.StepToolLoop,
		})
		if err != nil {
			b.Fatal(err)
		}
		if result.Action != queue.Continue {
			b.Fatalf("action = %s", result.Action)
		}
	}
}

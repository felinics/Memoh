package queue_test

import (
	"context"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// TestQueueWriteLoad drives the direct PostgreSQL enqueue path with a fixed
// number of in-flight requests spread across many sessions. It is the write
// counterpart of TestQueueReadLoad and the A/B baseline against the removed
// Redis staging batcher, whose E2E test used the same shape (N requests,
// S sessions, F in flight).
//
//	MEMOH_RUN_QUEUE_WRITE_LOAD=1 TEST_POSTGRES_DSN=... go test ./internal/agent/runtime/session/queue -run TestQueueWriteLoad -v
//
// Tunables: QUEUE_WLOAD_N (default 100000), QUEUE_WLOAD_SESSIONS (default 100),
// QUEUE_WLOAD_INFLIGHT (comma list, default 64,256,2048).
func TestQueueWriteLoad(t *testing.T) {
	if os.Getenv("MEMOH_RUN_QUEUE_WRITE_LOAD") != "1" {
		t.Skip("set MEMOH_RUN_QUEUE_WRITE_LOAD=1 to run the queue write load test")
	}
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))

	total := envInt("QUEUE_WLOAD_N", 100000)
	sessions := envInt("QUEUE_WLOAD_SESSIONS", 100)
	levels := envIntList("QUEUE_WLOAD_INFLIGHT", []int{64, 256, 2048})

	botIDs, sessionIDs := createWriteLoadFixtures(t, ctx, pool, sessions)
	t.Logf("pgx pool max_conns=%d sessions=%d requests per level=%d", pool.Config().MaxConns, sessions, total)
	t.Logf("%-10s %-10s %-10s %-10s %-10s %-10s %-8s %-8s", "in_flight", "req/s", "p50", "p95", "p99", "max", "errors", "wall")
	for _, inFlight := range levels {
		runWriteLoad(t, ctx, queries, botIDs, sessionIDs, total, inFlight)
	}
}

func runWriteLoad(t *testing.T, ctx context.Context, queries *postgresstore.Queries, botIDs, sessionIDs []string, total, inFlight int) {
	t.Helper()
	store := queue.NewPostgresStore(queries)
	runWriteLoadWith(t, ctx, "", botIDs, sessionIDs, total, inFlight, func(ctx context.Context, botID, sessionID string) error {
		// Autocommit single statement, matching the application service.
		_, err := store.EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"load"}`))
		return err
	})
}

func runWriteLoadWith(t *testing.T, ctx context.Context, label string, botIDs, sessionIDs []string, total, inFlight int, enqueue func(context.Context, string, string) error) {
	t.Helper()
	jobs := make(chan int)
	latencies := make([]time.Duration, total)
	var errCount atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < inFlight; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				idx := i % len(sessionIDs)
				began := time.Now()
				err := enqueue(ctx, botIDs[idx], sessionIDs[idx])
				latencies[i] = time.Since(began)
				if err != nil {
					errCount.Add(1)
				}
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	wall := time.Since(start)
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	pct := func(p float64) time.Duration { return latencies[int(float64(total-1)*p)] }
	head := any(inFlight)
	if label != "" {
		head = label
	}
	t.Logf("%-10v %-10.0f %-10s %-10s %-10s %-10s %-8d %-8s",
		head, float64(total)/wall.Seconds(),
		pct(0.50).Round(10*time.Microsecond), pct(0.95).Round(10*time.Microsecond),
		pct(0.99).Round(10*time.Microsecond), latencies[total-1].Round(10*time.Microsecond),
		errCount.Load(), wall.Round(time.Millisecond))
}

func createWriteLoadFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessions int) ([]string, []string) {
	t.Helper()
	botIDs := make([]string, 0, sessions)
	sessionIDs := make([]string, 0, sessions)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			botID, sessionID, _ := createQueueFixture(t, ctx, pool)
			mu.Lock()
			botIDs = append(botIDs, botID)
			sessionIDs = append(sessionIDs, sessionID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return botIDs, sessionIDs
}

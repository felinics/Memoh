package queue_test

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// TestQueueReadLoad is a closed-loop read stress test for the UI list path.
// Each worker loops over PendingSteer + PendingFollowUp for a random session
// from a fixed pool, mirroring what one polling client costs the database.
//
// Run with:
//
//	MEMOH_RUN_QUEUE_READ_LOAD=1 TEST_POSTGRES_DSN=... go test ./internal/agent/runtime/session/queue -run TestQueueReadLoad -v
//
// Tunables (env): QUEUE_LOAD_SESSIONS (default 200), QUEUE_LOAD_CONCURRENCY
// (comma list, default 8,32,128), QUEUE_LOAD_DURATION (default 5s),
// QUEUE_LOAD_PENDING (items per queue per session, default 3).
func TestQueueReadLoad(t *testing.T) {
	if os.Getenv("MEMOH_RUN_QUEUE_READ_LOAD") != "1" {
		t.Skip("set MEMOH_RUN_QUEUE_READ_LOAD=1 to run the queue read load test")
	}
	ctx := context.Background()
	pool := openQueuePostgres(t, ctx)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	store := queue.NewPostgresStore(queries)

	sessions := envInt("QUEUE_LOAD_SESSIONS", 200)
	pending := envInt("QUEUE_LOAD_PENDING", 3)
	duration := envDuration("QUEUE_LOAD_DURATION", 5*time.Second)
	levels := envIntList("QUEUE_LOAD_CONCURRENCY", []int{8, 32, 128})

	t.Logf("seeding %d sessions x %d steer + %d follow-up", sessions, pending, pending)
	sessionIDs := make([]string, 0, sessions)
	var seedWG sync.WaitGroup
	seedSem := make(chan struct{}, 16)
	var seedMu sync.Mutex
	for i := 0; i < sessions; i++ {
		seedWG.Add(1)
		seedSem <- struct{}{}
		go func() {
			defer seedWG.Done()
			defer func() { <-seedSem }()
			botID, sessionID, _ := createQueueFixture(t, ctx, pool)
			for j := 0; j < pending; j++ {
				if err := enqueueOne(ctx, queries, botID, sessionID, true); err != nil {
					t.Error(err)
					return
				}
				if err := enqueueOne(ctx, queries, botID, sessionID, false); err != nil {
					t.Error(err)
					return
				}
			}
			seedMu.Lock()
			sessionIDs = append(sessionIDs, sessionID)
			seedMu.Unlock()
		}()
	}
	seedWG.Wait()
	if t.Failed() {
		return
	}

	poolMax := pool.Config().MaxConns
	t.Logf("pgx pool max_conns=%d", poolMax)
	t.Logf("%-12s %-10s %-10s %-10s %-10s %-10s %-8s", "concurrency", "req/s", "p50", "p95", "p99", "max", "errors")
	for _, concurrency := range levels {
		runReadLoad(t, ctx, store, sessionIDs, concurrency, duration)
	}
}

func runReadLoad(t *testing.T, ctx context.Context, store *queue.PostgresStore, sessionIDs []string, concurrency int, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	var (
		mu        sync.Mutex
		latencies []time.Duration
		errCount  atomic.Int64
		wg        sync.WaitGroup
	)
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			local := make([]time.Duration, 0, 4096)
			i := worker
			for time.Now().Before(deadline) {
				sessionID := sessionIDs[i%len(sessionIDs)]
				i += 7919 // stride so workers spread over sessions
				start := time.Now()
				_, _, err := store.PendingQueues(ctx, sessionID, queue.DefaultPendingListLimit)
				local = append(local, time.Since(start))
				if err != nil {
					errCount.Add(1)
				}
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	n := len(latencies)
	if n == 0 {
		t.Fatalf("concurrency %d produced no samples", concurrency)
	}
	pct := func(p float64) time.Duration {
		idx := int(float64(n-1) * p)
		return latencies[idx]
	}
	rps := float64(n) / duration.Seconds()
	t.Logf("%-12d %-10.0f %-10s %-10s %-10s %-10s %-8d",
		concurrency, rps,
		pct(0.50).Round(10*time.Microsecond), pct(0.95).Round(10*time.Microsecond),
		pct(0.99).Round(10*time.Microsecond), latencies[n-1].Round(10*time.Microsecond),
		errCount.Load())
}

func enqueueOne(ctx context.Context, queries *postgresstore.Queries, botID, sessionID string, steer bool) error {
	return queries.InTx(ctx, func(q dbstore.Queries) error {
		store := queue.NewPostgresStore(q)
		if steer {
			_, err := store.EnqueueSteer(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"steer"}`))
			return err
		}
		_, err := store.EnqueueFollowUp(ctx, botID, sessionID, uuid.NewString(), uuid.NewString(), []byte(`{"text":"follow"}`))
		return err
	})
}

func envInt(name string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envDuration(name string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envIntList(name string, fallback []int) []int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	out := make([]int, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if v, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

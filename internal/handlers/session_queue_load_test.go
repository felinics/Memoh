package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	dbpkg "github.com/felinics/memoh/internal/db"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// TestSessionQueueListHTTPLoad drives GET /bots/:bot_id/sessions/:session_id/queue
// through the real Echo router, JWT middleware, authorization, service, and
// JSON serialization against PostgreSQL. It measures what one queue panel
// refresh costs end to end, not only the SQL.
//
//	MEMOH_RUN_QUEUE_HTTP_LOAD=1 TEST_POSTGRES_DSN=... go test ./internal/handlers -run TestSessionQueueListHTTPLoad -v
//
// Tunables: QUEUE_HTTP_SESSIONS (default 100), QUEUE_HTTP_CONCURRENCY (comma
// list, default 8,32,128), QUEUE_HTTP_DURATION (default 5s).
func TestSessionQueueListHTTPLoad(t *testing.T) {
	if os.Getenv("MEMOH_RUN_QUEUE_HTTP_LOAD") != "1" {
		t.Skip("set MEMOH_RUN_QUEUE_HTTP_LOAD=1 to run the queue HTTP load test")
	}
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := postgresstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	queries := postgresstore.NewQueriesWithPool(pool, store.SQLC())

	sessions := loadEnvInt("QUEUE_HTTP_SESSIONS", 100)
	levels := loadEnvIntList("QUEUE_HTTP_CONCURRENCY", []int{8, 32, 128})
	duration := loadEnvDuration("QUEUE_HTTP_DURATION", 5*time.Second)

	secret := "queue-http-load-" + uuid.NewString() //nolint:gosec // test-only signing key
	userID, botID, sessionIDs := createQueueHTTPFixtures(t, ctx, pool, queries, sessions)
	token, _, err := auth.GenerateToken(userID, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	agentService := application.NewService(slog.New(slog.DiscardHandler), nil, queries, nil, nil, nil, nil, time.UTC, time.Minute)
	handler := NewSessionQueueHandler(queries, agentService, bots.NewService(nil, queries), accounts.NewService(nil, store))
	e := echo.New()
	e.Use(auth.JWTMiddleware(secret, nil))
	handler.Register(e)

	// Sanity check one request end to end before timing.
	probe := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/sessions/"+sessionIDs[0]+"/queue", nil)
	probe.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, probe)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe status = %d body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("probe body bytes=%d pgx pool max_conns=%d sessions=%d", rec.Body.Len(), pool.Config().MaxConns, sessions)
	t.Logf("%-12s %-10s %-10s %-10s %-10s %-10s %-8s", "concurrency", "req/s", "p50", "p95", "p99", "max", "non200")

	for _, concurrency := range levels {
		deadline := time.Now().Add(duration)
		var (
			mu        sync.Mutex
			latencies []time.Duration
			bad       atomic.Int64
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
					i += 7919
					req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID+"/queue", nil)
					req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
					rec := httptest.NewRecorder()
					start := time.Now()
					e.ServeHTTP(rec, req)
					local = append(local, time.Since(start))
					if rec.Code != http.StatusOK {
						bad.Add(1)
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
		pct := func(p float64) time.Duration { return latencies[int(float64(n-1)*p)] }
		t.Logf("%-12d %-10.0f %-10s %-10s %-10s %-10s %-8d",
			concurrency, float64(n)/duration.Seconds(),
			pct(0.50).Round(10*time.Microsecond), pct(0.95).Round(10*time.Microsecond),
			pct(0.99).Round(10*time.Microsecond), latencies[n-1].Round(10*time.Microsecond), bad.Load())
	}
}

func createQueueHTTPFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, queries *postgresstore.Queries, sessions int) (string, string, []string) {
	t.Helper()
	userID, botID := uuid.New(), uuid.New()
	name := "queue-http-load-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `WITH u AS (INSERT INTO users(id,username,is_active) VALUES($1,$2,true) RETURNING id) INSERT INTO team_members(user_id,role) SELECT id,'member' FROM u`, userID, name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots(id,owner_user_id,name) VALUES($1,$2,$3)`, botID, userID, name); err != nil {
		t.Fatal(err)
	}
	sessionIDs := make([]string, 0, sessions)
	for i := 0; i < sessions; i++ {
		sessionID, runID := uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO bot_sessions(id,bot_id,channel_type,runtime_type,created_by_user_id) VALUES($1,$2,'local','model',$3)`, sessionID, botID, userID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO session_runs(run_id,bot_id,session_id,invocation_id,turn_id,turn_position,state,input_json,input_fingerprint,owner_id,owner_since,fencing_token) VALUES($1,$2,$3,$4,$5,1,'running','{}',$6,'owner',now(),1)`, runID, botID, sessionID, uuid.NewString(), uuid.New(), "input"); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			if err := queries.InTx(ctx, func(q dbstore.Queries) error {
				s := queue.NewPostgresStore(q)
				if _, err := s.EnqueueSteer(ctx, botID.String(), sessionID.String(), uuid.NewString(), uuid.NewString(), []byte(`{"text":"steer"}`)); err != nil {
					return err
				}
				_, err := s.EnqueueFollowUp(ctx, botID.String(), sessionID.String(), uuid.NewString(), uuid.NewString(), []byte(`{"text":"follow"}`))
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		sessionIDs = append(sessionIDs, sessionID.String())
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE id=$1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id=$1", userID)
	})
	return userID.String(), botID.String(), sessionIDs
}

func loadEnvInt(name string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func loadEnvDuration(name string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func loadEnvIntList(name string, fallback []int) []int {
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

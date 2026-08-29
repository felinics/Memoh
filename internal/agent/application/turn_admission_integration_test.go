package application

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// This integration test pins the #1044 chain end to end at the backend: a
// channel-shaped StartTurnCommand admitted through admitTurnRun must surface
// its user message in the runtime snapshot a subscriber receives — the same
// snapshot the web frontend renders the live bubble from. It uses the real
// Manager and the real PostgreSQL ledger, so a break anywhere between wiring,
// activation, and snapshot delivery fails here rather than in production.

var (
	turnAdmissionMigrationOnce sync.Once
	turnAdmissionMigrationErr  error
)

func TestAdmitTurnRunRequestUserTurnReachesSubscriber(t *testing.T) {
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)

	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-turn-admission-test",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		Ledger:        ledger.NewPostgres(dbsqlc.New(pool), pool),
		// The durable ledger refuses admission without the fence that orders
		// its writes; wire the same pair the composition root wires.
		Fence: runtimefence.NewActivator(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))),
	})
	t.Cleanup(func() { _ = manager.Close() })

	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionRuntime: manager}
	cmd := turn.StartTurnCommand{
		TeamID:                  "team-1",
		Mode:                    turn.ModeChat,
		BotID:                   botID,
		ThreadID:                sessionID,
		Query:                   "hello from telegram",
		ExternalMessageID:       "ext-1",
		CurrentChannel:          "telegram",
		DisplayName:             "Alice",
		SourceChannelIdentityID: "ci-1",
	}
	admission, err := svc.admitTurnRun(ctx, cmd, func() {}, nil, nil)
	if err != nil {
		t.Fatalf("admitTurnRun() error = %v", err)
	}
	t.Cleanup(func() {
		// Close the run's record so the fixture leaves no active row behind;
		// the fixture rows themselves cascade away with the bot.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 10*time.Second)
		defer cancel()
		_ = manager.FinishRun(ctx, admission.Handle, sessionruntime.RunStatusCompleted, "")
	})

	sub, err := manager.Subscribe(ctx, botID, sessionID)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(sub.Close)

	select {
	case event := <-sub.C:
		if event.Type != sessionruntime.EventRuntimeSnapshot {
			t.Fatalf("first event = %q, want %q", event.Type, sessionruntime.EventRuntimeSnapshot)
		}
		if event.Snapshot == nil || event.Snapshot.CurrentRunView == nil {
			t.Fatal("snapshot has no current run, want the admitted run")
		}
		request := event.Snapshot.CurrentRunView.RequestUserTurn
		if request == nil {
			t.Fatal("snapshot request_user_turn is nil — the #1044 regression shape")
		}
		if request.Text != "hello from telegram" {
			t.Fatalf("request_user_turn text = %q, want the channel message", request.Text)
		}
		if request.TurnID != admission.TurnID {
			t.Fatalf("request_user_turn turn id = %q, want the admitted turn %q", request.TurnID, admission.TurnID)
		}
		if request.Platform != "telegram" || request.SenderDisplayName != "Alice" {
			t.Fatalf("platform/sender = (%q, %q), want telegram/Alice", request.Platform, request.SenderDisplayName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the subscription baseline snapshot")
	}
}

func openTurnAdmissionPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the turn admission PostgreSQL integration")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		turnAdmissionMigrationOnce.Do(func() {
			turnAdmissionMigrationErr = dbtest.MigratePostgresUp(dsn)
		})
		if turnAdmissionMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", turnAdmissionMigrationErr)
		}
	}
	return pool
}

// createTurnAdmissionFixture mirrors the ledger integration fixtures: random
// identities relying on the default team, cleaned up by deleting the bot
// (session_runs and bot_sessions cascade) and the user.
func createTurnAdmissionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	sessionID := uuid.New()
	name := "turn-admission-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create user fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)
	`, botID, userID, name); err != nil {
		t.Fatalf("create bot fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'telegram', 'model')
	`, sessionID, botID); err != nil {
		t.Fatalf("create session fixture: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	return botID.String(), sessionID.String()
}

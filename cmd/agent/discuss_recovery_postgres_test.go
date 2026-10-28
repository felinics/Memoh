package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/channel/discuss"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/contextview"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/settings"
)

func TestPostgresDiscussRecoveryPreservesBatchAcrossCompactorRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := openDiscussRecoveryPostgres(t, ctx)
	botID, sessionID := createDiscussRecoveryFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, sqlc.New(pool))
	var teamID string
	if err := pool.QueryRow(ctx, `SELECT team_id FROM bots WHERE id=$1`, botID).Scan(&teamID); err != nil {
		t.Fatal(err)
	}
	var summaries, modelCalls atomic.Int32
	var lastRequest atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		summarizes, stream, err := summaryRequest(body)
		if err != nil {
			t.Error(err)
			return
		}
		if !summarizes {
			modelCalls.Add(1)
			lastRequest.Store(string(body))
			writeAnswer(w, stream, "reply", "DRIVER_RECOVERY_OK")
			return
		}
		summaries.Add(1)
		writeAnswer(w, stream, "summary", "Earlier history preserved.")
	}))
	defer server.Close()
	chatModel, providerID := uuid.NewString(), uuid.NewString()
	providerConfig, _ := json.Marshal(map[string]string{"base_url": server.URL, "api_key": "test-key"})
	if _, err := pool.Exec(ctx, `INSERT INTO providers(id,name,client_type,config) VALUES($1,$2,'openai-completions',$3)`, providerID, providerID, providerConfig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID) })
	config, _ := json.Marshal(map[string]any{"context_window": 8000, "compatibilities": []string{}})
	if _, err := pool.Exec(ctx, `INSERT INTO models(id,model_id,provider_id,config) VALUES($1,'chat-model',$2,$3)`, chatModel, providerID, config); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bots SET chat_model_id=$2 WHERE id=$1`, botID, chatModel); err != nil {
		t.Fatal(err)
	}
	var rc timeline.RenderedContext
	var after timeline.DiscussCursorPosition
	logger := slog.Default()
	cursors := timeline.NewEventStore(logger, queries)
	messages := messagepkg.NewService(logger, queries)
	base := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	appendMessage := func(id, role, text string, self bool) {
		created := base.Add(time.Duration(len(rc)+1) * time.Second)
		content, _ := json.Marshal(text)
		if _, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, ExternalMessageID: id, Role: role, Content: content, SessionMode: "discuss"}); err != nil {
			t.Fatal(err)
		}
		rc = append(rc, timeline.RenderedSegment{MessageID: id, ReceivedAtMs: created.UnixMilli(), IsSelfSent: self, Content: []timeline.RenderedContentPiece{{Type: "text", Text: text}}})
	}
	for i := range 6 {
		appendMessage(fmt.Sprintf("old-%d", i), "user", strings.Repeat("history ", 1000), false)
	}
	after = timeline.ConsumedDiscussCursor(rc)
	if err := cursors.UpsertDiscussCursor(ctx, sessionID, "default", "", "", after); err != nil {
		t.Fatal(err)
	}
	const rounds = 32
	// The last round only adds one small message: the round after the
	// recoveries must be answered with the summaries they persisted.
	for round := 0; round <= rounds; round++ {
		nextRound := round == rounds
		// Grow consumed history again so each round must make durable progress.
		if round > 0 && !nextRound {
			appendMessage(fmt.Sprintf("growth-%d", round), "assistant", strings.Repeat("old history ", 1200), true)
		}
		a, b := fmt.Sprintf("input-a-%d", round), fmt.Sprintf("input-b-%d", round)
		textA, textB := a+strings.Repeat("a", 600), b+strings.Repeat("b", 600)
		if nextRound {
			a, textA = "next-round", "next-round small input"
			appendMessage(a, "user", textA, false)
			b, textB = a, textA
		} else {
			appendMessage(a, "user", textA, false)
			appendMessage(b, "user", textB, false)
			appendMessage(fmt.Sprintf("echo-%d", round), "assistant", "self echo", true)
		}

		manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: uuid.NewString(), Ledger: ledger.NewPostgres(sqlc.New(pool), pool), Fence: runtimefence.NewActivator(queries)})
		t.Cleanup(func() { _ = manager.Close() })
		service := application.NewService(logger, models.NewService(logger, queries), queries, messages, settings.NewService(logger, queries, nil, nil), nil, native.New(native.Deps{Logger: logger, ContextViewApplier: contextview.ProviderRunConfigApplier(logger)}), time.UTC, time.Minute)
		service.SetSessionRuntime(manager)
		service.SetCompactionService(compaction.NewService(logger, queries))
		service.SetContextAbsoluteMaxTokens(16000)

		cursor := &notifyingRecoveryCursor{EventStore: cursors, advanced: make(chan timeline.DiscussCursorPosition, 2)}
		driver := discuss.NewDiscussDriver(discuss.DiscussDriverDeps{Turn: service, CursorStore: cursor, MessageService: messages, Checkpoints: timeline.NewCheckpointSource(logger, queries, messages), AdmissionMaxTokens: 16000, Logger: logger})
		t.Cleanup(driver.StopAll)
		cfg := discuss.DiscussSessionConfig{BotID: botID, ThreadID: sessionID, TeamID: teamID}
		before := summaries.Load()
		callsBefore := modelCalls.Load()
		driver.NotifyRC(ctx, sessionID, rc, cfg)
		select {
		case after = <-cursor.advanced:
		case <-ctx.Done():
			t.Fatalf("round %d did not complete: summary_calls=%d provider_calls=%d", round, summaries.Load()-before, modelCalls.Load()-callsBefore)
		}
		if after != timeline.ConsumedDiscussCursor(rc) {
			t.Fatalf("cursor did not commit the full current batch: %+v", after)
		}
		switch passes := summaries.Load() - before; {
		case round == 0 && passes < 2:
			t.Fatalf("fixture must need a compaction of more than one summarizer pass, got %d", passes)
		case !nextRound && passes == 0:
			t.Fatalf("round %d grew history without compacting it", round)
		case nextRound && passes != 0:
			t.Fatalf("next round compacted again: summaries=%d", passes)
		}
		request, _ := lastRequest.Load().(string)
		if modelCalls.Load()-callsBefore != 1 || strings.Count(request, textA) != 1 || strings.Count(request, textB) != 1 {
			t.Fatalf("round %d: provider calls=%d, current input multiplicity=%d/%d", round, modelCalls.Load()-callsBefore, strings.Count(request, textA), strings.Count(request, textB))
		}

		var compacted int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+historyRowsSQL+`) h WHERE h.source_message_id=ANY($2) AND h.folded`, sessionID, []string{a, b}).Scan(&compacted); err != nil {
			t.Fatal(err)
		}
		if compacted != 0 {
			t.Fatal("current batch was compacted")
		}
		assertActiveCheckpointHolds(t, ctx, queries, sessionID)
		driver.StopAll()
		_ = manager.Close()
	}
	var replies, pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND role='assistant' AND content::text LIKE '%DRIVER_RECOVERY_OK%'`, sessionID).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE session_id=$1 AND status='pending'`, sessionID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if replies != rounds+1 || pending != 0 {
		t.Fatalf("replies=%d pending=%d", replies, pending)
	}
	t.Logf("rounds=%d+next summary_calls=%d provider_calls=%d stored_replies=%d pending=0 duplicate_current=0", rounds, summaries.Load(), modelCalls.Load(), replies)
}

type notifyingRecoveryCursor struct {
	*timeline.EventStore
	advanced chan timeline.DiscussCursorPosition
}

func (c *notifyingRecoveryCursor) UpsertDiscussCursor(ctx context.Context, sessionID, scope, route, source string, position timeline.DiscussCursorPosition) error {
	if err := c.EventStore.UpsertDiscussCursor(ctx, sessionID, scope, route, source, position); err != nil {
		return err
	}
	c.advanced <- position
	return nil
}

// assertActiveCheckpointHolds fails unless the session's active checkpoint
// holds as it stands: every row it keeps is a context row, every summary it
// holds is usable, and no context row appeared below it.
func assertActiveCheckpointHolds(t *testing.T, ctx context.Context, queries dbstore.Queries, sessionID string) {
	t.Helper()
	active, err := checkpoint.NewStore(nil, queries).Active(ctx, sessionID)
	if err != nil || active.ID == "" {
		t.Fatalf("load the active history checkpoint: %q, %v", active.ID, err)
	}
	kept, err := queries.ListHistoryMessagesByIDs(ctx, sqlc.ListHistoryMessagesByIDsParams{
		SessionID: dbpkg.ParseUUIDOrEmpty(sessionID),
		Ids:       active.RowUUIDs(),
	})
	if err != nil || len(kept) != len(active.RowIDs()) {
		t.Fatalf("active history checkpoint %s keeps %d rows, %d of them context rows: %v", active.ID, len(active.RowIDs()), len(kept), err)
	}
}

func openDiscussRecoveryPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
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
		turnAdmissionMigrationErr := dbtest.MigratePostgresUp(dsn)
		if turnAdmissionMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", turnAdmissionMigrationErr)
		}
	}
	return pool
}

// createDiscussRecoveryFixture mirrors the ledger integration fixtures: random
// identities relying on the default team, cleaned up by deleting the bot
// (session_runs and bot_sessions cascade) and the user.
func createDiscussRecoveryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
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
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type, type)
		VALUES ($1, $2, 'telegram', 'model', 'discuss')
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

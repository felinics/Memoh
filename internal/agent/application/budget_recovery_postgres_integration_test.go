package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/channel/discuss"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

type postgresBudgetCompactor struct {
	queries *postgresstore.Queries
	url     string
}

func (r postgresBudgetCompactor) RunCompactionSync(ctx context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	cfg.BaseURL = r.url
	cfg.ModelRecordID = ""
	cfg.MaxCompactTokens = 5000
	return compaction.NewService(slog.Default(), r.queries).RunCompactionSync(ctx, cfg)
}

func TestPostgresDiscussRecoveryPreservesBatchAcrossCompactorRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, sqlc.New(pool))
	var summaries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if max(request.MaxTokens, request.MaxCompletionTokens) > 400 {
			t.Errorf("summary cap ignored replay budget: %+v", request)
		}
		summaries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"summary","object":"chat.completion","model":"compact-model","choices":[{"index":0,"message":{"role":"assistant","content":"Earlier history preserved."},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`))
	}))
	defer server.Close()
	var rc timeline.RenderedContext
	var after timeline.DiscussCursorPosition
	logger := slog.Default()
	cursors := timeline.NewEventStore(logger, queries)
	messages := messagepkg.NewService(logger, queries)
	base := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	appendMessage := func(id, role, text string, self bool) {
		created := base.Add(time.Duration(len(rc)+1) * time.Second)
		content, _ := json.Marshal(text)
		msg, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, ExternalMessageID: id, Role: role, Content: content, SessionMode: "discuss"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE bot_history_messages SET created_at=$2 WHERE id=$1`, msg.ID, created); err != nil {
			t.Fatal(err)
		}

		rc = append(rc, timeline.RenderedSegment{MessageID: id, ReceivedAtMs: created.UnixMilli(), IsSelfSent: self, Content: []timeline.RenderedContentPiece{{Type: "text", Text: text}}})
	}
	for i := range 6 {
		appendMessage(fmt.Sprintf("old-%d", i), "user", strings.Repeat("history ", 1000), false)
	}
	after = timeline.ConsumedDiscussCursor(rc)
	const rounds = 32
	for round := 0; round < rounds; round++ {
		// Grow consumed history again so each round must make durable progress.
		if round > 0 {
			appendMessage(fmt.Sprintf("growth-%d", round), "assistant", strings.Repeat("old history ", 1000), true)
			after = timeline.ConsumedDiscussCursor(rc)
		}
		a, b := fmt.Sprintf("input-a-%d", round), fmt.Sprintf("input-b-%d", round)
		textA, textB := a+strings.Repeat("a", 600), b+strings.Repeat("b", 600)
		appendMessage(a, "user", textA, false)
		appendMessage(b, "user", textB, false)
		appendMessage(fmt.Sprintf("echo-%d", round), "assistant", "self echo", true)

		if err := cursors.UpsertDiscussCursor(ctx, sessionID, "default", "", "", after); err != nil {
			t.Fatal(err)
		}
		service, resolver, _, _, _ := discussBudgetRecoveryFixture(t)
		configureDiscussLifecycle(service)
		service.logger = logger
		resolver.resolveResult.RuntimeType = "model"
		provider := &triggerLifecycleProvider{}
		resolver.resolveResult.RunConfig.Model.Provider = provider
		service.compactionService = postgresBudgetCompactor{queries: queries, url: server.URL}
		stored := 0
		service.turnHooks.storeRound = func(ctx context.Context, _, _, _, _, _ string, response []sdk.Message, _ string, _ *contextfrag.LifecycleHolder) error {
			stored++
			for _, msg := range sdkMessagesToModelMessages(response) {
				appendMessage(fmt.Sprintf("reply-%d", round), msg.Role, msg.TextContent(), true)
			}
			return nil
		}
		cursor := &notifyingRecoveryCursor{EventStore: cursors, advanced: make(chan timeline.DiscussCursorPosition, 2)}
		driver := discuss.NewDiscussDriver(discuss.DiscussDriverDeps{Turn: service, CursorStore: cursor, MessageService: messages, Artifacts: compaction.NewTimelineArtifactSource(queries), AdmissionMaxTokens: 16000, Logger: logger})
		t.Cleanup(driver.StopAll)
		cfg := discuss.DiscussSessionConfig{BotID: botID, ThreadID: sessionID, TeamID: "team-1"}
		before := summaries.Load()
		driver.NotifyRC(ctx, sessionID, rc, cfg)
		select {
		case after = <-cursor.advanced:
		case <-ctx.Done():
			t.Fatalf("round %d did not complete: summary_calls=%d provider_calls=%d", round, summaries.Load()-before, provider.callCount())
		}
		if round == 0 && summaries.Load()-before != 3 {
			t.Fatalf("fixture must require exactly three recoveries, got %d", summaries.Load()-before)
		}
		if provider.callCount() != 1 || stored != 1 || countRecoveryText(provider.params.Messages, textA) != 1 || countRecoveryText(provider.params.Messages, textB) != 1 {
			t.Fatalf("round %d: provider=%d stored=%d", round, provider.callCount(), stored)
		}
		var compacted int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND source_message_id=ANY($2) AND compact_id IS NOT NULL`, sessionID, []string{a, b}).Scan(&compacted); err != nil {
			t.Fatal(err)
		}
		if compacted != 0 {
			t.Fatal("current batch was compacted")
		}
		frontier, err := compaction.NewArtifactProjection(queries).LoadActiveSession(ctx, compaction.ArtifactOwner{BotID: botID, SessionID: sessionID, SessionIDKnown: true})
		if err != nil || len(frontier.Issues) > 0 {
			t.Fatalf("round %d invalid frontier: %v %+v", round, err, frontier.Issues)
		}
		driver.StopAll()

	}
	if summaries.Load() < rounds || summaries.Load() > rounds*3 {
		t.Fatalf("unexpected bounded recovery count=%d", summaries.Load())
	}
	var replies, pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND source_message_id LIKE 'reply-%'`, sessionID).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE session_id=$1 AND status='pending'`, sessionID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if replies != rounds || pending != 0 {
		t.Fatalf("replies=%d pending=%d", replies, pending)
	}
	t.Logf("rounds=%d summary_calls=%d provider_calls=%d stored_replies=%d pending=0 duplicate_current=0", rounds, summaries.Load(), rounds, replies)
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

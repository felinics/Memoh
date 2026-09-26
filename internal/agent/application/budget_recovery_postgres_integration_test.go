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
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

type postgresBudgetCompactor struct {
	service *compaction.Service
	url     string
}

func (r postgresBudgetCompactor) RunCompactionSync(ctx context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	cfg.BaseURL = r.url
	cfg.ModelRecordID = ""
	return r.service.RunCompactionSync(ctx, cfg)
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
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	appendMessage := func(id, role, text string, self bool) {
		created := base.Add(time.Duration(len(rc)+1) * time.Second)
		content, _ := json.Marshal(text)
		_, err := pool.Exec(ctx, `INSERT INTO bot_history_messages(id,bot_id,session_id,source_message_id,role,content,session_mode,created_at,turn_visible,turn_position,turn_message_seq,turn_id) VALUES($1,$2,$3,$4,$5,$6,'discuss',$7,true,$8,0,gen_random_uuid())`, uuid.NewString(), botID, sessionID, id, role, content, created, len(rc)+1)
		if err != nil {
			t.Fatal(err)
		}
		rc = append(rc, timeline.RenderedSegment{MessageID: id, ReceivedAtMs: created.UnixMilli(), IsSelfSent: self, Content: []timeline.RenderedContentPiece{{Type: "text", Text: text}}})
	}
	appendMessage("old-user", "user", strings.Repeat("old user ", 1000), false)
	appendMessage("old-answer", "assistant", strings.Repeat("old answer ", 1000), true)
	after = timeline.ConsumedDiscussCursor(rc)
	const rounds = 32
	for round := 0; round < rounds; round++ {
		// Grow consumed history again so each round must make durable progress.
		if round > 0 {
			appendMessage(fmt.Sprintf("old-%d", round), "assistant", strings.Repeat("old history ", 1000), true)
			after = timeline.ConsumedDiscussCursor(rc)
		}
		a, b := fmt.Sprintf("input-a-%d", round), fmt.Sprintf("input-b-%d", round)
		textA, textB := a+strings.Repeat("a", 600), b+strings.Repeat("b", 600)
		appendMessage(a, "user", textA, false)
		appendMessage(b, "user", textB, false)
		appendMessage(fmt.Sprintf("echo-%d", round), "assistant", "self echo", true)
		service, resolver, _, _, cmd := discussBudgetRecoveryFixture(t)
		configureDiscussLifecycle(service)
		provider := &triggerLifecycleProvider{}
		resolver.resolveResult.RunConfig.Model.Provider = provider
		cmd.BotID, cmd.ThreadID = botID, sessionID
		stored := 0
		service.turnHooks.storeRound = func(_ context.Context, _, _, _, _, _ string, _ []sdk.Message, _ string, _ *contextfrag.LifecycleHolder) error {
			stored++
			appendMessage(fmt.Sprintf("reply-%d", round), "assistant", "completed", true)
			return nil
		}
		completed := false
		for attempt := 0; attempt < 3; attempt++ {
			// A new service instance must recover solely from committed PostgreSQL state.
			service.compactionService = postgresBudgetCompactor{service: compaction.NewService(slog.New(slog.DiscardHandler), queries), url: server.URL}
			artifacts, err := compaction.NewTimelineArtifactSource(queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			composed, admission := timeline.ComposeContextWithArtifactsBudgeted(rc, nil, artifacts, timeline.ComposeBudget{MaxTokens: 16000, After: &after})
			if composed == nil {
				t.Fatalf("unexpected channel rejection: %+v", admission)
			}
			wire, _ := json.Marshal(composed.Messages)
			cmd.DiscussMessages = nil
			if err := json.Unmarshal(wire, &cmd.DiscussMessages); err != nil {
				t.Fatal(err)
			}
			cmd.DiscussCurrentSources = []turn.ContextMessageSource{{Kind: "external", ID: a, Current: true}, {Kind: "external", ID: b, Current: true}}
			cmd.DiscussContextTokens, cmd.DiscussCurrentTokens = admission.EstimatedTokens, admission.CurrentTokens
			handle, err := service.StartTurn(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			recompose := false
			for event := range handle.Events() {
				if event.Kind == turn.DiscussEventRecompose {
					recompose = true
				}
			}
			for err := range handle.Errs() {
				if err != nil {
					t.Fatal(err)
				}
			}
			var compacted int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND source_message_id=ANY($2) AND compact_id IS NOT NULL`, sessionID, []string{a, b}).Scan(&compacted); err != nil {
				t.Fatal(err)
			}
			if compacted != 0 {
				t.Fatal("current batch was compacted")
			}
			if recompose {
				if provider.callCount() != 0 || stored != 0 {
					t.Fatal("recovery dispatched or persisted a response")
				}
				continue
			}
			if provider.callCount() != 1 || stored != 1 || countRecoveryText(provider.params.Messages, textA) != 1 || countRecoveryText(provider.params.Messages, textB) != 1 {
				t.Fatalf("batch failed after recovery: provider=%d stored=%d", provider.callCount(), stored)
			}
			after = timeline.ConsumedDiscussCursor(rc)
			completed = true
			break
		}
		if !completed {
			t.Fatalf("round %d exhausted recovery without progress", round)
		}
	}
	if summaries.Load() < rounds || summaries.Load() > rounds*2 {
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

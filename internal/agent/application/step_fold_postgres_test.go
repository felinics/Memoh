package application

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/messageconv"
)

// A fold inside a run reads back the rows the run's steps committed. The
// bytes PostgreSQL hands back are not the bytes the step wrote (jsonb
// reorders keys, adds spaces and unescapes <), so the sources a step
// names must not pin a version the step never read.
func TestPostgresRunFoldsRowsItCommitted(t *testing.T) {
	ctx := t.Context()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	messages := messagepkg.NewService(slog.Default(), queries)

	persist := func(message turn.ModelMessage, request string) messagepkg.Message {
		t.Helper()
		content, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		row, err := messages.Persist(ctx, messagepkg.PersistInput{BotID: botID, SessionID: sessionID, Role: message.Role, Content: content, TurnRequestMessageID: request})
		if err != nil {
			t.Fatalf("persist %s: %v", message.Role, err)
		}
		return row
	}
	request := persist(messageconv.SDKMessagesToModelMessages([]sdk.Message{sdk.UserMessage("read the <files> & report")})[0], "")
	view := stepProvenance([]messagepkg.Message{request}, []string{request.ID})[0].Sources
	for i := range 12 {
		callID := fmt.Sprintf("call-%d", i)
		var rows []messagepkg.Message
		for _, message := range messageconv.SDKMessagesToModelMessages([]sdk.Message{
			{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
				sdk.TextPart{Text: fmt.Sprintf("reading <file-%d> & its notes", i)},
				sdk.ToolCallPart{ToolCallID: callID, ToolName: "read", Input: toolexec.ArgumentsFromValue(map[string]any{"path": fmt.Sprintf("file-%d", i)})},
			}},
			sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: callID, ToolName: "read", Result: toolexec.OutputFromValue(strings.Repeat(fmt.Sprintf("<line %d> & more ", i), 40))}),
		}) {
			rows = append(rows, persist(message, request.ID))
		}
		for _, provenance := range stepProvenance(rows, []string{request.ID}) {
			view = append(view, provenance.Sources...)
		}
	}

	summarizer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"summary","object":"chat.completion","model":"compact-model","choices":[{"index":0,"message":{"role":"assistant","content":"The files were read."},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(summarizer.Close)

	passes, err := compaction.NewService(slog.Default(), queries).Compact(ctx, compaction.TriggerConfig{
		BotID: botID, SessionID: sessionID, ModelID: "compact-model", ClientType: "openai-completions",
		BaseURL: summarizer.URL, APIKey: "test-key", SummaryWindowTokens: 60000, HistoryBudgetTokens: 1500,
		View: view, Anchors: view[:1],
	})
	if err != nil {
		t.Fatalf("fold of the run's own rows: %v", err)
	}
	if len(passes) == 0 {
		t.Fatal("no pass committed over the run's own rows")
	}
}

package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	compaction "github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func TestDropEmptyHistoryFailures(t *testing.T) {
	empty := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("")})[0]
	kept := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("hello")})[0]
	records := []historyfrag.HistoryRecord{
		{ModelMessage: empty, Metadata: map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "agent.response_timeout"}},
		{ModelMessage: kept, Metadata: map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "agent.response_timeout"}},
		{ModelMessage: empty},
	}
	got := dropEmptyHistoryFailures(records)
	if len(got) != 2 {
		t.Fatalf("kept %d records, want 2", len(got))
	}
	if got[0].ModelMessage.TextContent() != "hello" {
		t.Fatalf("first kept record = %#v", got[0].ModelMessage)
	}
	if strings.TrimSpace(got[1].ModelMessage.TextContent()) != "" || historyErrorCode(got[1].Metadata) != "" {
		t.Fatalf("empty unmarked assistant should stay, got %#v", got[1])
	}
}

func TestProjectInterruptedHistoryReasoning(t *testing.T) {
	records := []historyfrag.HistoryRecord{{
		ModelMessage: sdkMessagesToModelMessages([]sdk.Message{{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.ReasoningPart{Text: "partial reasoning"}, sdk.TextPart{Text: "partial text"},
			},
		}})[0],
		Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
	}}
	got := modelMessageToSDKMessage(projectInterruptedHistoryReasoning(records)[0].ModelMessage)
	if len(got.Content) != 1 || got.Content[0].(sdk.TextPart).Text !=
		messagepkg.AgentStepInterruptedReasoningPrefix+"partial reasoning\n\npartial text" {
		t.Fatalf("projected content = %#v", got.Content)
	}
	if len(modelMessageToSDKMessage(records[0].ModelMessage).Content) != 2 {
		t.Fatal("input records were mutated")
	}
}

func TestProjectInterruptedHistoryReasoningKeepsOpaqueBlock(t *testing.T) {
	checkpoint := historyfrag.HistoryRecord{
		ModelMessage: sdkMessagesToModelMessages([]sdk.Message{{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ReasoningPart{
				ID:     "r1",
				Format: sdk.ReasoningFormatAnthropic,
				Model:  "claude-sonnet-4-20250514",
				ProviderMetadata: map[string]any{
					"anthropic": map[string]any{"redactedData": "BLOB"},
				},
			}},
		}})[0],
		Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
	}

	got := modelMessageToSDKMessage(projectInterruptedHistoryReasoning([]historyfrag.HistoryRecord{checkpoint})[0].ModelMessage)
	if len(got.Content) != 1 {
		t.Fatalf("projected content = %#v, want one opaque reasoning block", got.Content)
	}
	part, ok := got.Content[0].(sdk.ReasoningPart)
	if !ok {
		t.Fatalf("content[0] = %T, want ReasoningPart", got.Content[0])
	}
	if part.ID != "r1" || part.Format != sdk.ReasoningFormatAnthropic ||
		part.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("reasoning provenance was not preserved: %#v", part)
	}
	meta, _ := part.ProviderMetadata["anthropic"].(map[string]any)
	if data, _ := meta["redactedData"].(string); data != "BLOB" {
		t.Fatalf("redactedData = %q, want BLOB", data)
	}
}

func TestProjectInterruptedHistoryReasoningSkipsSupersededCheckpoint(t *testing.T) {
	checkpoint := historyfrag.HistoryRecord{
		ModelMessage: sdkMessagesToModelMessages([]sdk.Message{{
			Role:    sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial reasoning"}},
		}})[0],
		Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
	}
	answer := historyfrag.HistoryRecord{
		ModelMessage: sdkMessagesToModelMessages([]sdk.Message{{
			Role:    sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.TextPart{Text: "the answer"}},
		}})[0],
	}
	projected := projectInterruptedHistoryReasoning([]historyfrag.HistoryRecord{checkpoint, answer})
	got := modelMessageToSDKMessage(projected[0].ModelMessage)
	if len(got.Content) != 1 {
		t.Fatalf("projected content = %#v, want the reasoning part untouched", got.Content)
	}
	if _, ok := got.Content[0].(sdk.ReasoningPart); !ok {
		t.Fatalf("superseded checkpoint was projected into prompt text: %#v", got.Content[0])
	}
}

const (
	pipelineTestBotID     = "11111111-1111-1111-1111-111111111111"
	pipelineTestSessionID = "22222222-2222-2222-2222-222222222222"
)

type fakeArtifactLineageQueries struct {
	dbstore.Queries
	rows []sqlc.BotHistoryMessageCompact
}

func (f fakeArtifactLineageQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return f.rows, nil
}

func (fakeArtifactLineageQueries) GetCompactionLogByID(context.Context, pgtype.UUID) (sqlc.BotHistoryMessageCompact, error) {
	return sqlc.BotHistoryMessageCompact{}, pgx.ErrNoRows
}

func (fakeArtifactLineageQueries) ListCompactionArtifactParentIDsBySuccessor(context.Context, sqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error) {
	return nil, nil
}

func compactionLogRow(t *testing.T, summary string, coveredExternalID string, createdAtMs int64) sqlc.BotHistoryMessageCompact {
	t.Helper()
	coverage, err := json.Marshal([]compaction.CoveredSource{{
		Ref: contextfrag.ContextRef{
			Namespace:   "bot_history_message",
			ID:          "33333333-3333-3333-3333-333333333333",
			Schema:      contextfrag.SchemaContextRef,
			HashAlgo:    contextfrag.HashAlgoSHA256,
			HashScope:   contextfrag.HashScopeSourcePayload,
			ContentHash: "deadbeef",
		},
		ExternalMessageID: coveredExternalID,
		CreatedAtMs:       createdAtMs,
	}})
	if err != nil {
		t.Fatalf("encode coverage: %v", err)
	}
	id, err := dbpkg.ParseUUID("44444444-4444-4444-4444-444444444444")
	if err != nil {
		t.Fatalf("parse artifact id: %v", err)
	}
	botID, err := dbpkg.ParseUUID(pipelineTestBotID)
	if err != nil {
		t.Fatalf("parse bot id: %v", err)
	}
	sessionID, err := dbpkg.ParseUUID(pipelineTestSessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	return sqlc.BotHistoryMessageCompact{
		ID:            id,
		BotID:         botID,
		SessionID:     sessionID,
		Status:        "ok",
		Summary:       summary,
		Coverage:      coverage,
		AnchorStartMs: createdAtMs,
		AnchorEndMs:   createdAtMs,
	}
}

func pipelineTextEvent(messageID string, atMs int64, text string) timeline.MessageEvent {
	return timeline.MessageEvent{
		SessionID:    pipelineTestSessionID,
		MessageID:    messageID,
		ReceivedAtMs: atMs,
		TimestampSec: atMs / 1000,
		Content:      []timeline.ContentNode{{Type: "text", Text: text}},
		Conversation: timeline.ConversationMeta{Channel: "telegram", ConversationType: "group"},
	}
}

func TestBuildMessagesFromPipelineInsertsArtifactSummary(t *testing.T) {
	t.Parallel()

	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "old original"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, "current question"))

	svc := &Service{
		pipeline: pipeline,
		queries:  fakeArtifactLineageQueries{rows: []sqlc.BotHistoryMessageCompact{compactionLogRow(t, "compacted window", "m1", 1000)}},
		logger:   slog.New(slog.DiscardHandler),
	}

	messages := svc.buildMessagesFromPipeline(context.Background(), ChatRequest{
		BotID:    pipelineTestBotID,
		ThreadID: pipelineTestSessionID,
	}, 0)

	if len(messages) != 2 {
		t.Fatalf("expected summary + current message, got %d: %s", len(messages), messagesDebug(messages))
	}
	var summaryText string
	if err := json.Unmarshal(messages[0].Content, &summaryText); err != nil {
		t.Fatalf("decode summary content: %v", err)
	}
	if !strings.Contains(summaryText, "<summary>") || !strings.Contains(summaryText, "compacted window") {
		t.Fatalf("expected leading summary, got %q", summaryText)
	}
	joined := messagesDebug(messages)
	if strings.Contains(joined, "old original") {
		t.Fatalf("covered original must be replaced, got %s", joined)
	}
	if !strings.Contains(joined, "current question") {
		t.Fatalf("uncovered message must survive, got %s", joined)
	}
}

func messagesDebug(messages []ModelMessage) string {
	parts := make([]string, 0, len(messages))
	for _, m := range messages {
		parts = append(parts, m.Role+":"+string(m.Content))
	}
	return strings.Join(parts, "|")
}

func TestTrimPipelineMessagesKeepsPinnedSummaries(t *testing.T) {
	t.Parallel()

	messages := []ModelMessage{
		{Role: "user", Content: newTextContent("<summary>\nold window\n</summary>")},
		{Role: "user", Content: newTextContent(strings.Repeat("a", 400))},
		{Role: "assistant", Content: newTextContent(strings.Repeat("b", 400))},
		{Role: "user", Content: newTextContent("recent")},
	}
	pinned := []bool{true, false, false, false}

	trimmed := trimPipelineMessagesByTokens(nil, messages, pinned, 40)

	if len(trimmed) != 2 {
		t.Fatalf("expected pinned summary + recent, got %s", messagesDebug(trimmed))
	}
	if !strings.Contains(string(trimmed[0].Content), "old window") {
		t.Fatalf("pinned summary must survive trim, got %s", messagesDebug(trimmed))
	}
	if !strings.Contains(string(trimmed[1].Content), "recent") {
		t.Fatalf("recent message must survive trim, got %s", messagesDebug(trimmed))
	}
}

func TestBuildMessagesFromPipelineKeepsSummaryUnderBudget(t *testing.T) {
	t.Parallel()

	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "old original"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, strings.Repeat("x", 4000)))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m3", 3000, "current question"))

	svc := &Service{
		pipeline: pipeline,
		queries:  fakeArtifactLineageQueries{rows: []sqlc.BotHistoryMessageCompact{compactionLogRow(t, "compacted window", "m1", 1000)}},
		logger:   slog.New(slog.DiscardHandler),
	}

	messages := svc.buildMessagesFromPipeline(context.Background(), ChatRequest{
		BotID:    pipelineTestBotID,
		ThreadID: pipelineTestSessionID,
	}, 200)

	// Consecutive rendered segments merge into one user message, so the tail
	// here is a single oversized block: without pinning the budget would drop
	// everything and the model would receive no context at all.
	if len(messages) == 0 {
		t.Fatal("artifact summary must survive a budget that drops the whole tail")
	}
	joined := messagesDebug(messages)
	if !strings.Contains(joined, "compacted window") {
		t.Fatalf("artifact summary must survive the dropped prefix, got %s", joined)
	}
	if strings.Contains(joined, strings.Repeat("x", 4000)) {
		t.Fatalf("oversized tail should have been trimmed, got %d messages", len(messages))
	}
}

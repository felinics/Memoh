package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestDropPreflightFailureTurns(t *testing.T) {
	user := sdkMessagesToModelMessages([]sdk.Message{sdk.UserMessage("rejected")})[0]
	assistant := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("")})[0]
	kept := sdkMessagesToModelMessages([]sdk.Message{sdk.UserMessage("accepted")})[0]
	records := []historyfrag.HistoryRecord{
		{ModelMessage: user, Metadata: map[string]any{messagepkg.HistoryFailureOriginMetadataKey: messagepkg.HistoryFailureOriginUserMessageHook}},
		{ModelMessage: assistant, Metadata: map[string]any{messagepkg.HistoryFailureOriginMetadataKey: messagepkg.HistoryFailureOriginUserMessageHook}},
		{ModelMessage: kept},
	}

	got := dropPreflightFailureTurns(records)
	if len(got) != 1 || got[0].ModelMessage.TextContent() != "accepted" {
		t.Fatalf("preflight failure records = %#v, want only accepted input", got)
	}
}

func TestDropEmptyHistoryFailures(t *testing.T) {
	empty := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("")})[0]
	kept := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("hello")})[0]
	var stepFailure ModelMessage
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":[]}`), &stepFailure); err != nil {
		t.Fatal(err)
	}
	records := []historyfrag.HistoryRecord{
		{ModelMessage: empty, Metadata: map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "agent.response_timeout"}},
		{ModelMessage: stepFailure, Metadata: map[string]any{messagepkg.HistoryErrorCodeMetadataKey: "agent.provider_request_rejected", messagepkg.AgentStepInterruptedMetadataKey: true}},
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
				ProviderMetadata: partmeta.Fold(map[string]any{
					"anthropic": map[string]any{"redactedData": "BLOB"},
				}),
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
	meta := part.ProviderMetadata["anthropic"]
	if data := meta["redactedData"]; data != "BLOB" {
		t.Fatalf("redactedData = %q, want BLOB", data)
	}
}

func TestProjectInterruptedHistoryReasoningProjectsSupersededCheckpoint(t *testing.T) {
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
		t.Fatalf("projected content = %#v, want one text part", got.Content)
	}
	if text, ok := got.Content[0].(sdk.TextPart); !ok || !strings.Contains(text.Text, "partial reasoning") {
		t.Fatalf("superseded checkpoint must keep the projection it had while live: %#v", got.Content[0])
	}
}

const (
	pipelineTestBotID     = "11111111-1111-1111-1111-111111111111"
	pipelineTestSessionID = "22222222-2222-2222-2222-222222222222"
)

// pipelineCheckpointService composes the pipeline under checkpoints read from
// queries, whose kept rows messages serves.
func pipelineCheckpointService(pipeline *timeline.Pipeline, queries *historyCheckpointQueries, messages messagepkg.Service) *Service {
	logger := slog.New(slog.DiscardHandler)
	return &Service{
		pipeline:            pipeline,
		messageService:      messages,
		pipelineCheckpoints: timeline.NewCheckpointSource(logger, queries, messages),
		logger:              logger,
	}
}

// foldedSummaryQueries is a session whose checkpoint folds the inbound
// message m1 into one summary.
func foldedSummaryQueries(t *testing.T, summary string) *historyCheckpointQueries {
	t.Helper()
	queries := &historyCheckpointQueries{classified: []sqlc.ClassifyHistoryCheckpointMessagesRow{{ExternalMessageID: "m1"}}}
	queries.activate(historyCheckpointRow(t, checkpointTestID, historyTestPosition(0),
		checkpoint.Item{SummaryID: queries.summary(t, checkpointTestSummaryID, summary)}))
	return queries
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

func TestBuildMessagesFromPipelinePinsTheCheckpointSummary(t *testing.T) {
	t.Parallel()

	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "old original"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, "current question"))
	svc := pipelineCheckpointService(pipeline, foldedSummaryQueries(t, "compacted window"), nil)

	messages, pressure, _, err := svc.buildMessagesFromPipeline(context.Background(), ChatRequest{
		BotID:    pipelineTestBotID,
		ThreadID: pipelineTestSessionID,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	want := 0
	for _, message := range messages {
		want += estimateMessageTokens(message)
	}
	if pressure != want {
		t.Fatalf("pressure = %d, want summary and raw history %d", pressure, want)
	}
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
	if source := messages[0].ContextSource; source == nil || source.Kind != "summary" || source.ID != checkpointTestSummaryID {
		t.Fatalf("summary renders source %+v, want the checkpoint's summary", source)
	}
	joined := messagesDebug(messages)
	if strings.Contains(joined, "old original") {
		t.Fatalf("folded original must be replaced, got %s", joined)
	}
	if !strings.Contains(joined, "current question") {
		t.Fatalf("message after the checkpoint must survive, got %s", joined)
	}
}

// What the checkpoint keeps raw renders in its place among what it holds,
// before what came after it: a kept inbound message as its segment, then
// the summary, then the later inbound message and the turn responses after
// the checkpoint's position.
func TestBuildMessagesFromPipelinePinsWhatTheCheckpointHolds(t *testing.T) {
	t.Parallel()

	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "kept input"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, "folded question"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m3", 3000, "later question"))
	kept := persistedHistoryMessage(t, "kept-row", "user", "kept input")
	kept.ExternalMessageID = "m1"
	answer := persistedHistoryMessage(t, "answer", "assistant", "later answer")
	answer.CreatedAt = time.UnixMilli(4000)
	messages := &turnResponseSourceMessageService{kept: []messagepkg.Message{kept}, projected: []messagepkg.Message{answer}}
	queries := &historyCheckpointQueries{classified: []sqlc.ClassifyHistoryCheckpointMessagesRow{
		{ExternalMessageID: "m1", Kept: true}, {ExternalMessageID: "m2"}, {ExternalMessageID: "m3", Kept: true},
	}}
	position := messagepkg.HistoryPosition{Turn: 2, Seq: 2}
	queries.activate(historyCheckpointRow(t, checkpointTestID, position,
		checkpoint.Item{RowID: kept.ID}, checkpoint.Item{SummaryID: queries.summary(t, checkpointTestSummaryID, "folded work")}))
	svc := pipelineCheckpointService(pipeline, queries, messages)

	got, _, _, err := svc.buildMessagesFromPipeline(context.Background(), ChatRequest{BotID: pipelineTestBotID, ThreadID: pipelineTestSessionID}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(got))
	for _, message := range got {
		texts = append(texts, message.TextContent())
	}
	if want := []string{"kept input", turn.SummaryText("folded work"), "later question", "later answer"}; len(texts) != len(want) ||
		!strings.Contains(texts[0], want[0]) || texts[1] != want[1] || !strings.Contains(texts[2], want[2]) || texts[3] != want[3] {
		t.Fatalf("composed %q, want %q in order", texts, want)
	}
	if messages.after != position {
		t.Fatalf("turn responses load after %+v, want the checkpoint's position %+v", messages.after, position)
	}
}

func messagesDebug(messages []ModelMessage) string {
	parts := make([]string, 0, len(messages))
	for _, m := range messages {
		parts = append(parts, m.Role+":"+string(m.Content))
	}
	return strings.Join(parts, "|")
}

func TestBuildMessagesFromPipelineKeepsHistoryOverBudget(t *testing.T) {
	t.Parallel()

	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "old original"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m2", 2000, strings.Repeat("x", 4000)))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m3", 3000, "current question"))
	svc := pipelineCheckpointService(pipeline, foldedSummaryQueries(t, "compacted window"), nil)

	messages, pressure, _, err := svc.buildMessagesFromPipeline(context.Background(), ChatRequest{
		BotID:    pipelineTestBotID,
		ThreadID: pipelineTestSessionID,
	}, 200)
	if err != nil {
		t.Fatal(err)
	}

	if pressure < 1000 {
		t.Fatalf("pressure lost history over budget: %d", pressure)
	}
	joined := messagesDebug(messages)
	if !strings.Contains(joined, "compacted window") || !strings.Contains(joined, strings.Repeat("x", 4000)) || !strings.Contains(joined, "current question") {
		t.Fatalf("history over budget must reach provider admission whole, got %s", joined)
	}
}

func TestPipelineCurrentIdentitySurvivesLaterExternalAndSelfMessages(t *testing.T) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("old", 1000, strings.Repeat("h", 8000)))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("current", 2000, "required input"))
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("later", 3000, "later input"))
	echo := pipelineTextEvent("echo", 4000, "self echo")
	echo.IsSelfSent = true
	pipeline.PushEvent(pipelineTestSessionID, echo)
	svc := pipelineCheckpointService(pipeline, &historyCheckpointQueries{}, &turnResponseSourceMessageService{})
	messages, _, _, err := svc.buildMessagesFromPipeline(t.Context(), ChatRequest{ThreadID: pipelineTestSessionID, ExternalMessageID: "current"}, 500)
	if err != nil {
		t.Fatal(err)
	}
	index := latestModelUserMessageIndex(messages)
	if index == nil || !strings.Contains(messages[*index].TextContent(), "required input") {
		t.Fatalf("wrong current source: index=%v messages=%s", index, messagesDebug(messages))
	}
}

func TestBuildMessagesFromPipelineFailsWhenASourceCannotLoad(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"checkpoint", "history"} {
		t.Run(source, func(t *testing.T) {
			loadErr := errors.New(source + " unavailable")
			queries := &historyCheckpointQueries{}
			rows := &turnResponseSourceMessageService{}
			if source == "checkpoint" {
				queries.activeErr = loadErr
			} else {
				rows.err = loadErr
			}
			pipeline := timeline.NewPipeline(timeline.RenderParams{})
			pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "current question"))
			svc := pipelineCheckpointService(pipeline, queries, rows)
			messages, _, _, err := svc.buildMessagesFromPipeline(t.Context(), ChatRequest{BotID: pipelineTestBotID, ThreadID: pipelineTestSessionID}, 1000)
			if !errors.Is(err, loadErr) || messages != nil {
				t.Fatalf("messages=%s err=%v, want %v without composing", messagesDebug(messages), err, loadErr)
			}
		})
	}
}

// A pipeline load whose byte budget left turn responses out reports it, so
// the request folds them before it runs instead of starting its history
// later.
func TestBuildMessagesFromPipelineReportsATruncatedLoad(t *testing.T) {
	t.Parallel()
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	pipeline.PushEvent(pipelineTestSessionID, pipelineTextEvent("m1", 1000, "question"))
	answer := persistedHistoryMessage(t, "answer", "assistant", "newest answer")
	answer.CreatedAt = time.UnixMilli(2000)
	for _, truncated := range []bool{false, true} {
		messages := &turnResponseSourceMessageService{projected: []messagepkg.Message{answer}, truncated: truncated}
		svc := pipelineCheckpointService(pipeline, &historyCheckpointQueries{}, messages)
		_, _, got, err := svc.buildMessagesFromPipeline(t.Context(), ChatRequest{BotID: pipelineTestBotID, ThreadID: pipelineTestSessionID}, 1000)
		if err != nil || got != truncated {
			t.Fatalf("truncated=%v err=%v, want %v", got, err, truncated)
		}
	}
}

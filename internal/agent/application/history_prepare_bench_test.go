package application

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/toolexec"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/contextview"
)

// preparedHistoryRequest is what resolve derives from loaded history rows
// before the run config is refreshed: the rendered messages and everything
// a run compacting in place needs to name what each of them renders.
type preparedHistoryRequest struct {
	Messages        []sdk.Message
	Provenance      []native.MessageProvenance
	SourceIDs       []string
	TokenEstimates  []int
	Trimmable       int
	PressureTokens  int
	SummaryFragsLen int
	MemoryIndex     *int
}

// prepareHistoryRequest runs the pure part of resolve over stored rows, in
// resolve's order, so benchmarks and goldens exercise exactly that path.
func prepareHistoryRequest(tb testing.TB, rows []messagepkg.Message, query string) preparedHistoryRequest {
	tb.Helper()
	records := make([]historyfrag.HistoryRecord, 0, len(rows))
	for _, row := range rows {
		record, err := historyfrag.FromDBMessage(row, historyfrag.ScopeFallback{})
		if err != nil {
			tb.Fatal(err)
		}
		records = append(records, record)
	}
	records = pruneHistoryForGateway(records)
	records = dropPreflightFailureTurns(records)
	records = dropEmptyHistoryFailures(records)
	records = projectInterruptedHistoryReasoning(records)
	messages, records := historyMessagesFromRecords(records)
	pressure := estimateMessagesTokens(records)

	historyCount := len(messages)
	messages = append(messages, ModelMessage{Role: "user", Content: newTextContent("<memory-context>prefers short answers</memory-context>")})
	memoryIndex := intPointer(len(messages) - 1)
	messages = append(messages, ModelMessage{Role: "user", Content: newTextContent(query)})
	normalized := normalizeContext(messages)
	trimmable := normalized.prefixLength(historyCount)
	memoryIndex = normalized.index(memoryIndex)
	messages = normalized.models()
	estimates := make([]int, len(messages))
	for i := range messages {
		estimates[i] = estimateMessageTokens(messages[i])
	}
	frags := historyContextFragsForMessages(messages, records)
	sent := normalized.sent()
	return preparedHistoryRequest{
		Messages:        sent,
		Provenance:      contextProvenance(messages, sent, records, nil),
		SourceIDs:       historySourceMessageIDsForMessages(messages, records),
		TokenEstimates:  estimates,
		Trimmable:       min(trimmable, len(messages)),
		PressureTokens:  pressure,
		SummaryFragsLen: len(frags),
		MemoryIndex:     memoryIndex,
	}
}

// syntheticHistoryRows builds the shape of a long scheduled-task session:
// user prompts, assistant steps calling tools, and large nested JSON tool
// results, plus an unanswered call and a legacy tool_calls row so repair
// and compatibility paths run. Rows total roughly targetBytes of content.
func syntheticHistoryRows(tb testing.TB, turns int, targetBytes int) []messagepkg.Message {
	tb.Helper()
	perResult := max(256, targetBytes/max(1, turns))
	base := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	rows := make([]messagepkg.Message, 0, turns*3)
	add := func(role string, content json.RawMessage) {
		i := len(rows)
		rows = append(rows, messagepkg.Message{
			ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1),
			BotID:       "bot-1",
			SessionID:   "session-1",
			Role:        role,
			Content:     content,
			RawMetadata: json.RawMessage(`{"agent_step":` + strconv.Itoa(i) + `}`),
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}
	stored := func(message sdk.Message) json.RawMessage {
		raw, err := historyfrag.MarshalStoredSDKMessage(message)
		if err != nil {
			tb.Fatal(err)
		}
		return raw
	}
	for turn := 0; turn < turns; turn++ {
		add("user", stored(sdk.UserMessage(fmt.Sprintf("scheduled check #%d: report the dashboard status", turn))))
		callID := fmt.Sprintf("call-%d", turn)
		add("assistant", stored(sdk.Message{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.TextPart{Text: "Fetching the dashboard."},
				sdk.ToolCallPart{ToolCallID: callID, ToolName: "web_fetch", Input: toolexec.ArgumentsFromValue(map[string]any{"url": "https://example.com/dashboard", "page": turn})},
			},
		}))
		switch turn % 50 {
		case 7:
			// Interrupted step: the call never got a result.
		case 13:
			legacy, err := historyfrag.MarshalStoredModelMessage(ModelMessage{
				Role:      "assistant",
				Content:   newTextContent(""),
				ToolCalls: []ToolCall{{ID: callID + "-legacy", Type: "function", Function: turnpkg.ToolCallFunction{Name: "exec", Arguments: `{"command":"uptime"}`}}},
			})
			if err != nil {
				tb.Fatal(err)
			}
			add("assistant", legacy)
		default:
			add("tool", stored(sdk.ToolMessage(sdk.ToolResultPart{
				ToolCallID: callID,
				ToolName:   "web_fetch",
				Result:     toolexec.OutputFromValue(syntheticToolOutput(turn, perResult)),
			})))
		}
		add("assistant", stored(sdk.AssistantMessage(fmt.Sprintf("Dashboard #%d is healthy.", turn))))
	}
	return rows
}

func syntheticToolOutput(seed, bytes int) map[string]any {
	items := make([]any, 0, bytes/160+1)
	for i := 0; len(items)*160 < bytes; i++ {
		items = append(items, map[string]any{
			"id":     fmt.Sprintf("item-%d-%d", seed, i),
			"status": []string{"ok", "degraded", "down"}[i%3],
			"metrics": map[string]any{
				"latency_ms": i % 997,
				"errors":     []int{i % 7, i % 11, i % 13},
			},
			"note": strings.Repeat("x", 48),
		})
	}
	return map[string]any{"page": seed, "items": items}
}

func BenchmarkPrepareHistoryRequest(b *testing.B) {
	for _, size := range []struct {
		turns int
		bytes int
	}{
		{turns: 170, bytes: 2 << 20},
		{turns: 170, bytes: 8 << 20},
	} {
		rows := syntheticHistoryRows(b, size.turns, size.bytes)
		total := 0
		for _, row := range rows {
			total += len(row.Content)
		}
		b.Run(fmt.Sprintf("rows=%d/bytes=%dMB", len(rows), total>>20), func(b *testing.B) {
			b.SetBytes(int64(total))
			b.ReportAllocs()
			for b.Loop() {
				prepareHistoryRequest(b, rows, "scheduled check: report the dashboard status")
			}
		})
	}
}

// BenchmarkResolveHistoryRunConfig adds what resolve does with the prepared
// history before the first model call: the run config's context fragments
// are refreshed when resolve builds it and again when prompts are attached,
// and the provider view is applied.
func BenchmarkResolveHistoryRunConfig(b *testing.B) {
	rows := syntheticHistoryRows(b, 170, 8<<20)
	total := 0
	for _, row := range rows {
		total += len(row.Content)
	}
	applier := contextview.ProviderRunConfigApplier(nil)
	b.Run(fmt.Sprintf("rows=%d/bytes=%dMB", len(rows), total>>20), func(b *testing.B) {
		b.SetBytes(int64(total))
		b.ReportAllocs()
		for b.Loop() {
			prepared := prepareHistoryRequest(b, rows, "scheduled check: report the dashboard status")
			budget := 0
			for _, estimate := range prepared.TokenEstimates {
				budget += estimate
			}
			cfg := native.RunConfig{
				Messages:                     prepared.Messages,
				ContextHistoryTokenEstimates: prepared.TokenEstimates,
				ContextTrimmableMessages:     prepared.Trimmable,
				ContextMemoryMessageIndex:    prepared.MemoryIndex,
				ContextProvenance:            prepared.Provenance,
				ForkContextSourceMessageIDs:  prepared.SourceIDs,
				ContextBudgetMaxTokens:       2 * budget,
				ContextLifecycle:             contextfrag.NewLifecycleHolder(),
			}.RefreshContextFrag().RefreshContextFrag()
			if _, err := applier(b.Context(), cfg); err != nil {
				b.Fatal(err)
			}
		}
	})
}

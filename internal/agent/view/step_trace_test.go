package view

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestConvertMessagesToUITurnsProjectsStepTracesAndExecutionTiming(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 3, 1, 2, 5, 0, time.UTC)
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:          "assistant-1",
		Role:        "assistant",
		TurnID:      "turn-1",
		Content:     json.RawMessage(`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-1","toolName":"exec","input":{"command":"pwd"},"providerMetadata":{"execution_timing":{"started_at_ms":2000,"ended_at_ms":2600}}}]}`),
		RawMetadata: json.RawMessage(`{"step_trace":{"version":1,"step_index":0,"started_at_ms":1000,"first_token_at_ms":1200,"ended_at_ms":1900,"finish_reason":"tool-calls","usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":12}}}`),
		CreatedAt:   createdAt,
	}, {
		ID:        "tool-1",
		Role:      "tool",
		TurnID:    "turn-1",
		Content:   json.RawMessage(`{"role":"tool","content":[{"type":"tool-result","toolCallId":"call-1","toolName":"exec","result":"/home"}]}`),
		CreatedAt: createdAt.Add(time.Second),
	}, {
		ID:          "assistant-2",
		Role:        "assistant",
		TurnID:      "turn-1",
		Content:     json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"done"}]}`),
		RawMetadata: json.RawMessage(`{"step_trace":{"version":1,"step_index":1,"started_at_ms":3000,"first_token_at_ms":3100,"ended_at_ms":3500,"finish_reason":"stop","usage":{"input_tokens":130,"output_tokens":4}}}`),
		CreatedAt:   createdAt.Add(2 * time.Second),
	}})
	if len(turns) != 1 || len(turns[0].Messages) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	tool := turns[0].Messages[0]
	if tool.Type != UIMessageTool || tool.ExecutionTiming == nil || tool.ExecutionTiming.StartedAtMS != 2000 || tool.ExecutionTiming.EndedAtMS != 2600 {
		t.Fatalf("tool block = %#v", tool)
	}
	traces := turns[0].StepTraces
	if len(traces) != 2 {
		t.Fatalf("step traces = %#v, want two", traces)
	}
	if traces[0].FirstMessageID != tool.ID || traces[0].LastMessageID != tool.ID || traces[0].StepIndex != 0 || traces[0].StartedAtMS != 1000 || traces[0].FirstTokenAtMS != 1200 || traces[0].EndedAtMS != 1900 || traces[0].FinishReason != "tool-calls" {
		t.Fatalf("first trace = %#v", traces[0])
	}
	if traces[0].Usage == nil || traces[0].Usage.InputTokens != 100 || traces[0].Usage.CachedInputTokens != 60 || traces[0].Usage.OutputTokens != 12 {
		t.Fatalf("first trace usage = %#v", traces[0].Usage)
	}
	if traces[1].FirstMessageID != turns[0].Messages[1].ID || traces[1].LastMessageID != turns[0].Messages[1].ID || traces[1].StepIndex != 1 || traces[1].FinishReason != "stop" {
		t.Fatalf("second trace = %#v", traces[1])
	}
}

func TestUIMessageStreamConverterAnchorsStepTracesToFirstBlock(t *testing.T) {
	t.Parallel()

	converter := NewUIMessageStreamConverter()
	converter.HandleStepStart()
	converter.HandleEvent(UIMessageStreamEvent{Type: "reasoning_delta", Delta: "think"})
	converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "hi"})
	usage, _ := json.Marshal(map[string]any{"inputTokens": 50, "outputTokens": 3, "cachedInputTokens": 20})
	first := converter.HandleStepEnd(UIMessageStreamEvent{
		Type:         "step_end",
		FinishReason: "tool-calls",
		StepIndex:    0,
		Usage:        usage,
		Timing:       &event.StepTiming{StartedAtMS: 1000, FirstTokenAtMS: 1100, EndedAtMS: 1800},
	})
	if first == nil || first.FirstMessageID != 0 || first.LastMessageID != 1 || first.StepIndex != 0 || first.StartedAtMS != 1000 || first.EndedAtMS != 1800 || first.FinishReason != "tool-calls" {
		t.Fatalf("first trace = %#v", first)
	}
	if first.Usage == nil || first.Usage.InputTokens != 50 || first.Usage.CachedInputTokens != 20 || first.Usage.OutputTokens != 3 {
		t.Fatalf("first trace usage = %#v", first.Usage)
	}

	converter.HandleStepStart()
	toolMessages := converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_start", ToolCallID: "call-1", ToolName: "exec"})
	ended := converter.HandleEvent(UIMessageStreamEvent{
		Type: "tool_call_end", ToolCallID: "call-1", ToolName: "exec", Output: "ok",
		Metadata: map[string]any{event.ExecutionTimingMetadataKey: event.ExecutionTiming{StartedAtMS: 1900, EndedAtMS: 2400}},
	})
	if len(ended) != 1 || ended[0].ExecutionTiming == nil || ended[0].ExecutionTiming.StartedAtMS != 1900 || ended[0].ExecutionTiming.EndedAtMS != 2400 {
		t.Fatalf("tool end = %#v", ended)
	}
	second := converter.HandleStepEnd(UIMessageStreamEvent{Type: "step_end", StepIndex: 1, Timing: &event.StepTiming{StartedAtMS: 1850, EndedAtMS: 1890}})
	if second == nil || second.FirstMessageID != toolMessages[0].ID || second.LastMessageID != toolMessages[0].ID || second.StepIndex != 1 {
		t.Fatalf("second trace = %#v", second)
	}

	converter.HandleStepStart()
	if empty := converter.HandleStepEnd(UIMessageStreamEvent{Type: "step_end", StepIndex: 2, Timing: &event.StepTiming{StartedAtMS: 1, EndedAtMS: 2}}); empty != nil {
		t.Fatalf("a step without blocks must not anchor a trace: %#v", empty)
	}
}

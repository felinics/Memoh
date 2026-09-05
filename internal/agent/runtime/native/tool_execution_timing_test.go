package native

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/event"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestToolExecutionMetadataRegistryRecordsExecutionTiming(t *testing.T) {
	t.Parallel()

	fake := &stepClockTestTime{now: time.UnixMilli(10_000)}
	registry := newToolExecutionMetadataRegistry(nil)
	registry.now = fake.read
	wrapped := registry.wrapExecute([]sdk.Tool{{
		Name: "exec",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			fake.advance(1200 * time.Millisecond)
			return "ok", nil
		},
	}})

	out, err := wrapped[0].Execute(&sdk.ToolExecContext{Context: context.Background(), ToolCallID: "call-1", ToolName: "exec"}, nil)
	if err != nil || out != "ok" {
		t.Fatalf("execute = %v, %v", out, err)
	}
	timing, ok := registry.metadata("call-1")[event.ExecutionTimingMetadataKey].(event.ExecutionTiming)
	if !ok || timing.StartedAtMS != 10_000 || timing.EndedAtMS != 11_200 {
		t.Fatalf("execution timing = %#v", registry.metadata("call-1"))
	}

	annotated := registry.annotate([]sdk.Message{{
		Role:    sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "exec"}},
	}})
	call := annotated[0].Content[0].(sdk.ToolCallPart)
	if call.ProviderMetadata[event.ExecutionTimingMetadataKey] != timing {
		t.Fatalf("annotated provider metadata = %#v", call.ProviderMetadata)
	}
	encoded, err := json.Marshal(call.ProviderMetadata)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `{"execution_timing":{"started_at_ms":10000,"ended_at_ms":11200}}` {
		t.Fatalf("serialized metadata = %s", encoded)
	}
}

func TestAgentStreamToolCallEndCarriesExecutionTiming(t *testing.T) {
	t.Parallel()

	calls := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		if calls == 1 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "echo", Input: map[string]any{}},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		}
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop},
		), nil
	})
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "echo",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			time.Sleep(5 * time.Millisecond)
			return "ok", nil
		},
	}}}})

	var toolEnd, terminal StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
	}) {
		switch ev.Type {
		case EventToolCallEnd:
			toolEnd = ev
		case EventAgentEnd:
			terminal = ev
		}
	}

	timing, ok := toolEnd.Metadata[event.ExecutionTimingMetadataKey].(event.ExecutionTiming)
	if !ok || timing.StartedAtMS == 0 || timing.EndedAtMS < timing.StartedAtMS+5 {
		t.Fatalf("tool_call_end metadata = %#v", toolEnd.Metadata)
	}
	var messages []sdk.Message
	if err := json.Unmarshal(terminal.Messages, &messages); err != nil {
		t.Fatalf("decode terminal messages: %v", err)
	}
	var found bool
	for _, message := range messages {
		for _, part := range message.Content {
			call, ok := part.(sdk.ToolCallPart)
			if !ok {
				continue
			}
			raw, ok := call.ProviderMetadata[event.ExecutionTimingMetadataKey].(map[string]any)
			if !ok || raw["started_at_ms"] != float64(timing.StartedAtMS) || raw["ended_at_ms"] != float64(timing.EndedAtMS) {
				t.Fatalf("persisted tool call metadata = %#v", call.ProviderMetadata)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("terminal messages carry no tool call: %s", terminal.Messages)
	}
}

func TestAgentStreamRetryPathToolCallEndCarriesExecutionTiming(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", "api error 429: engine overloaded"),
		scriptToolCall("call-retry", "echo"),
		scriptText("done"),
	)
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "echo",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			time.Sleep(5 * time.Millisecond)
			return "ok", nil
		},
	}}}})
	cfg := retryLoopTestConfig(provider, fastRetry)
	cfg.SupportsToolCall = true

	var toolEnd StreamEvent
	for ev := range a.Stream(context.Background(), cfg) {
		if ev.Type == EventToolCallEnd && ev.ToolCallID == "call-retry" {
			toolEnd = ev
		}
	}
	if invocations.Load() < 2 {
		t.Fatalf("provider invocations = %d, want the retry to run", invocations.Load())
	}
	timing, ok := toolEnd.Metadata[event.ExecutionTimingMetadataKey].(event.ExecutionTiming)
	if !ok || timing.StartedAtMS == 0 || timing.EndedAtMS < timing.StartedAtMS+5 {
		t.Fatalf("retried tool_call_end metadata = %#v", toolEnd.Metadata)
	}
}

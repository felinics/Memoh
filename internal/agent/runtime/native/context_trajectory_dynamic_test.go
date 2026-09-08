package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type failingTrajectoryHookProvider struct{}

func (failingTrajectoryHookProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, errors.New("HOOK_SOURCE_FAILED")
}

func TestTrajectoryRecordsFailedHookWithoutProviderRequest(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	cfg := RunConfig{RunID: "run", ContextLifecycle: holder, Identity: SessionContext{BotID: "bot", SessionID: "session"}}
	agent := New(Deps{HookService: hooks.NewService(nil, failingTrajectoryHookProvider{})})
	if _, err := agent.applyBeforeModelCallHook(cfg.TrajectoryContext(t.Context()), cfg, 0); err == nil {
		t.Fatal("fixture hook did not fail")
	}
	if len(sink.events) != 1 || sink.events[0].Stage != "before_model_hook" {
		t.Fatalf("failed hook disappeared: %#v", sink.events)
	}
	blocks := sink.events[0].Blocks
	body := sink.contents[blocks[len(blocks)-1].Chunks[0]]
	if !strings.Contains(body, "HOOK_SOURCE_FAILED") || !strings.Contains(body, `"applied":false`) {
		t.Fatalf("failed hook represented as applied: %s", body)
	}
}

func TestTrajectoryRecordsInitialAndStepHookApplication(t *testing.T) {
	bridgeProvider, hookService := newBeforeModelCallHook(t, "HOOK_SOURCE_BODY")
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	agent := New(Deps{BridgeProvider: bridgeProvider, HookService: hookService})
	agent.SetToolProviders(mockToolLoopTools())
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "hook-call", ToolName: "lookup", Input: map[string]any{"query": "one"}}}}, nil
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	_, err := agent.Generate(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("task")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var steps []int
	for _, event := range sink.events {
		if event.Stage != "before_model_hook" {
			continue
		}
		steps = append(steps, *event.StepIndex)
		block := event.Blocks[len(event.Blocks)-1]
		body := sink.contents[block.Chunks[0]]
		if !strings.Contains(body, "HOOK_SOURCE_BODY") || !strings.Contains(body, `"applied":true`) {
			t.Fatalf("hook source/application missing: %s", body)
		}
	}
	if len(steps) != 2 || steps[0] != 0 || steps[1] != 1 {
		t.Fatalf("hook steps = %v", steps)
	}
}

func TestTrajectoryRetainsPreLimitToolOutput(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	marker := "UNIQUE_MIDDLE_BEFORE_TOOL_LIMIT"
	original := strings.Repeat("left ", 1000) + marker + strings.Repeat(" right", 1000)
	calls := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		if calls == 1 {
			return closedAgentTestStream(&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "limited-call", ToolName: "echo", Input: map[string]any{}},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls}), nil
		}
		return closedAgentTestStream(&sdk.StartStepPart{}, &sdk.TextDeltaPart{ID: "answer", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	agent := New(Deps{Limits: Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80}})
	agent.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name: "echo", Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) { return original, nil },
	}}}})
	for event := range agent.Stream(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider},
		Messages: []sdk.Message{sdk.UserMessage("read complete tool output")},
	}) {
		if event.Type == EventError {
			t.Fatalf("unexpected stream error: %s", event.Error)
		}
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want 2", calls)
	}
	for _, content := range sink.contents {
		if strings.Contains(content, marker) {
			return
		}
	}
	t.Fatalf("raw tool output marker absent from all %d captured stages; only limited output survives", len(sink.events))
}

func TestTrajectoryRetryStepMatchesRecoveredOutput(t *testing.T) {
	sink := &nativeTrajectorySink{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetTrajectoryRecorder(trajectory.NewRecorder(sink))
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "retry-call", ToolName: "lookup", Input: map[string]any{"query": "one"}}}}, nil
		case 2:
			return nil, errors.New("api error 429: fixture overloaded")
		default:
			return &sdk.GenerateResult{Text: "recovered", FinishReason: sdk.FinishReasonStop}, nil
		}
	}}
	agent := New(Deps{})
	agent.SetToolProviders(mockToolLoopTools())
	var ends []StreamEvent
	for event := range agent.Stream(t.Context(), RunConfig{
		RunID: "run", ContextLifecycle: holder, SupportsToolCall: true,
		Identity: SessionContext{BotID: "bot", SessionID: "session"},
		Model:    &sdk.Model{ID: "fixture", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("task")}, Retry: fastRetry,
	}) {
		if event.Type == EventStepEnd {
			ends = append(ends, event)
		}
	}
	inputs := sink.providerInputs()
	if len(inputs) != 3 || len(ends) != 2 {
		t.Fatalf("fixture calls=%d ends=%d, want 3/2", len(inputs), len(ends))
	}
	capturedStep := -1
	if inputs[2].StepIndex != nil {
		capturedStep = *inputs[2].StepIndex
	}
	if capturedStep != ends[1].StepIndex {
		t.Fatalf("recovered request capture step=%d, output step=%d; capture has no attempt offset", capturedStep, ends[1].StepIndex)
	}
}

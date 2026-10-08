package native

import (
	"context"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// deferredApprovalBatch drives one model step that emits an unguarded tool call
// followed by a call the approval handler defers. A deferred batch executes
// nothing: the step record parks every call open; the decision path executes
// the approved call when it resumes the run and closes the others with
// synthetic error results.
func deferredApprovalBatch(t *testing.T) (*Agent, *atomicMockProvider, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	provider := &atomicMockProvider{
		handler: func(int, sdk.Request) (sdk.ModelResult, error) {
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{
					{ToolCallID: "call-search", ToolName: "web_search", Input: toolexec.ArgumentsFromValue(map[string]any{"q": "one"})},
					{ToolCallID: "call-exec", ToolName: "exec", Input: toolexec.ArgumentsFromValue(map[string]any{"cmd": "ls"})},
				},
			}, nil
		},
	}

	var searchRuns, execRuns atomic.Int32
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{
				{
					Name:       "web_search",
					Parameters: &jsonschema.Schema{Type: "object"},
					Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
						searchRuns.Add(1)
						return toolexec.OutputFromValue(map[string]any{"hits": 3}), nil
					},
				},
				{
					Name:       "exec",
					Parameters: &jsonschema.Schema{Type: "object"},
					Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
						execRuns.Add(1)
						return toolexec.OutputFromValue(map[string]any{"stdout": "ok"}), nil
					},
				},
			},
		},
	})
	return a, provider, &searchRuns, &execRuns
}

func deferOnExec(_ context.Context, call sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
	if call.ToolName != "exec" {
		return toolexec.ToolApprovalResult{Decision: toolexec.ToolApprovalDecisionApproved}, nil
	}
	return toolexec.ToolApprovalResult{
		Decision:   toolexec.ToolApprovalDecisionDeferred,
		ApprovalID: "approval-1",
		Metadata:   map[string]any{"tool_call_id": call.ToolCallID},
	}, nil
}

func assertDeferredStepExecutesNothing(t *testing.T, record *step.Record, searchRuns, execRuns *atomic.Int32) {
	t.Helper()

	if record == nil {
		t.Fatal("no step record was committed")
	}
	if record.Deferred == nil {
		t.Fatal("record.Deferred = nil, want the parked approval")
	}
	if got := searchRuns.Load(); got != 0 {
		t.Fatalf("web_search executions = %d, want 0 while the batch is parked", got)
	}
	if got := execRuns.Load(); got != 0 {
		t.Fatalf("exec executions = %d, want 0 while the approval is parked", got)
	}

	if len(record.ToolResults) != 0 {
		t.Fatalf("record.ToolResults = %#v, want none for a parked batch", record.ToolResults)
	}

	var results []sdk.ToolResultPart
	var calls []sdk.ToolCallPart
	for _, msg := range record.Messages {
		for _, part := range msg.Content {
			switch p := part.(type) {
			case sdk.ToolResultPart:
				results = append(results, p)
			case sdk.ToolCallPart:
				calls = append(calls, p)
			}
		}
	}
	if len(calls) != 2 {
		t.Fatalf("persisted tool calls = %d, want 2", len(calls))
	}
	// Both calls stay dangling until the approval resolves and the resume
	// path executes the batch and appends its results.
	if len(results) != 0 {
		t.Fatalf("persisted tool results = %#v, want none", results)
	}
}

func TestAgentGenerateDeferredStepExecutesNothing(t *testing.T) {
	t.Parallel()

	a, provider, searchRuns, execRuns := deferredApprovalBatch(t)
	var record *step.Record
	result, err := a.Generate(context.Background(), RunConfig{
		Model:               &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:            []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall:    true,
		Identity:            SessionContext{BotID: "bot-1"},
		ToolApprovalHandler: deferOnExec,
		OnStepCommitted: func(_ context.Context, _ int, sr *step.Record) (StepDirective, error) {
			record = sr
			return StepDirective{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	assertDeferredStepExecutesNothing(t, record, searchRuns, execRuns)
	// The non-streaming result carries the parked decision on the deferred
	// call, like the stream's terminal messages, so a caller can render the
	// pending approval without re-reading the persisted step.
	annotated := false
	for _, msg := range result.Messages {
		for _, part := range msg.Content {
			if call, ok := part.(sdk.ToolCallPart); ok && call.ToolCallID == "call-exec" {
				approval, ok := partmeta.Object(call.ProviderMetadata, partmeta.KeyApproval)
				annotated = ok && approval["approval_id"] == "approval-1"
			}
		}
	}
	if !annotated {
		t.Fatalf("Generate() messages lack the deferred approval annotation: %#v", result.Messages)
	}
}

func TestAgentStreamDeferredStepExecutesNothing(t *testing.T) {
	t.Parallel()

	a, provider, searchRuns, execRuns := deferredApprovalBatch(t)
	var record *step.Record
	for range a.Stream(context.Background(), RunConfig{
		Model:               &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:            []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall:    true,
		Identity:            SessionContext{BotID: "bot-1"},
		ToolApprovalHandler: deferOnExec,
		OnStepCommitted: func(_ context.Context, _ int, sr *step.Record) (StepDirective, error) {
			record = sr
			return StepDirective{}, nil
		},
	}) {
	}
	assertDeferredStepExecutesNothing(t, record, searchRuns, execRuns)
}

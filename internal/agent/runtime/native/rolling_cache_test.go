package native

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
)

// Each call of an Anthropic run marks the end of its own request, so the next
// call reads all of it from cache: the mark moves with the steps and the
// request never holds more than Anthropic's four breakpoints.
func TestEachCallMarksTheEndOfItsRequest(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			var lastMarked []int
			next := func(params sdk.Request) (sdk.ModelResult, error) {
				marks, last := 0, -1
				for _, tool := range params.Tools {
					if tool.CacheControl != nil {
						marks++
					}
				}
				for i, msg := range params.Messages {
					if !reflect.DeepEqual(msg.Content, models.WithoutCacheControl(msg.Content)) {
						marks, last = marks+1, i
					}
				}
				if marks > 4 {
					t.Errorf("call %d carries %d breakpoints", len(lastMarked)+1, marks)
				}
				if last != len(params.Messages)-1 {
					t.Errorf("call %d: last marked message %d, want its last message %d", len(lastMarked)+1, last, len(params.Messages)-1)
				}
				lastMarked = append(lastMarked, last)
				if len(lastMarked) < 3 {
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-" + strconv.Itoa(len(lastMarked)), ToolName: "lookup", Input: toolexec.ArgumentsFromValue(map[string]any{}),
					}}}, nil
				}
				return sdk.ModelResult{FinishReason: sdk.FinishReasonStop, Text: "done"}, nil
			}
			mock := &atomicMockProvider{handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) { return next(params) }}
			mock.stream = func(_ context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
				result, err := next(params)
				if err != nil {
					return nil, err
				}
				var parts []sdk.StreamPart
				for _, call := range result.ToolCalls {
					parts = append(parts, &sdk.StreamToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: call.Input})
				}
				if result.Text != "" {
					parts = append(parts, &sdk.TextDeltaPart{Text: result.Text})
				}
				return closedAgentTestStream(append(parts, &sdk.FinishStepPart{FinishReason: result.FinishReason})...), nil
			}
			a := New(Deps{})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
				Name: "lookup", Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
				},
			}}}})
			cfg := RunConfig{
				Model:            &sdk.Model{ID: "claude-test", Provider: anthropicNamedProvider{mock}},
				Messages:         []sdk.Message{sdk.UserMessage("look it up")},
				System:           "base system",
				PromptCacheTTL:   models.PromptCacheTTL5m,
				SupportsToolCall: true,
				Identity:         SessionContext{BotID: "bot-1", SessionID: "session-1"},
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if streaming {
				for event := range a.Stream(ctx, cfg) {
					if event.Type == EventError {
						t.Error(event.Error)
					}
				}
			} else if _, err := a.Generate(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			// system, user | + tool call, tool result | + tool call, tool result
			if want := []int{1, 3, 5}; !reflect.DeepEqual(lastMarked, want) {
				t.Fatalf("last marked message per call = %v, want %v", lastMarked, want)
			}
		})
	}
}

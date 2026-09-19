package native

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

type capabilityRefreshProvider struct {
	installed bool
	used      int
}

func (p *capabilityRefreshProvider) Tools(_ context.Context, session tools.SessionContext) ([]sdk.Tool, error) {
	result := []sdk.Tool{{Name: "install_test_capability", Parameters: map[string]any{"type": "object"}, Execute: func(*sdk.ToolExecContext, any) (any, error) {
		p.installed = true
		session.CapabilitiesChanged()
		return map[string]any{"installed": true}, nil
	}}}
	if p.installed {
		result = append(result, sdk.Tool{Name: "new_capability", Parameters: map[string]any{"type": "object"}, Execute: func(*sdk.ToolExecContext, any) (any, error) { p.used++; return "worked", nil }})
	}
	return result, nil
}

func TestCapabilityChangeRefreshesExecutableToolsInSameRun(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(strconv.FormatBool(streaming), func(t *testing.T) {
			capability := &capabilityRefreshProvider{}
			a := New(Deps{})
			a.SetToolProviders([]tools.ToolProvider{capability})
			calls := 0
			next := func(params sdk.GenerateParams) (*sdk.GenerateResult, error) {
				calls++
				switch calls {
				case 1:
					return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "install", ToolName: "install_test_capability", Input: map[string]any{}}}}, nil
				case 2:
					found := false
					for _, tool := range params.Tools {
						if tool.Name == "new_capability" {
							found = true
						}
					}
					if !found {
						t.Error("new tool missing from model definitions")
					}
					if _, ok := findToolResult(params.Messages, "install_test_capability"); !ok {
						t.Error("installation result was not carried forward")
					}
					return &sdk.GenerateResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "use", ToolName: "new_capability", Input: map[string]any{}}}}, nil
				default:
					return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop, Text: "done"}, nil
				}
			}
			cfg := RunConfig{SupportsToolCall: true, Messages: []sdk.Message{sdk.UserMessage("install and use")}}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var committed []int
			cfg.OnStepCommitted = func(_ context.Context, index int, _ *sdk.StepResult) error {
				committed = append(committed, index)
				return nil
			}
			if streaming {
				cfg.Model = &sdk.Model{ID: "test", Provider: agentStreamTestProvider(func(_ context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
					result, err := next(params)
					if err != nil {
						return nil, err
					}
					parts := []sdk.StreamPart{}
					for _, call := range result.ToolCalls {
						parts = append(parts, &sdk.StreamToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: call.Input})
					}
					if result.Text != "" {
						parts = append(parts, &sdk.TextDeltaPart{Text: result.Text})
					}
					parts = append(parts, &sdk.FinishStepPart{FinishReason: result.FinishReason})
					return closedAgentTestStream(parts...), nil
				})}
				for event := range a.Stream(ctx, cfg) {
					if event.Type == EventError {
						t.Error(event.Error)
					}
				}
			} else {
				cfg.Model = &sdk.Model{ID: "test", Provider: &atomicMockProvider{handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) { return next(params) }}}
				if _, err := a.Generate(ctx, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if capability.used != 1 || calls != 3 {
				t.Fatalf("used=%d calls=%d", capability.used, calls)
			}
			if fmt.Sprint(committed) != "[0 1 2]" {
				t.Fatalf("step commits=%v", committed)
			}
		})
	}
}

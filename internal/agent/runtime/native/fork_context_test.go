package native

import (
	"context"
	"fmt"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

type forkSnapshotToolProvider struct {
	mu        sync.Mutex
	snapshots [][]sdk.Message
}

func (p *forkSnapshotToolProvider) Tools(_ context.Context, session agenttools.SessionContext) ([]sdk.Tool, error) {
	return []sdk.Tool{{
		Name:       "capture_fork_context",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			messages, err := session.ForkContext.Messages()
			if err != nil {
				return nil, err
			}
			p.mu.Lock()
			p.snapshots = append(p.snapshots, messages)
			p.mu.Unlock()
			return map[string]any{"captured": len(messages)}, nil
		},
	}}, nil
}

func TestForkContextTracksMessagesBeforeEachToolCallingStep(t *testing.T) {
	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call <= 2 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("capture-call-%d", call),
						ToolName:   "capture_fork_context",
						Input:      map[string]any{},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	capture := &forkSnapshotToolProvider{}
	agent := New(Deps{})
	agent.SetToolProviders([]agenttools.ToolProvider{capture})

	_, err := agent.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("parent-visible message")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1", SessionID: "session-1"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.snapshots) != 2 {
		t.Fatalf("expected two captured snapshots, got %d", len(capture.snapshots))
	}
	if len(capture.snapshots[0]) != 1 || capture.snapshots[0][0].Role != sdk.MessageRoleUser {
		t.Fatalf("first tool must see context before its own assistant tool call, got %+v", capture.snapshots[0])
	}
	if len(capture.snapshots[1]) < 3 {
		t.Fatalf("second tool must see the prior tool call and result, got %+v", capture.snapshots[1])
	}
	if capture.snapshots[1][0].Role != sdk.MessageRoleUser {
		t.Fatalf("second snapshot lost the parent prefix: %+v", capture.snapshots[1])
	}
}

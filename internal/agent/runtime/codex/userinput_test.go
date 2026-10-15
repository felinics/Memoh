package codex

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/runtimefence"
)

type rejectedUserInput struct {
	userinput.FlowService
	requests int
}

type subagentConsentFlow struct {
	closingApprovalFlow
	fence runtimefence.Fence
}

func (s *subagentConsentFlow) CreatePending(ctx context.Context, input approval.CreatePendingInput) (approval.Request, error) {
	s.fence, _ = runtimefence.FromContext(ctx)
	return s.closingApprovalFlow.CreatePending(ctx, input)
}

func (*subagentConsentFlow) WaitForDecision(context.Context, string) (approval.Request, error) {
	return approval.Request{Status: approval.StatusApproved, DecidedByUser: true}, nil
}

func TestSubagentMCPConsentPreservesExecutionApproval(t *testing.T) {
	svc := &subagentConsentFlow{}
	fence := runtimefence.Fence{BotID: "bot", SessionID: "child", Token: 7}
	turn := newTurnState(runtimefence.WithContext(t.Context(), fence), external.PromptInput{
		BotID: "bot", ThreadID: "child", RunID: "child-run",
		SessionMode: sessionmode.Subagent, CanRequestUserInput: true,
		Sink: external.EventSinkFunc(func(event.StreamEvent) {}),
	}, "native-child", svc, nil, nil, nil, slog.Default())
	defer turn.close()
	response := turn.runElicitation(turn.ctx, &protocol.McpServerElicitationRequestParams{
		Tag: protocol.McpServerElicitationRequestParamsTagForm,
		Form: &protocol.FormMcpServerElicitationRequestParams{Message: "Allow tool?", Meta: map[string]any{
			"codex_approval_kind": "mcp_tool_call",
		}},
	})
	if response.Action != protocol.McpServerElicitationActionAccept {
		t.Fatalf("child execution approval was not honored: %+v", response)
	}
	if svc.req.SessionID != "child" || svc.fence != fence {
		t.Fatalf("approval lost child identity: request=%+v fence=%+v", svc.req, svc.fence)
	}
}

func (s *rejectedUserInput) CreatePending(context.Context, userinput.CreatePendingInput) (userinput.Request, error) {
	s.requests++
	return userinput.Request{}, errors.New("test refuses to open a user question")
}

func TestSubagentCannotRequestUserInput(t *testing.T) {
	for _, kind := range []string{"question", "form", "url"} {
		t.Run(kind, func(t *testing.T) {
			svc := &rejectedUserInput{}
			turn := newTurnState(t.Context(), external.PromptInput{
				BotID: "bot", ThreadID: "child", RunID: "child-run",
				SessionMode: sessionmode.Subagent, CanRequestUserInput: true,
			}, "native-child", nil, nil, svc, nil, slog.Default())
			defer turn.close()
			switch kind {
			case "question":
				response := turn.requestUserInput(t.Context(), &protocol.ToolRequestUserInputParams{
					ItemID: "ask", Questions: []protocol.ToolRequestUserInputQuestion{{ID: "q", Question: "Choose?"}},
				})
				if len(response.Answers) != 0 {
					t.Fatalf("child question was answered: %+v", response)
				}
			case "form":
				response := turn.runElicitation(t.Context(), &protocol.McpServerElicitationRequestParams{
					Tag: protocol.McpServerElicitationRequestParamsTagForm,
					Form: &protocol.FormMcpServerElicitationRequestParams{Message: "Value?", RequestedSchema: protocol.McpElicitationSchema{
						Type: protocol.McpElicitationObjectTypeObject, Properties: map[string]protocol.McpElicitationPrimitiveSchema{"value": json.RawMessage(`{"type":"string"}`)},
					}},
				})
				if response.Action != protocol.McpServerElicitationActionDecline {
					t.Fatalf("child form was not declined: %+v", response)
				}
			case "url":
				response := turn.runElicitation(t.Context(), &protocol.McpServerElicitationRequestParams{
					Tag: protocol.McpServerElicitationRequestParamsTagURL,
					URL: &protocol.URLMcpServerElicitationRequestParams{Message: "Confirm?", URL: "https://example.com"},
				})
				if response.Action != protocol.McpServerElicitationActionDecline {
					t.Fatalf("child URL was not declined: %+v", response)
				}
			}
			if svc.requests != 0 {
				t.Fatalf("child opened %d user questions", svc.requests)
			}
		})
	}
}

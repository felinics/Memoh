package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAgentStreamStampsInjectedSteeringMessages(t *testing.T) {
	t.Parallel()

	injectCh := make(chan InjectMessage, 1)
	provider := &atomicMockProvider{handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			injectCh <- InjectMessage{Text: "<message>stop</message>"}
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-1", ToolName: "lookup"}},
			}, nil
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}}}})

	var committed [][]sdk.Message
	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		InjectCh:         injectCh,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, step.Messages)
			return nil
		},
	}) {
	}

	if len(committed) != 2 {
		t.Fatalf("committed steps = %d, want 2", len(committed))
	}
	first := committed[1][0]
	if first.Role != sdk.MessageRoleUser {
		t.Fatalf("second step leads with %q, want the injected user message", first.Role)
	}
	part, ok := first.Content[0].(sdk.TextPart)
	if !ok {
		t.Fatalf("injected message part = %#v", first.Content[0])
	}
	if kind := event.ContextInjectionKind(part.ProviderMetadata); kind != event.ContextInjectionSteering {
		t.Fatalf("injected message stamp = %q, want steering (%#v)", kind, part.ProviderMetadata)
	}
}

package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	native "github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/step"
)

func TestFinalSteerSurvivesProductionContextCompilationAndStepCapture(t *testing.T) {
	var indexes []int
	var captured [][]sdk.Message
	provider := &envelopeProbeProvider{handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
		if call == 2 {
			var text strings.Builder
			for _, message := range params.Messages {
				if message.Role == sdk.MessageRoleUser {
					text.Reset()
					for _, part := range message.Content {
						if p, ok := part.(sdk.TextPart); ok {
							text.WriteString(p.Text)
						}
					}
				}
			}
			if !strings.Contains(text.String(), "new direction") {
				t.Errorf("typed context dropped the steer: %q", text.String())
			}
		}
		return sdk.ModelResult{Text: "answer", FinishReason: sdk.FinishReasonStop}, nil
	}}
	cfg := native.RunConfig{
		Model: &sdk.Model{ID: "model", Provider: provider}, Messages: []sdk.Message{sdk.UserMessage("original")},
		ContextSourceFrags:     []contextfrag.ContextFrag{currentMessageFrag("message.000", "original")},
		ContextBudgetMaxTokens: 200000, ContextQueryMaterialized: true,
		OnStepCommitted: func(_ context.Context, index int, record *step.Record) (native.StepDirective, error) {
			indexes = append(indexes, index)
			captured = append(captured, record.Messages)
			if index == 0 {
				return native.StepDirective{
					NextInputs: []native.DirectiveInput{{ID: "steer-1", Text: "new direction"}},
				}, nil
			}
			return native.StepDirective{}, nil
		},
	}
	if _, err := native.New(native.Deps{ContextViewApplier: ProviderRunConfigApplier(nil)}).Generate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if provider.calls.Load() != 2 || len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("calls=%d indexes=%v", provider.calls.Load(), indexes)
	}
	if len(captured[1]) < 2 || captured[1][0].Role != sdk.MessageRoleUser {
		t.Fatalf("steer input missing at persistence barrier: %+v", captured[1])
	}
}

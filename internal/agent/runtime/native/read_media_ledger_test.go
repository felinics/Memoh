package native

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

// TestAgentGenerateReadMediaRecordsMutationThroughContextViewApplier proves
// the production reachability path end-to-end: decorateReadMediaTools runs,
// and captures the ledger, before the ContextViewApplier creates it, so
// readMediaState must be attached to the applier's ledger after the fact
// rather than at construction. This mirrors
// TestAgentGenerateReadMediaInjectsImageIntoNextStep but swaps New(Deps{})
// for a Deps.ContextViewApplier that stands in for
// contextview.ApplyProviderRunConfig, assigning a fresh, inspectable ledger
// to cfg.ContextMutations exactly like the real pipeline does. If
// decorateReadMediaTools captured cfg.ContextMutations at construction time
// (before the applier runs), readMediaState.ledger would stay nil and this
// ledger would never see the read_media record.
func TestAgentGenerateReadMediaRecordsMutationThroughContextViewApplier(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00payload")

	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "read",
						Input:      map[string]any{"path": "/data/images/demo.png"},
					}},
				}, nil
			}
			return &sdk.GenerateResult{
				Text:         "done",
				FinishReason: sdk.FinishReasonStop,
			}, nil
		},
	}

	// ContainerProvider normalizes paths by stripping the workdir prefix,
	// so the mock files map must use the normalized (relative) path.
	bp := newAgentReadMediaBridgeProvider(t, map[string][]byte{
		"images/demo.png": pngBytes,
	})

	ledger := contextfrag.NewMutationLedger()
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		cfg.ContextMutations = ledger
		return cfg, nil
	}})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, nil, "/data"),
	})

	result, err := a.Generate(context.Background(), RunConfig{
		Model:              &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:           []sdk.Message{sdk.UserMessage("look at the image")},
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: SessionContext{
			BotID: "bot-1",
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != "done" {
		t.Fatalf("unexpected result text: %q", result.Text)
	}

	records := ledger.Records()
	if len(records) != 1 {
		t.Fatalf("ledger records = %d, want 1 (read_media mutation must reach the real per-request ledger): %#v", len(records), records)
	}
	if records[0].Kind != contextfrag.MutationReadMedia {
		t.Fatalf("ledger record kind = %q, want %q", records[0].Kind, contextfrag.MutationReadMedia)
	}
	if records[0].Detail != "images=1" {
		t.Fatalf("ledger record detail = %q, want %q", records[0].Detail, "images=1")
	}
}

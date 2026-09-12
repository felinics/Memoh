package native

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestWrapToolUIOutputStripsReservedKey(t *testing.T) {
	t.Parallel()

	registry := newToolExecutionMetadataRegistry(nil)
	diffText := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n"
	sdkTools := []sdk.Tool{{
		Name: "edit",
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return map[string]any{
				"ok":                      true,
				tools.UIOutputMetadataKey: map[string]any{"diff": diffText},
			}, nil
		},
	}}

	wrapped := registry.wrapToolUIOutput(sdkTools)
	output, err := wrapped[0].Execute(&sdk.ToolExecContext{ToolCallID: "call-1"}, nil)
	if err != nil {
		t.Fatalf("execute error = %v", err)
	}
	outputMap, ok := output.(map[string]any)
	if !ok {
		t.Fatalf("output type = %T, want map[string]any", output)
	}
	if _, leaked := outputMap[tools.UIOutputMetadataKey]; leaked {
		t.Fatalf("model-facing output still carries the UI-only key: %#v", outputMap)
	}
	if outputMap["ok"] != true {
		t.Fatalf("model-facing output lost its real payload: %#v", outputMap)
	}

	metadata := registry.metadata("call-1")
	if got := metadata["diff"]; got != diffText {
		t.Fatalf("recorded diff = %#v, want %#v", got, diffText)
	}
}

func TestWrapToolUIOutputPassesThroughOtherOutputs(t *testing.T) {
	t.Parallel()

	registry := newToolExecutionMetadataRegistry(nil)
	sdkTools := []sdk.Tool{
		{
			Name: "read",
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"content": "hello"}, nil
			},
		},
		{
			Name: "exec",
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return "plain string output", nil
			},
		},
	}

	wrapped := registry.wrapToolUIOutput(sdkTools)
	for i := range wrapped {
		output, err := wrapped[i].Execute(&sdk.ToolExecContext{ToolCallID: "call-x"}, nil)
		if err != nil {
			t.Fatalf("tool %s execute error = %v", wrapped[i].Name, err)
		}
		switch i {
		case 0:
			m, _ := output.(map[string]any)
			if m["content"] != "hello" {
				t.Fatalf("map output changed: %#v", output)
			}
		case 1:
			if output != "plain string output" {
				t.Fatalf("string output changed: %#v", output)
			}
		}
	}
	if got := registry.metadata("call-x"); got != nil {
		t.Fatalf("no UI metadata should be recorded, got %#v", got)
	}
}

func TestWrapToolUIOutputAnnotatesPersistedToolCall(t *testing.T) {
	t.Parallel()

	registry := newToolExecutionMetadataRegistry(nil)
	registry.recordUIOutput("call-9", map[string]any{"diff": "@@ -1 +1 @@"})

	annotated := registry.annotate([]sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ToolCallPart{ToolCallID: "call-9", ToolName: "edit"},
		},
	}})
	call, ok := annotated[0].Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("part type = %T, want sdk.ToolCallPart", annotated[0].Content[0])
	}
	if got := call.ProviderMetadata["diff"]; got != "@@ -1 +1 @@" {
		t.Fatalf("persisted ProviderMetadata diff = %#v", got)
	}
}

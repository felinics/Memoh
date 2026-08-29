package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
)

func TestToolExecutionMetadataRegistryAnnotatesToolCall(t *testing.T) {
	t.Parallel()

	location := &toolapproval.ExecutionLocation{
		TargetID: "runtime-target-id",
		Kind:     "remote",
		Name:     "Office Mac",
	}
	var update map[string]any
	registry := newToolExecutionMetadataRegistry(func(_ sdk.ToolCall, metadata map[string]any) {
		update = metadata
	})
	handler := registry.wrap(func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error) {
		return sdk.ToolApprovalResult{
			Decision: sdk.ToolApprovalDecisionApproved,
			Metadata: map[string]any{
				toolapproval.ExecutionLocationMetadataKey: location,
				"unrelated": "not persisted",
			},
		}, nil
	})

	_, err := handler(context.Background(), sdk.ToolCall{
		ToolCallID: "call-1",
		ToolName:   "exec",
		Input:      map[string]any{"command": "pwd"},
	})
	if err != nil {
		t.Fatalf("approval handler error = %v", err)
	}
	if got := update[toolapproval.ExecutionLocationMetadataKey]; got != location {
		t.Fatalf("update location = %#v, want %#v", got, location)
	}
	if _, ok := update["unrelated"]; ok {
		t.Fatalf("update leaked unrelated approval metadata: %#v", update)
	}

	messages := []sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID:       "call-1",
			ToolName:         "exec",
			Input:            map[string]any{"command": "pwd"},
			ProviderMetadata: map[string]any{"provider": "kept"},
		}},
	}}
	annotated := registry.annotate(messages)
	call, ok := annotated[0].Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("annotated part = %#v, want ToolCallPart", annotated[0].Content[0])
	}
	if call.ProviderMetadata["provider"] != "kept" {
		t.Fatalf("provider metadata was not preserved: %#v", call.ProviderMetadata)
	}
	if call.ProviderMetadata[toolapproval.ExecutionLocationMetadataKey] != location {
		t.Fatalf("execution location = %#v, want %#v", call.ProviderMetadata, location)
	}
	original := messages[0].Content[0].(sdk.ToolCallPart)
	if _, ok := original.ProviderMetadata[toolapproval.ExecutionLocationMetadataKey]; ok {
		t.Fatalf("annotate mutated original messages: %#v", original.ProviderMetadata)
	}
	encoded, err := json.Marshal(annotated)
	if err != nil {
		t.Fatalf("marshal annotated messages: %v", err)
	}
	serialized := string(encoded)
	if !strings.Contains(serialized, "Office Mac") {
		t.Fatalf("serialized messages lost the stable name: %s", serialized)
	}
	if strings.Contains(serialized, "runtime-target-id") || strings.Contains(serialized, "/Users/alice") {
		t.Fatalf("serialized messages leaked internal execution details: %s", serialized)
	}
}

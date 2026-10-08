package native

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// The terminal messages of a deferred step carry the pending decision on the
// parked tool call: a user-input request is annotated under the user_input
// key, a tool approval under the approval key. The UI reads these annotations
// to render the card; without them a refreshed page shows a bare tool call.
func TestAnnotateDeferredApprovalMarksParkedCall(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		sdk.UserMessage("start"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "ask_user", Input: sdk.ParseToolArguments(`{"question":"?"}`)},
			sdk.ToolCallPart{ToolCallID: "call-2", ToolName: "exec", Input: sdk.ParseToolArguments(`{"cmd":"ls"}`)},
		}},
	}

	annotated := annotateDeferredApproval(messages, toolexec.ToolApprovalResult{
		Decision:   toolexec.ToolApprovalDecisionDeferred,
		ApprovalID: "ui-1",
		Metadata:   map[string]any{"tool_call_id": "call-1", "kind": userinput.DeferredKind, "short_id": 7, "ui_payload": map[string]any{"title": "pick"}},
	})
	parked := annotated[1].Content[0].(sdk.ToolCallPart)
	request, ok := partmeta.Object(parked.ProviderMetadata, partmeta.KeyUserInput)
	if !ok || request["user_input_id"] != "ui-1" || request["status"] != "pending" || request["short_id"] != float64(7) {
		t.Fatalf("user_input annotation = %#v", parked.ProviderMetadata)
	}
	if partmeta.Has(parked.ProviderMetadata, partmeta.KeyApproval) {
		t.Fatalf("user-input request annotated as tool approval: %#v", parked.ProviderMetadata)
	}
	if other := annotated[1].Content[1].(sdk.ToolCallPart); partmeta.Has(other.ProviderMetadata, partmeta.KeyUserInput) {
		t.Fatalf("sibling call annotated: %#v", other.ProviderMetadata)
	}
	if original := messages[1].Content[0].(sdk.ToolCallPart); partmeta.Has(original.ProviderMetadata, partmeta.KeyUserInput) {
		t.Fatal("annotateDeferredApproval mutated its input")
	}

	approval := annotateDeferredApproval(messages, toolexec.ToolApprovalResult{
		Decision:   toolexec.ToolApprovalDecisionDeferred,
		ApprovalID: "approval-2",
		Metadata:   map[string]any{"tool_call_id": "call-2", "short_id": 3, "status": "pending"},
	})
	exec := approval[1].Content[1].(sdk.ToolCallPart)
	if !partmeta.Has(exec.ProviderMetadata, partmeta.KeyApproval) || partmeta.Has(exec.ProviderMetadata, partmeta.KeyUserInput) {
		t.Fatalf("approval annotation = %#v", exec.ProviderMetadata)
	}

	// Without a tool_call_id nothing can be annotated; the messages pass through.
	if got := annotateDeferredApproval(messages, toolexec.ToolApprovalResult{ApprovalID: "x"}); len(got) != len(messages) || partmeta.Has(got[1].Content[0].(sdk.ToolCallPart).ProviderMetadata, partmeta.KeyUserInput) {
		t.Fatalf("annotation without tool_call_id = %#v", got)
	}
}

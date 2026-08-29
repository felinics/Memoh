package contextfrag

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestRepairToolClosureFragsRefreshesRebuiltMessageAccounting(t *testing.T) {
	t.Parallel()

	assistant := MessageFrag(MessageFragInput{
		ID: "history.assistant",
		Message: sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "call-valid",
			ToolName:   "search",
		}}},
		Kind: KindConversationEvent,
		Slot: SlotHistory,
	})
	result := MessageFrag(MessageFragInput{
		ID: "history.result",
		Message: sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
			sdk.ToolResultPart{ToolCallID: "call-orphan", ToolName: "search", Result: "orphan"},
			sdk.ToolResultPart{ToolCallID: "call-valid", ToolName: "search", Result: "kept"},
		}},
		Kind:          KindConversationEvent,
		Slot:          SlotHistory,
		TokenEstimate: 999,
	})
	result.ConflictKey = "history.tool-result"
	result = WithContextRef(result, ContextRef{
		Namespace:   "history",
		ID:          "result-row",
		Version:     3,
		HashAlgo:    HashAlgoSHA256,
		HashScope:   HashScopeSourcePayload,
		ContentHash: "stale-source-hash",
		Schema:      SchemaContextRef,
		Durability:  RefDurable,
	})

	repaired := RepairToolClosureFrags([]ContextFrag{assistant, result}, Scope{}, "test")
	if len(repaired) != 2 {
		t.Fatalf("repaired fragments = %d, want 2", len(repaired))
	}
	got := repaired[1]
	msg := FragMessage(got)
	if msg == nil || len(msg.Content) != 1 {
		t.Fatalf("rebuilt tool result = %#v, want one valid result", msg)
	}
	kept, ok := msg.Content[0].(sdk.ToolResultPart)
	if !ok || kept.ToolCallID != "call-valid" {
		t.Fatalf("rebuilt tool result content = %#v, want call-valid", msg.Content[0])
	}
	if got.TokenEstimate != 0 {
		t.Fatalf("rebuilt token estimate = %d, want stale estimate cleared", got.TokenEstimate)
	}
	if resolved, estimated := ResolveFragTokens(got), EstimateFragTokens(got); resolved != estimated || resolved == 999 {
		t.Fatalf("rebuilt token accounting = %d, estimated = %d, stale = 999", resolved, estimated)
	}
	if !got.Ref.EqualIdentity(result.Ref) {
		t.Fatalf("rebuilt ref identity = %#v, want %#v", got.Ref, result.Ref)
	}
	if got.Ref.HashScope != HashScopeCanonicalFragment || got.Ref.ContentHash == "" || got.Ref.ContentHash == result.Ref.ContentHash {
		t.Fatalf("rebuilt ref hash was not refreshed: got %#v, original %#v", got.Ref, result.Ref)
	}
	expected, err := CanonicalFragmentHash(got)
	if err != nil {
		t.Fatalf("hash rebuilt fragment: %v", err)
	}
	if got.Ref.ContentHash != expected.Value {
		t.Fatalf("rebuilt ref hash = %q, want canonical %q", got.Ref.ContentHash, expected.Value)
	}
	if got.ConflictKey != result.ConflictKey {
		t.Fatalf("rebuilt conflict key = %q, want %q", got.ConflictKey, result.ConflictKey)
	}
}

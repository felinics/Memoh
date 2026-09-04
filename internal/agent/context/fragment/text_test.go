package contextfrag

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func textFragments() []ContextFrag {
	history := sdk.AssistantMessage("earlier answer")
	current := sdk.UserMessage("what now?")
	recall := sdk.UserMessage("Recalled: likes tea", sdk.TextPart{Text: "Recalled: prefers metric"})
	return []ContextFrag{
		{ID: "system.prompt.body", Kind: KindSystemPrompt, Slot: SlotSystem, Parts: []Part{{Type: PartText, Text: "You are Memoh."}}},
		{ID: "rules.agents", Kind: KindWorkspaceInstruction, Slot: SlotSystem, Ref: ContextRef{Namespace: "workspace", ID: "AGENTS.md", Schema: SchemaContextRef, HashAlgo: HashAlgoSHA256, HashScope: HashScopeCanonicalFragment, ContentHash: "abc123"}, Parts: []Part{{Type: PartText, Text: "Follow AGENTS.md"}}},
		{ID: "message.003", Kind: KindConversationEvent, Slot: SlotHistory, Parts: []Part{{Type: PartSDKMessage, SDKMessage: &history}}},
		{ID: "message.016", Kind: KindCurrentUserMessage, Slot: SlotCurrentUser, Parts: []Part{{Type: PartSDKMessage, SDKMessage: &current}}},
		{ID: "memory.recall", Kind: KindMemoryRecall, Slot: SlotAfterHistoryBeforeCurrent, Parts: []Part{{Type: PartSDKMessage, SDKMessage: &recall}}},
	}
}

func TestFragmentTextsKeepsInjectedContextAndSkipsTheConversation(t *testing.T) {
	t.Parallel()

	texts := FragmentTexts(textFragments())
	if len(texts) != 3 {
		t.Fatalf("texts = %#v, want the three injected fragments", texts)
	}
	if texts[0].Kind != KindSystemPrompt || texts[0].Label != "system.prompt.body" || texts[0].Text != "You are Memoh." || texts[0].ContentHash == "" {
		t.Fatalf("system text = %#v", texts[0])
	}
	if texts[1].ContentHash != "abc123" || texts[1].Text != "Follow AGENTS.md" {
		t.Fatalf("rules text must keep the fragment's own hash: %#v", texts[1])
	}
	if texts[2].Kind != KindMemoryRecall || texts[2].Text != "Recalled: likes tea\nRecalled: prefers metric" {
		t.Fatalf("recall text = %#v", texts[2])
	}
	if FragmentTexts(nil) != nil {
		t.Fatalf("no fragments must yield no texts")
	}
}

func TestToolDefinitionTextHashesTheSerializedTool(t *testing.T) {
	t.Parallel()

	accounting, text := ToolDefinitionText("workspace", sdk.Tool{Name: "exec", Description: "run a command"})
	if accounting.Provider != "workspace" || accounting.Name != "exec" || accounting.ContentHash == "" || accounting.ContentHash != text.ContentHash || text.Label != "workspace/exec" {
		t.Fatalf("accounting = %#v, text = %#v", accounting, text)
	}
	if text.Kind != KindToolDefinition || !json.Valid([]byte(text.Text)) || accounting.Bytes != len(text.Text) {
		t.Fatalf("tool definition text = %#v (bytes %d)", text, accounting.Bytes)
	}
	again, _ := ToolDefinitionText("workspace", sdk.Tool{Name: "exec", Description: "run a command"})
	if again.ContentHash != accounting.ContentHash {
		t.Fatalf("hash must be stable for the same definition")
	}
}

func TestLifecycleSnapshotListsInjectedFragmentRefs(t *testing.T) {
	t.Parallel()

	snapshot := BuildLifecycleSnapshot(BuildManifest(textFragments()))
	kinds := make([]Kind, 0, len(snapshot.Fragments))
	for _, ref := range snapshot.Fragments {
		kinds = append(kinds, ref.Kind)
		if ref.ContentHash == "" || ref.TokenEstimate <= 0 || ref.Kind == "" || ref.Slot == "" {
			t.Fatalf("fragment ref = %#v", ref)
		}
	}
	if len(kinds) != 3 || kinds[0] != KindSystemPrompt || kinds[1] != KindWorkspaceInstruction || kinds[2] != KindMemoryRecall {
		t.Fatalf("fragment refs = %v, want the injected fragments in manifest order", kinds)
	}
	if snapshot.Fragments[1].ContentHash != "abc123" {
		t.Fatalf("ref must carry the fragment's content hash: %#v", snapshot.Fragments[1])
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "rules.agents") || strings.Contains(string(raw), "You are Memoh") {
		t.Fatalf("snapshot must stay content-light: %s", raw)
	}
}

type recordingTextSink struct {
	calls [][]FragmentText
}

func (s *recordingTextSink) PersistFragmentTexts(texts []FragmentText) {
	s.calls = append(s.calls, texts)
}

func TestLifecycleHolderRecordsEachFragmentTextOnce(t *testing.T) {
	t.Parallel()

	sink := &recordingTextSink{}
	holder := NewLifecycleHolder()
	holder.SetTextSink(sink)
	frags := textFragments()
	holder.RecordFragmentTexts(frags)
	holder.RecordFragmentTexts(frags)
	if len(sink.calls) != 1 || len(sink.calls[0]) != 3 {
		t.Fatalf("sink calls = %#v, want one call with three texts", sink.calls)
	}
	_, tool := ToolDefinitionText("workspace", sdk.Tool{Name: "exec"})
	holder.RecordFragmentTexts(nil)
	holder.RecordToolDefinitions([]FragmentText{tool, tool})
	if len(sink.calls) != 2 || len(sink.calls[1]) != 1 || sink.calls[1][0].Kind != KindToolDefinition {
		t.Fatalf("tool definition call = %#v", sink.calls)
	}

	var nilHolder *LifecycleHolder
	nilHolder.RecordFragmentTexts(frags)
	NewLifecycleHolder().RecordFragmentTexts(frags)
}

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

func TestFragmentTextHashIgnoresScopeAndRef(t *testing.T) {
	t.Parallel()

	a := ContextFrag{ID: "system.prompt.body", Kind: KindSystemPrompt, Slot: SlotSystem, Scope: Scope{SessionID: "sess-a", CurrentMessageID: "tg:1"}, Ref: ContextRef{ContentHash: "canon-a"}, Parts: []Part{{Type: PartText, Text: "You are Memoh."}}}
	b := a
	b.Scope = Scope{SessionID: "sess-b"}
	b.Ref = ContextRef{ContentHash: "canon-b"}
	texts := FragmentTexts([]ContextFrag{a, b})
	if len(texts) != 2 || texts[0].TextHash == "" || texts[0].TextHash != texts[1].TextHash {
		t.Fatalf("the same text must share one store key across scopes: %#v", texts)
	}
	if texts[0].ContentHash != "canon-a" || texts[1].ContentHash != "canon-b" {
		t.Fatalf("texts must keep their fragment's own content hash: %#v", texts)
	}
	if texts[0].TextHash != TextHash(KindSystemPrompt, "You are Memoh.") || TextHash(KindWorkspaceInstruction, "You are Memoh.") == texts[0].TextHash {
		t.Fatalf("store key must be the kind-qualified text hash: %#v", texts[0])
	}
	_, tool := ToolDefinitionText("workspace", sdk.Tool{Name: "exec"})
	if tool.TextHash == "" || tool.TextHash != tool.ContentHash {
		t.Fatalf("tool definitions are keyed by their serialized hash: %#v", tool)
	}
}

func TestLifecycleSnapshotRefsCarryTheStoredTextHash(t *testing.T) {
	t.Parallel()

	holder := NewLifecycleHolder()
	holder.SetTextSink(&recordingTextSink{})
	frags := textFragments()
	holder.SetManifest(BuildManifest(frags))
	holder.RecordFragmentTexts(frags)
	snapshot, ok := holder.Snapshot()
	if !ok || len(snapshot.Fragments) != 3 {
		t.Fatalf("snapshot = %#v, %v", snapshot, ok)
	}
	if snapshot.Fragments[1].ContentHash != "abc123" || snapshot.Fragments[1].TextHash != TextHash(KindWorkspaceInstruction, "Follow AGENTS.md") {
		t.Fatalf("ref must carry both the fragment hash and the text store key: %#v", snapshot.Fragments[1])
	}
	if snapshot.Fragments[0].TextHash != TextHash(KindSystemPrompt, "You are Memoh.") || snapshot.Fragments[2].TextHash == "" {
		t.Fatalf("every recorded fragment resolves its text hash: %#v", snapshot.Fragments)
	}

	unrecorded := NewLifecycleHolder()
	unrecorded.SetManifest(BuildManifest(frags))
	snapshot, _ = unrecorded.Snapshot()
	for _, ref := range snapshot.Fragments {
		if ref.TextHash != "" {
			t.Fatalf("a run that stored no text must not claim a text hash: %#v", ref)
		}
	}
}

func TestRowCopyKeepsOnlyTheBoundedAccounting(t *testing.T) {
	t.Parallel()

	holder := NewLifecycleHolder()
	holder.SetTextSink(&recordingTextSink{})
	frags := textFragments()
	holder.SetManifest(BuildManifest(frags))
	holder.RecordFragmentTexts(frags)
	holder.SetRunTraceSource(func() *RunTrace { return &RunTrace{Steps: 1} })
	snapshot, _ := holder.Snapshot()
	snapshot.ToolDefs = []ToolDefAccounting{{Provider: "workspace", Name: "exec", Bytes: 90, TokenEstimate: 22, ContentHash: "tool-exec"}}
	snapshot.SelectionDecisions = []SelectionDecision{{ID: "system.prompt.body"}}

	row := snapshot.RowCopy()
	if row.RunTrace != nil || row.Fragments != nil || row.SelectionDecisions != nil {
		t.Fatalf("row copy keeps run facts: %#v", row)
	}
	if len(row.ToolDefs) != 1 || row.ToolDefs[0].ContentHash != "" || row.ToolDefs[0].TokenEstimate != 22 {
		t.Fatalf("row copy must keep tool accounting without hashes: %#v", row.ToolDefs)
	}
	if snapshot.ToolDefs[0].ContentHash != "tool-exec" || snapshot.RunTrace == nil || len(snapshot.Fragments) != 3 {
		t.Fatalf("the run snapshot must not be touched: %#v", snapshot)
	}
}

func TestFragmentTextsStoreMemoryRecallFromTheHistorySlot(t *testing.T) {
	t.Parallel()

	recall := sdk.UserMessage("Recalled: likes tea")
	history := sdk.AssistantMessage("earlier answer")
	frags := []ContextFrag{
		{ID: "memory.recall", Kind: KindMemoryRecall, Slot: SlotHistory, Parts: []Part{{Type: PartSDKMessage, SDKMessage: &recall}}},
		{ID: "message.003", Kind: KindConversationEvent, Slot: SlotHistory, Parts: []Part{{Type: PartSDKMessage, SDKMessage: &history}}},
	}
	texts := FragmentTexts(frags)
	if len(texts) != 1 || texts[0].Kind != KindMemoryRecall || texts[0].Text != "Recalled: likes tea" {
		t.Fatalf("texts = %#v, want only the recalled memory", texts)
	}
	refs := BuildLifecycleSnapshot(BuildManifest(frags)).Fragments
	if len(refs) != 1 || refs[0].Kind != KindMemoryRecall || refs[0].Slot != SlotHistory {
		t.Fatalf("refs = %#v, want the recall ref in its history slot", refs)
	}
}

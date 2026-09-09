package contextfrag

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestCompileAddsRefsAndManifestTraceWithoutChangingRenderedLegacyView(t *testing.T) {
	t.Parallel()

	system := "base system"
	messages := []sdk.Message{sdk.UserMessage("history")}
	images := []sdk.ImagePart{{Image: "data:image/png;base64,abc", MediaType: "image/png"}}

	got := Compile(CompileInput{
		Source:       "test",
		System:       system,
		Messages:     messages,
		Query:        "current",
		InlineImages: images,
	})

	if got.System != system || got.Query != "current" || len(got.Messages) != 1 || len(got.InlineImages) != 1 {
		t.Fatalf("rendered legacy view changed: system=%q query=%q messages=%d images=%d", got.System, got.Query, len(got.Messages), len(got.InlineImages))
	}
	for _, frag := range got.Frags {
		if err := ValidateContextRef(frag.Ref); err != nil {
			t.Fatalf("compiled frag %q has invalid ref %#v: %v", frag.ID, frag.Ref, err)
		}
		if frag.Ref.HashScope != HashScopeCanonicalFragment || frag.Ref.ContentHash == "" {
			t.Fatalf("compiled frag %q missing canonical hash ref: %#v", frag.ID, frag.Ref)
		}
	}
	if len(got.Manifest.RenderedOutputs) == 0 {
		t.Fatal("manifest should explain rendered output refs")
	}
	if !manifestHasWarning(got.Manifest, "non_durable_context_ref") {
		t.Fatalf("source-less legacy refs should produce synthetic warning: %#v", got.Manifest.ValidationWarnings)
	}
}

func TestContextRefEmptyDurabilityMatchesDurableIdentity(t *testing.T) {
	t.Parallel()

	legacy := ContextRef{Namespace: "history", ID: "row-1", Schema: SchemaContextRef}
	durable := legacy
	durable.Durability = RefDurable

	if !legacy.EqualIdentity(durable) {
		t.Fatalf("empty durability should match durable identity: legacy=%#v durable=%#v", legacy, durable)
	}
	if legacy.StableKey() != durable.StableKey() {
		t.Fatalf("empty durability stable key = %q, durable = %q", legacy.StableKey(), durable.StableKey())
	}
	if legacy.StableKey() != "history:row-1#context_ref" {
		t.Fatalf("durable stable key should preserve legacy shape, got %q", legacy.StableKey())
	}
	if !legacy.IsDurable() {
		t.Fatalf("empty durability should default to durable for compatibility: %#v", legacy)
	}
}

func TestBuildManifestWarnsForDirectNonDurableRefs(t *testing.T) {
	t.Parallel()

	synthetic := TextFrag(TextFragInput{
		ID:   "history.synthetic",
		Kind: KindConversationEvent,
		Slot: SlotHistory,
		Text: "source-less",
	})
	debug := TextFrag(TextFragInput{
		ID:   "history.debug",
		Kind: KindConversationEvent,
		Slot: SlotHistory,
		Text: "debug",
	})
	debug.Ref = ContextRef{
		Namespace:  "test",
		ID:         "debug-ref",
		Schema:     SchemaContextRef,
		Durability: RefDebug,
	}

	manifest := BuildManifest([]ContextFrag{synthetic, debug})
	if countManifestWarnings(manifest, "non_durable_context_ref") != 2 {
		t.Fatalf("BuildManifest should warn for direct synthetic/debug refs: %#v", manifest.ValidationWarnings)
	}
}

func TestCompileUsesStableContentAddressedRefsWhenSourceIDIsMissing(t *testing.T) {
	t.Parallel()

	first := Compile(CompileInput{
		Source:   "test",
		Messages: []sdk.Message{sdk.UserMessage("target")},
	})
	second := Compile(CompileInput{
		Source:   "test",
		Messages: []sdk.Message{sdk.UserMessage("prefix"), sdk.UserMessage("target")},
	})

	firstRef, ok := refForRenderedMessageText(first.Frags, "target")
	if !ok {
		t.Fatalf("missing target ref in first compile: %#v", first.Manifest.Items)
	}
	secondRef, ok := refForRenderedMessageText(second.Frags, "target")
	if !ok {
		t.Fatalf("missing target ref in second compile: %#v", second.Manifest.Items)
	}
	if firstRef.ID != secondRef.ID {
		t.Fatalf("source-less legacy message ref drifted with position: first=%#v second=%#v", firstRef, secondRef)
	}
	if firstRef.Durability != RefSynthetic || secondRef.Durability != RefSynthetic {
		t.Fatalf("source-less fallback refs must be marked synthetic: first=%#v second=%#v", firstRef, secondRef)
	}
}

func TestCompileMarksDuplicateSourceLessRefsSynthetic(t *testing.T) {
	t.Parallel()

	got := Compile(CompileInput{
		Source: "test",
		Messages: []sdk.Message{
			sdk.UserMessage("same"),
			sdk.UserMessage("same"),
		},
	})

	var refs []ContextRef
	for _, frag := range got.Frags {
		if frag.Slot == SlotHistory {
			refs = append(refs, frag.Ref)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("history refs = %d, want 2: %#v", len(refs), got.Manifest.Items)
	}
	if refs[0].ID != refs[1].ID {
		t.Fatalf("duplicate source-less content should share content-addressed ID for trace: %#v", refs)
	}
	for _, ref := range refs {
		if ref.Durability != RefSynthetic || ref.IsDurable() {
			t.Fatalf("duplicate source-less refs must not be treated as durable: %#v", ref)
		}
	}
	if !manifestHasWarning(got.Manifest, "non_durable_context_ref") {
		t.Fatalf("manifest missing synthetic warning for duplicate source-less refs: %#v", got.Manifest.ValidationWarnings)
	}
	if countManifestWarnings(got.Manifest, "non_durable_context_ref") != 2 {
		t.Fatalf("manifest should warn for each non-durable ref: %#v", got.Manifest.ValidationWarnings)
	}
}

func TestCompileWarnsWhenSourceLessRefsDependOnScope(t *testing.T) {
	t.Parallel()

	first := Compile(CompileInput{
		Source:   "test",
		Scope:    Scope{CurrentMessageID: "msg-1"},
		Messages: []sdk.Message{sdk.UserMessage("same")},
	})
	second := Compile(CompileInput{
		Source:   "test",
		Scope:    Scope{CurrentMessageID: "msg-2"},
		Messages: []sdk.Message{sdk.UserMessage("same")},
	})

	firstRef, ok := refForRenderedMessageText(first.Frags, "same")
	if !ok {
		t.Fatalf("missing first ref: %#v", first.Manifest.Items)
	}
	secondRef, ok := refForRenderedMessageText(second.Frags, "same")
	if !ok {
		t.Fatalf("missing second ref: %#v", second.Manifest.Items)
	}
	if firstRef.ID == secondRef.ID {
		t.Fatalf("scope-dependent source-less refs should expose drift, got same ID %#v", firstRef)
	}
	if firstRef.Durability != RefSynthetic || secondRef.Durability != RefSynthetic {
		t.Fatalf("scope-dependent source-less refs must be synthetic: first=%#v second=%#v", firstRef, secondRef)
	}
	if !manifestHasWarning(first.Manifest, "non_durable_context_ref") || !manifestHasWarning(second.Manifest, "non_durable_context_ref") {
		t.Fatalf("missing synthetic warnings: first=%#v second=%#v", first.Manifest.ValidationWarnings, second.Manifest.ValidationWarnings)
	}
}

func TestManifestRenderedOutputsOnlyIncludeRenderedRefs(t *testing.T) {
	t.Parallel()

	historyText := WithContextRef(TextFrag(TextFragInput{
		ID:   "history.text",
		Kind: KindConversationEvent,
		Slot: SlotHistory,
		Text: "not rendered by legacy renderer",
	}), ContextRef{Namespace: "test", ID: "history.text", Schema: SchemaContextRef})
	currentA := WithContextRef(TextFrag(TextFragInput{
		ID:   "current.a",
		Kind: KindCurrentUserMessage,
		Slot: SlotCurrentUser,
		Text: "a",
	}), ContextRef{Namespace: "test", ID: "current.a", Schema: SchemaContextRef})
	currentB := WithContextRef(TextFrag(TextFragInput{
		ID:   "current.b",
		Kind: KindCurrentUserMessage,
		Slot: SlotCurrentUser,
		Text: "b",
	}), ContextRef{Namespace: "test", ID: "current.b", Schema: SchemaContextRef})

	manifest := BuildManifest([]ContextFrag{historyText, currentA, currentB})
	if renderedOutputContainsRef(manifest.RenderedOutputs, historyText.Ref) {
		t.Fatalf("history text ref should not be listed as rendered output: %#v", manifest.RenderedOutputs)
	}
	if renderedOutputContainsRef(manifest.RenderedOutputs, currentA.Ref) {
		t.Fatalf("overwritten current-user text ref should not be listed as rendered output: %#v", manifest.RenderedOutputs)
	}
	if !renderedOutputContainsRef(manifest.RenderedOutputs, currentB.Ref) {
		t.Fatalf("last current-user text ref should be listed as rendered output: %#v", manifest.RenderedOutputs)
	}
}

func TestCompilePreservesExplicitMessageFragAtSourcePosition(t *testing.T) {
	t.Parallel()

	before := sdk.UserMessage("before")
	summary := sdk.UserMessage("<summary>\ncondensed\n</summary>")
	summaryRef := ContextRef{Namespace: "compaction_log", ID: "log-1", Schema: SchemaContextRef, Durability: RefDurable}
	summaryFrag := MessageFrag(MessageFragInput{
		ID:      "message.001",
		Message: summary,
		Kind:    KindConversationSummary,
		Slot:    SlotHistory,
		Source:  "compaction_log",
	})
	summaryFrag.Ref = summaryRef

	assembled := Compile(CompileInput{
		Source:   SourceRunConfig,
		Messages: []sdk.Message{before, summary},
		Existing: []ContextFrag{summaryFrag},
	})

	if len(assembled.Messages) != 2 {
		t.Fatalf("messages = %d, want explicit message frag to replace derived message only: %#v", len(assembled.Messages), assembled.Messages)
	}
	if got := assembled.Messages[0].Content[0].(sdk.TextPart).Text; got != "before" {
		t.Fatalf("first message = %q, want source order preserved", got)
	}
	if len(assembled.Manifest.Items) != 2 {
		t.Fatalf("manifest items = %d, want 2: %#v", len(assembled.Manifest.Items), assembled.Manifest.Items)
	}
	if assembled.Manifest.Items[1].Kind != KindConversationSummary {
		t.Fatalf("second manifest item kind = %q, want %q", assembled.Manifest.Items[1].Kind, KindConversationSummary)
	}
	if !assembled.Manifest.Items[1].Ref.EqualIdentity(summaryRef) {
		t.Fatalf("explicit message ref was not preserved: %#v", assembled.Manifest.Items[1].Ref)
	}
}

func TestCompilePreservesUnmatchedExplicitMessageFrag(t *testing.T) {
	t.Parallel()

	summary := sdk.UserMessage("<summary>\ncondensed\n</summary>")
	summaryRef := ContextRef{Namespace: "compaction_log", ID: "log-1", Schema: SchemaContextRef, Durability: RefDurable}
	summaryFrag := MessageFrag(MessageFragInput{
		ID:      "summary.log-1",
		Message: summary,
		Kind:    KindConversationSummary,
		Slot:    SlotHistory,
		Source:  "compaction_log",
	})
	summaryFrag.Ref = summaryRef

	assembled := Compile(CompileInput{
		Source:   SourceRunConfig,
		Messages: []sdk.Message{sdk.UserMessage("after summary")},
		Existing: []ContextFrag{summaryFrag},
	})

	if len(assembled.Messages) != 2 {
		t.Fatalf("messages = %d, want explicit summary plus derived message: %#v", len(assembled.Messages), assembled.Messages)
	}
	if len(assembled.Manifest.Items) != 2 {
		t.Fatalf("manifest items = %d, want explicit summary plus derived message: %#v", len(assembled.Manifest.Items), assembled.Manifest.Items)
	}
	if assembled.Manifest.Items[0].Kind != KindConversationSummary {
		t.Fatalf("first manifest item kind = %q, want %q", assembled.Manifest.Items[0].Kind, KindConversationSummary)
	}
	if !assembled.Manifest.Items[0].Ref.EqualIdentity(summaryRef) {
		t.Fatalf("unmatched explicit message ref was not preserved: %#v", assembled.Manifest.Items[0].Ref)
	}
}

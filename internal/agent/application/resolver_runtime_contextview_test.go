package application

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/contextview"
)

func TestRuntimeContextViaContextViewAssemblesSections(t *testing.T) {
	t.Parallel()

	sections := []contextview.RuntimeSection{
		{ID: "runtime.preamble", Text: "# Memoh Runtime Context\n\npreamble body"},
		{ID: "runtime.section.current-runtime", Text: "## Current Runtime\n\n- Bot ID: bot-1"},
	}
	markdown, uri, manifest := runtimeContextViaContextView(context.Background(), nil, sections, "")

	const want = "# Memoh Runtime Context\n\npreamble body\n\n## Current Runtime\n\n- Bot ID: bot-1\n\n"
	if markdown != want {
		t.Fatalf("markdown = %q, want structural assembly %q", markdown, want)
	}
	if uri != runtimeContextURI {
		t.Fatalf("uri = %q, want %q", uri, runtimeContextURI)
	}
	if manifest == nil {
		t.Fatal("expected a non-nil manifest for a successful view build")
	}
	if manifest.Counts.Fragments == 0 {
		t.Fatalf("expected non-zero manifest fragment count, got %+v", manifest.Counts)
	}
}

func TestRuntimeContextViaContextViewFallbackCarriesContentLightManifest(t *testing.T) {
	t.Parallel()

	const privateText = "PRIVATE ACP FALLBACK CONTENT"
	sections := []contextview.RuntimeSection{
		{ID: "duplicate", Text: "# Memoh Runtime Context\n\n" + privateText},
		{ID: "duplicate", Text: "## Current Runtime\n\nkeep fallback output"},
	}

	markdown, uri, manifest := runtimeContextViaContextView(context.Background(), nil, sections, "")

	if !strings.Contains(markdown, privateText) || !strings.Contains(markdown, "keep fallback output") {
		t.Fatalf("legacy fallback markdown = %q, want both source sections", markdown)
	}
	if uri != runtimeContextURI {
		t.Fatalf("uri = %q, want %q", uri, runtimeContextURI)
	}
	if manifest == nil {
		t.Fatal("legacy fallback must return a lifecycle manifest")
	}
	if manifest.View != contextfrag.ViewExternalAgentPrompt {
		t.Fatalf("fallback manifest view = %q, want %q", manifest.View, contextfrag.ViewExternalAgentPrompt)
	}
	records := manifest.Mutations.Records()
	if len(records) != 1 ||
		records[0].Kind != contextfrag.MutationContextViewFallback ||
		records[0].Detail != "build_error" {
		t.Fatalf("fallback mutations = %#v, want one build_error context fallback", records)
	}
	raw, err := json.Marshal(contextfrag.BuildLifecycleSnapshot(*manifest))
	if err != nil {
		t.Fatalf("marshal fallback lifecycle: %v", err)
	}
	if bytes.Contains(raw, []byte(privateText)) {
		t.Fatalf("fallback lifecycle leaked raw prompt text: %s", raw)
	}
}

func TestRuntimeContextSectionsSafeWithHeadingInsideFileExcerpt(t *testing.T) {
	t.Parallel()

	fileBlock := "## Long-Term Memory\n\nEmbedded excerpt from `/data/MEMORY.md`.\n\n```markdown\n# User Memory\nline before heading\n## Preferences\nprefers small patches\n```"
	sections := []contextview.RuntimeSection{
		{ID: "runtime.preamble", Text: "# Memoh Runtime Context\n\npreamble"},
		{ID: "runtime.section.file.000", Text: fileBlock},
	}
	markdown, _, _ := runtimeContextViaContextView(context.Background(), nil, sections, "")

	if !strings.Contains(markdown, "line before heading\n## Preferences\nprefers small patches") {
		t.Fatalf("fence content must survive byte-for-byte:\n%s", markdown)
	}
}

func TestFinalizeRuntimeSectionsDropsOversizedDynamicSections(t *testing.T) {
	t.Parallel()

	sections := []contextview.RuntimeSection{
		{
			ID:     "runtime.preamble",
			Text:   "# Memoh Runtime Context",
			Budget: contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowKeep},
		},
		{
			ID:     "runtime.section.memory-recall",
			Text:   "## Retrieved Memory\n\n" + strings.Repeat("memory ", 20),
			Budget: contextfrag.BudgetPolicy{MaxChars: 32, Overflow: contextfrag.OverflowDrop},
		},
		{
			ID:     "runtime.section.memory-hook",
			Text:   "## Memory Hook Context\n\n" + strings.Repeat("hook ", 20),
			Budget: contextfrag.BudgetPolicy{MaxChars: 32, Overflow: contextfrag.OverflowDrop},
		},
		{ID: "runtime.section.runtime-notes", Text: "## Runtime Notes\n\nkeep me"},
	}

	markdown := finalizeRuntimeSections(sections)
	if !strings.Contains(markdown, "# Memoh Runtime Context") || !strings.Contains(markdown, "keep me") {
		t.Fatalf("fallback dropped retained sections: %q", markdown)
	}
	if strings.Contains(markdown, "Retrieved Memory") || strings.Contains(markdown, "Memory Hook Context") {
		t.Fatalf("fallback retained oversized dynamic sections: %q", markdown)
	}
}

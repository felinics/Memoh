package application

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/contextview"
)

func TestACPContextViaContextViewAssemblesSections(t *testing.T) {
	t.Parallel()

	sections := []contextview.ACPSection{
		{ID: "acp.preamble", Text: "# Memoh ACP Context\n\npreamble body"},
		{ID: "acp.section.current-runtime", Text: "## Current Runtime\n\n- Bot ID: bot-1"},
	}
	markdown, uri, manifest := acpContextViaContextView(context.Background(), nil, sections, "")

	const want = "# Memoh ACP Context\n\npreamble body\n\n## Current Runtime\n\n- Bot ID: bot-1\n\n"
	if markdown != want {
		t.Fatalf("markdown = %q, want structural assembly %q", markdown, want)
	}
	if uri != acpContextURI {
		t.Fatalf("uri = %q, want %q", uri, acpContextURI)
	}
	if manifest == nil {
		t.Fatal("expected a non-nil manifest for a successful view build")
	}
	if manifest.Counts.Fragments == 0 {
		t.Fatalf("expected non-zero manifest fragment count, got %+v", manifest.Counts)
	}
}

func TestACPContextViaContextViewFallbackCarriesContentLightManifest(t *testing.T) {
	t.Parallel()

	const privateText = "PRIVATE ACP FALLBACK CONTENT"
	sections := []contextview.ACPSection{
		{ID: "duplicate", Text: "# Memoh ACP Context\n\n" + privateText},
		{ID: "duplicate", Text: "## Current Runtime\n\nkeep fallback output"},
	}

	markdown, uri, manifest := acpContextViaContextView(context.Background(), nil, sections, "")

	if !strings.Contains(markdown, privateText) || !strings.Contains(markdown, "keep fallback output") {
		t.Fatalf("legacy fallback markdown = %q, want both source sections", markdown)
	}
	if uri != acpContextURI {
		t.Fatalf("uri = %q, want %q", uri, acpContextURI)
	}
	if manifest == nil {
		t.Fatal("legacy fallback must return a lifecycle manifest")
	}
	if manifest.View != contextfrag.ViewACPRuntimePrompt {
		t.Fatalf("fallback manifest view = %q, want %q", manifest.View, contextfrag.ViewACPRuntimePrompt)
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

func TestACPContextSectionsSafeWithHeadingInsideFileExcerpt(t *testing.T) {
	t.Parallel()

	fileBlock := "## Long-Term Memory\n\nEmbedded excerpt from `/data/MEMORY.md`.\n\n```markdown\n# User Memory\nline before heading\n## Preferences\nprefers small patches\n```"
	sections := []contextview.ACPSection{
		{ID: "acp.preamble", Text: "# Memoh ACP Context\n\npreamble"},
		{ID: "acp.section.file.000", Text: fileBlock},
	}
	markdown, _, _ := acpContextViaContextView(context.Background(), nil, sections, "")

	if !strings.Contains(markdown, "line before heading\n## Preferences\nprefers small patches") {
		t.Fatalf("fence content must survive byte-for-byte:\n%s", markdown)
	}
}

func TestFinalizeACPSectionsDropsOversizedDynamicSections(t *testing.T) {
	t.Parallel()

	sections := []contextview.ACPSection{
		{
			ID:     "acp.preamble",
			Text:   "# Memoh ACP Context",
			Budget: contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowKeep},
		},
		{
			ID:     "acp.section.memory-recall",
			Text:   "## Retrieved Memory\n\n" + strings.Repeat("memory ", 20),
			Budget: contextfrag.BudgetPolicy{MaxChars: 32, Overflow: contextfrag.OverflowDrop},
		},
		{
			ID:     "acp.section.memory-hook",
			Text:   "## Memory Hook Context\n\n" + strings.Repeat("hook ", 20),
			Budget: contextfrag.BudgetPolicy{MaxChars: 32, Overflow: contextfrag.OverflowDrop},
		},
		{ID: "acp.section.runtime-notes", Text: "## Runtime Notes\n\nkeep me"},
	}

	markdown := finalizeACPSections(sections)
	if !strings.Contains(markdown, "# Memoh ACP Context") || !strings.Contains(markdown, "keep me") {
		t.Fatalf("fallback dropped retained sections: %q", markdown)
	}
	if strings.Contains(markdown, "Retrieved Memory") || strings.Contains(markdown, "Memory Hook Context") {
		t.Fatalf("fallback retained oversized dynamic sections: %q", markdown)
	}
}

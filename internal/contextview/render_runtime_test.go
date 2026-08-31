package contextview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestRuntimeRenderer_ChatModePassThroughMarkdown(t *testing.T) {
	t.Parallel()

	markdown := "# Memoh Runtime Context\n\n  keep spacing exactly  \n"
	payload, rendered := renderRuntimeChat(t, RuntimeRenderConfig{
		Mode:            RuntimeRenderModeChat,
		ContextMarkdown: markdown,
		ContextURI:      "memoh://custom/context",
	}, nil)

	if payload.ContextMarkdown != markdown {
		t.Fatalf("ContextMarkdown = %q, want %q", payload.ContextMarkdown, markdown)
	}
	if payload.ContextURI != "memoh://custom/context" {
		t.Fatalf("ContextURI = %q, want custom URI", payload.ContextURI)
	}
	wantHash := acpTestSHA256(markdown)
	if payload.ContentHash != wantHash || rendered.ContentHash != wantHash {
		t.Fatalf("hashes = payload:%q rendered:%q, want %q", payload.ContentHash, rendered.ContentHash, wantHash)
	}
}

func TestRuntimeRenderer_ChatModeDefaultURI(t *testing.T) {
	t.Parallel()

	payload, _ := renderRuntimeChat(t, RuntimeRenderConfig{ContextMarkdown: "context"}, nil)
	if payload.ContextURI != "memoh://context/current-turn" {
		t.Fatalf("ContextURI = %q, want default current-turn URI", payload.ContextURI)
	}
}

func TestRuntimeRenderer_ChatModeFragmentsWinOverLegacyMarkdown(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{runtimeRenderTextFrag(
		"runtime.section.000", contextfrag.SlotSystem, contextfrag.KindRuntimeContext, sdk.MessageRoleSystem, "FRAGMENT_DOCUMENT",
	)}
	payload, _ := renderRuntimeChat(t, RuntimeRenderConfig{ContextMarkdown: "LEGACY_DOCUMENT"}, selected)

	if !strings.Contains(payload.ContextMarkdown, "FRAGMENT_DOCUMENT") || strings.Contains(payload.ContextMarkdown, "LEGACY_DOCUMENT") {
		t.Fatalf("ContextMarkdown = %q, want selected fragments only", payload.ContextMarkdown)
	}
}

func TestRuntimeRenderer_ChatModeExcludesCurrentUserFragment(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{
		runtimeRenderTextFrag("runtime.section.000", contextfrag.SlotSystem, contextfrag.KindRuntimeContext, sdk.MessageRoleSystem, "FRAGMENT_DOCUMENT"),
		runtimeRenderTextFrag("current_user.message", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, "latest question"),
	}
	payload, _ := renderRuntimeChat(t, RuntimeRenderConfig{}, selected)

	if strings.Contains(payload.ContextMarkdown, "latest question") {
		t.Fatalf("current user entered context document: %q", payload.ContextMarkdown)
	}
	if !strings.Contains(payload.ContextMarkdown, "FRAGMENT_DOCUMENT") {
		t.Fatalf("context section missing: %q", payload.ContextMarkdown)
	}
}

func TestRuntimeRenderer_ChatModeOnlyCurrentUserFragmentIsError(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{
		runtimeRenderTextFrag("current_user.message", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, "latest question"),
	}
	_, err := (&RuntimeFullContextRenderer{}).Render(context.Background(), RenderInput{
		Intent:    contextfrag.IntentExternalAgentPrompt,
		Target:    contextfrag.RenderRuntimeFullContext,
		Selected:  selected,
		Placement: IdentityPlacer{}.Place(selected, contextfrag.IntentExternalAgentPrompt),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want empty-context failure")
	}
}

func TestRuntimeRenderer_ChatModeEmptyIsError(t *testing.T) {
	t.Parallel()

	_, err := (&RuntimeFullContextRenderer{}).Render(context.Background(), RenderInput{
		Intent: contextfrag.IntentExternalAgentPrompt,
		Target: contextfrag.RenderRuntimeFullContext,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want empty-context failure")
	}
}

func renderRuntimeChat(t *testing.T, cfg RuntimeRenderConfig, selected []contextfrag.ContextFrag) (*RuntimeRenderedPayload, RenderedPayload) {
	t.Helper()
	rendered, err := (&RuntimeFullContextRenderer{Config: cfg}).Render(context.Background(), RenderInput{
		Intent:    contextfrag.IntentExternalAgentPrompt,
		Target:    contextfrag.RenderRuntimeFullContext,
		Selected:  selected,
		Placement: IdentityPlacer{}.Place(selected, contextfrag.IntentExternalAgentPrompt),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	payload, ok := rendered.Data.(*RuntimeRenderedPayload)
	if !ok {
		t.Fatalf("Data type = %T, want *RuntimeRenderedPayload", rendered.Data)
	}
	return payload, rendered
}

func runtimeRenderTextFrag(id string, slot contextfrag.Slot, kind contextfrag.Kind, role sdk.MessageRole, text string) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: id, Kind: kind, Role: role, Slot: slot, Text: text,
		Trust: contextfrag.TrustUser, Scope: contextfrag.Scope{BotID: "bot-1"},
		Source: "runtime_context", Collector: "runtime_sections",
	})
}

func acpTestSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestRuntimeRendererAuditsFinalPrune(t *testing.T) {
	t.Parallel()

	oversized := runtimeRenderTextFrag(
		"runtime.section.huge", contextfrag.SlotSystem, contextfrag.KindSystemPrompt,
		sdk.MessageRoleSystem, strings.Repeat("long section line\n", 8000),
	)
	selected := []contextfrag.ContextFrag{oversized}
	ledger := contextfrag.NewMutationLedger()
	manifest := contextfrag.BuildManifest(selected)
	manifest.Mutations = ledger

	rendered, err := (&RuntimeFullContextRenderer{}).Render(context.Background(), RenderInput{
		Intent:    contextfrag.IntentExternalAgentPrompt,
		Target:    contextfrag.RenderRuntimeFullContext,
		Selected:  selected,
		Placement: IdentityPlacer{}.Place(selected, contextfrag.IntentExternalAgentPrompt),
		Manifest:  &manifest,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	payload, ok := rendered.Data.(*RuntimeRenderedPayload)
	if !ok {
		t.Fatalf("Data type = %T, want *RuntimeRenderedPayload", rendered.Data)
	}
	if len(payload.ContextMarkdown) > 64*1024 {
		t.Fatalf("final markdown = %d bytes, want bounded", len(payload.ContextMarkdown))
	}
	records := ledger.Records()
	found := false
	for _, record := range records {
		if record.Kind == contextfrag.MutationRendererPrune {
			found = true
			if !strings.Contains(record.Detail, "runtime_context_bytes:") {
				t.Fatalf("prune audit detail = %q, want byte accounting", record.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("mutations = %#v, want renderer_prune audit", records)
	}
}

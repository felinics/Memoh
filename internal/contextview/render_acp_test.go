package contextview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestACPRenderer_ChatModePassThroughMarkdown(t *testing.T) {
	t.Parallel()

	markdown := "# Memoh ACP Context\n\n  keep spacing exactly  \n"
	payload, rendered := renderACPChat(t, ACPRenderConfig{
		Mode:            ACPRenderModeChat,
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

func TestACPRenderer_ChatModeDefaultURI(t *testing.T) {
	t.Parallel()

	payload, _ := renderACPChat(t, ACPRenderConfig{ContextMarkdown: "context"}, nil)
	if payload.ContextURI != "memoh://context/current-turn" {
		t.Fatalf("ContextURI = %q, want default current-turn URI", payload.ContextURI)
	}
}

func TestACPRenderer_ChatModeFragmentsWinOverLegacyMarkdown(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{acpRenderTextFrag(
		"acp.section.000", contextfrag.SlotSystem, contextfrag.KindACPContext, sdk.MessageRoleSystem, "FRAGMENT_DOCUMENT",
	)}
	payload, _ := renderACPChat(t, ACPRenderConfig{ContextMarkdown: "LEGACY_DOCUMENT"}, selected)

	if !strings.Contains(payload.ContextMarkdown, "FRAGMENT_DOCUMENT") || strings.Contains(payload.ContextMarkdown, "LEGACY_DOCUMENT") {
		t.Fatalf("ContextMarkdown = %q, want selected fragments only", payload.ContextMarkdown)
	}
}

func TestACPRenderer_ChatModeExcludesCurrentUserFragment(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{
		acpRenderTextFrag("acp.section.000", contextfrag.SlotSystem, contextfrag.KindACPContext, sdk.MessageRoleSystem, "FRAGMENT_DOCUMENT"),
		acpRenderTextFrag("current_user.message", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, "latest question"),
	}
	payload, _ := renderACPChat(t, ACPRenderConfig{}, selected)

	if strings.Contains(payload.ContextMarkdown, "latest question") {
		t.Fatalf("current user entered context document: %q", payload.ContextMarkdown)
	}
	if !strings.Contains(payload.ContextMarkdown, "FRAGMENT_DOCUMENT") {
		t.Fatalf("context section missing: %q", payload.ContextMarkdown)
	}
}

func TestACPRenderer_ChatModeOnlyCurrentUserFragmentIsError(t *testing.T) {
	t.Parallel()

	selected := []contextfrag.ContextFrag{
		acpRenderTextFrag("current_user.message", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, "latest question"),
	}
	_, err := (&ACPFullContextRenderer{}).Render(context.Background(), RenderInput{
		Intent:    contextfrag.IntentACPRuntimePrompt,
		Target:    contextfrag.RenderACPFullContext,
		Selected:  selected,
		Placement: IdentityPlacer{}.Place(selected, contextfrag.IntentACPRuntimePrompt),
	})
	if err == nil {
		t.Fatal("Render() error = nil, want empty-context failure")
	}
}

func TestACPRenderer_ChatModeEmptyIsError(t *testing.T) {
	t.Parallel()

	_, err := (&ACPFullContextRenderer{}).Render(context.Background(), RenderInput{
		Intent: contextfrag.IntentACPRuntimePrompt,
		Target: contextfrag.RenderACPFullContext,
	})
	if err == nil {
		t.Fatal("Render() error = nil, want empty-context failure")
	}
}

func renderACPChat(t *testing.T, cfg ACPRenderConfig, selected []contextfrag.ContextFrag) (*ACPRenderedPayload, RenderedPayload) {
	t.Helper()
	rendered, err := (&ACPFullContextRenderer{Config: cfg}).Render(context.Background(), RenderInput{
		Intent:    contextfrag.IntentACPRuntimePrompt,
		Target:    contextfrag.RenderACPFullContext,
		Selected:  selected,
		Placement: IdentityPlacer{}.Place(selected, contextfrag.IntentACPRuntimePrompt),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	payload, ok := rendered.Data.(*ACPRenderedPayload)
	if !ok {
		t.Fatalf("Data type = %T, want *ACPRenderedPayload", rendered.Data)
	}
	return payload, rendered
}

func acpRenderTextFrag(id string, slot contextfrag.Slot, kind contextfrag.Kind, role sdk.MessageRole, text string) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: id, Kind: kind, Role: role, Slot: slot, Text: text,
		Trust: contextfrag.TrustUser, Scope: contextfrag.Scope{BotID: "bot-1"},
		Source: "acp_context", Collector: "acp_sections",
	})
}

func acpTestSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

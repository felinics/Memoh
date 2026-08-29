package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestHookContextCollectorProducesBoundedWorkspaceFrag(t *testing.T) {
	t.Parallel()

	frags, err := (&HookContextCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{BotID: "bot-1"},
		Config: HookContextConfig{Text: "[Hook Context: before_prompt_build]\nextra guidance"},
	})
	if err != nil {
		t.Fatalf("Collect failed: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("frags = %d, want 1", len(frags))
	}
	frag := frags[0]
	if frag.ID != "hook_context.message" || frag.Kind != contextfrag.KindHookContext {
		t.Fatalf("identity = %q/%s, want hook_context.message/hook_context", frag.ID, frag.Kind)
	}
	if frag.Slot != contextfrag.SlotAfterHistoryBeforeCurrent || frag.Trust != contextfrag.TrustWorkspace || frag.CacheClass != contextfrag.CacheNever {
		t.Fatalf("placement authority = %#v", frag)
	}
	if frag.Budget.Overflow != contextfrag.OverflowDrop || frag.Budget.MaxChars != maxHookContextChars {
		t.Fatalf("budget = %#v, want drop at %d chars", frag.Budget, maxHookContextChars)
	}
	msg := contextfrag.FragMessage(frag)
	if msg == nil || msg.Role != sdk.MessageRoleUser {
		t.Fatalf("hook context renders as a user message: %#v", frag)
	}
}

func TestHookContextCollectorEmptyTextNoFrag(t *testing.T) {
	t.Parallel()

	frags, err := (&HookContextCollector{}).Collect(context.Background(), CollectRequest{
		Config: HookContextConfig{Text: "   "},
	})
	if err != nil || len(frags) != 0 {
		t.Fatalf("empty hook text must produce no frag: %v %d", err, len(frags))
	}
}

func TestApplyProviderRunConfigRendersHookContextBeforeCurrent(t *testing.T) {
	t.Parallel()

	current := 1
	cfg := agentpkg.RunConfig{
		System: "system", Messages: []sdk.Message{sdk.UserMessage("old history"), sdk.UserMessage("current question")},
		ContextCurrentUserMessageIndex: &current, ContextQueryMaterialized: true,
		ContextHookText: "[Hook Context: before_prompt_build]\nextra guidance",
		ContextScope:    contextfrag.Scope{BotID: "bot-1", SessionID: "s1"},
	}
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)

	if len(got.Messages) != 3 || messageText(t, got.Messages[0]) != "old history" ||
		messageText(t, got.Messages[1]) != cfg.ContextHookText || messageText(t, got.Messages[2]) != "current question" {
		t.Fatalf("messages = %#v, want history, hook context, current", got.Messages)
	}
	if strings.Contains(got.System, "extra guidance") || strings.Contains(got.System, "[Hook Context:") {
		t.Fatalf("hook context text must never leak into the system prompt: %q", got.System)
	}
	if !hasContextFragKind(got.ContextFrags, contextfrag.KindHookContext) {
		t.Fatalf("context fragments = %#v, want hook_context", got.ContextFrags)
	}
}

func TestProviderFallbackDropsOversizedHookContext(t *testing.T) {
	t.Parallel()

	current := 0
	duplicate := contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "duplicate", Message: sdk.UserMessage("duplicate"), Kind: contextfrag.KindConversationEvent,
		Slot: contextfrag.SlotHistory,
	})
	got := ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
		System: "system", Messages: []sdk.Message{sdk.UserMessage("current")},
		ContextCurrentUserMessageIndex: &current, ContextQueryMaterialized: true,
		ContextHookText:    strings.Repeat("界", maxHookContextChars+1),
		ContextSourceFrags: []contextfrag.ContextFrag{duplicate, duplicate},
	})
	if got.System != "system" || len(got.Messages) != 1 || messageText(t, got.Messages[0]) != "current" {
		t.Fatalf("oversized fallback hook reached provider: system=%q messages=%#v", got.System, got.Messages)
	}
}

func hasContextFragKind(frags []contextfrag.ContextFrag, kind contextfrag.Kind) bool {
	for _, frag := range frags {
		if frag.Kind == kind {
			return true
		}
	}
	return false
}

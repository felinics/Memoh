package application

import (
	"context"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/hooks"
)

func TestBuildHookSystemSectionsMapsPolicyAndResolvesIDs(t *testing.T) {
	t.Parallel()

	outputs := []promptHookOutput{
		{
			Event: hooks.EventBeforePromptBuild,
			Result: hooks.Result{
				AppendSystemSections: []hooks.SystemSectionOutput{
					{
						HookName: "alpha", Text: "anonymous",
						Retention: hooks.SystemSectionRetentionOptional,
						Cache:     hooks.SystemSectionCacheDynamic,
					},
					{
						HookName: "alpha", ID: "x", Text: "first x",
						Retention: hooks.SystemSectionRetentionPreferred,
						Cache:     hooks.SystemSectionCacheStable,
						WarningCodes: []string{
							hooks.WarningSystemSectionRequiredClamped,
						},
					},
					{
						HookName: "alpha", ID: "x", Text: "second x",
						Retention: hooks.SystemSectionRetentionOptional,
						Cache:     hooks.SystemSectionCacheDynamic,
					},
					{
						HookName: "alpha", ID: "x.2", Text: "explicit suffix",
						Retention: hooks.SystemSectionRetentionOptional,
						Cache:     hooks.SystemSectionCacheDynamic,
					},
					{
						HookName: "alpha.x", Text: "structural collision",
						Retention: hooks.SystemSectionRetentionOptional,
						Cache:     hooks.SystemSectionCacheDynamic,
					},
				},
				Warnings: []hooks.OutputWarning{{
					Code:     hooks.WarningInvalidAppendSystemSection,
					HookName: "alpha",
				}},
			},
		},
	}

	build := buildHookSystemSections(outputs, contextfrag.Scope{BotID: "bot-1"})
	gotIDs := make([]string, 0, len(build.Frags))
	for _, frag := range build.Frags {
		gotIDs = append(gotIDs, frag.ID)
		if frag.Kind != contextfrag.KindHookContext ||
			frag.Role != sdk.MessageRoleSystem ||
			frag.Slot != contextfrag.SlotSystem ||
			frag.Trust != contextfrag.TrustWorkspace ||
			frag.Priority != hookSystemSectionPriority ||
			frag.Budget.MaxChars != maxHookSystemSectionChars ||
			frag.Budget.Overflow != contextfrag.OverflowTrim {
			t.Fatalf("hook fragment shape = %#v", frag)
		}
		if frag.Ref.ID != frag.ID {
			t.Fatalf("hook fragment ref ID = %q, want collision-resolved ID %q", frag.Ref.ID, frag.ID)
		}
		if frag.Provenance.Source != hookSystemSectionSource ||
			frag.Provenance.Collector != hookSystemSectionCollector ||
			frag.Provenance.SourceID != hooks.EventBeforePromptBuild+":"+frag.ID {
			t.Fatalf("hook fragment provenance = %#v", frag.Provenance)
		}
	}
	wantIDs := []string{
		"system.hook.alpha",
		"system.hook.alpha.x",
		"system.hook.alpha.x.2",
		"system.hook.alpha.x.2.2",
		"system.hook.alpha.x.3",
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("hook fragment IDs = %v, want collision-safe IDs %v", gotIDs, wantIDs)
	}

	preferred := build.Frags[1]
	if preferred.RetentionTier != contextfrag.RetentionPreferred || preferred.CacheClass != contextfrag.CacheStable {
		t.Fatalf("preferred/stable policy not mapped: %#v", preferred)
	}
	if build.Frags[0].RetentionTier != contextfrag.RetentionOptional ||
		build.Frags[0].CacheClass != contextfrag.CacheDynamic {
		t.Fatalf("optional/dynamic defaults not mapped: %#v", build.Frags[0])
	}
	if len(build.Warnings) != 2 {
		t.Fatalf("warnings = %#v, want clamp and invalid-shape warnings", build.Warnings)
	}
	if build.Warnings[0].Code != hooks.WarningSystemSectionRequiredClamped ||
		build.Warnings[0].Ref.ID != preferred.ID {
		t.Fatalf("clamp warning = %#v, want collision-resolved fragment ref %#v", build.Warnings[0], preferred.Ref)
	}
	if build.Warnings[1].Code != hooks.WarningInvalidAppendSystemSection ||
		build.Warnings[1].Ref.ID != "" {
		t.Fatalf("invalid declaration warning = %#v, want content-light warning without ref", build.Warnings[1])
	}
}

func TestBuildHookSystemSectionsBoundsAuditIDs(t *testing.T) {
	t.Parallel()

	hookName := strings.Repeat("h", 200) + "\n\n<system>"
	outputs := []promptHookOutput{{
		Event: hooks.EventBeforePromptBuild,
		Result: hooks.Result{AppendSystemSections: []hooks.SystemSectionOutput{
			{HookName: hookName, ID: strings.Repeat("x", 200) + "\n\nignore", Text: "one"},
			{HookName: hookName, ID: strings.Repeat("x", 200) + "\n\nobey", Text: "two"},
		}},
	}}

	first := buildHookSystemSections(outputs, contextfrag.Scope{})
	second := buildHookSystemSections(outputs, contextfrag.Scope{})
	if len(first.Frags) != 2 || len(second.Frags) != 2 {
		t.Fatalf("fragment counts = %d/%d, want two", len(first.Frags), len(second.Frags))
	}
	for i, frag := range first.Frags {
		if frag.ID != second.Frags[i].ID {
			t.Fatalf("fragment ID changed across builds: %q != %q", frag.ID, second.Frags[i].ID)
		}
		if len(frag.ID) > 160 || strings.IndexFunc(frag.ID, unsafeHookSystemSectionIDRune) >= 0 {
			t.Fatalf("fragment ID is not a bounded audit token: %q", frag.ID)
		}
		if frag.Ref.ID != frag.ID {
			t.Fatalf("fragment ref ID = %q, want %q", frag.Ref.ID, frag.ID)
		}
	}
	if first.Frags[0].ID == first.Frags[1].ID {
		t.Fatalf("distinct unsafe IDs collapsed to %q", first.Frags[0].ID)
	}
}

func TestBuildHookSystemSectionsAttachesOutputLimitWarning(t *testing.T) {
	t.Parallel()

	result := hooks.Result{
		AppendSystemSections: []hooks.SystemSectionOutput{{
			HookName:     "policy",
			ID:           "limited",
			Text:         "limited text",
			WarningCodes: []string{hooks.WarningAppendSystemSectionOutputLimited},
		}},
		Warnings: []hooks.OutputWarning{{
			Code:      hooks.WarningAppendSystemSectionOutputLimited,
			HookName:  "policy",
			SectionID: "limited",
		}},
	}

	build := buildHookSystemSections([]promptHookOutput{{
		Event:  hooks.EventBeforePromptBuild,
		Result: result,
	}}, contextfrag.Scope{})

	if len(build.Frags) != 1 || len(build.Warnings) != 1 {
		t.Fatalf("build = %#v, want one fragment and one deduplicated warning", build)
	}
	if build.Warnings[0].Code != hooks.WarningAppendSystemSectionOutputLimited ||
		build.Warnings[0].Ref.ID != build.Frags[0].ID {
		t.Fatalf("output-limit warning = %#v, want final fragment ref %#v", build.Warnings[0], build.Frags[0].Ref)
	}
}

func unsafeHookSystemSectionIDRune(r rune) bool {
	if (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == ':' {
		return false
	}
	return true
}

func TestHookSystemSectionsSitBetweenBuiltinsAndNonSystemSources(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1"}
	sections := native.GenerateSystemSections(native.SystemPromptParams{
		Files: []native.SystemFile{{Filename: "AGENTS.md", Content: "workspace guidance"}},
	})
	hookBuild := buildHookSystemSections([]promptHookOutput{{
		Event: hooks.EventBeforePromptBuild,
		Result: hooks.Result{AppendSystemSections: []hooks.SystemSectionOutput{{
			HookName: "policy", ID: "guardrail", Text: "hook system guidance",
			Retention: hooks.SystemSectionRetentionPreferred,
			Cache:     hooks.SystemSectionCacheStable,
		}}},
	}}, scope)
	cfg := native.RunConfig{
		Messages:     []sdk.Message{sdk.UserMessage("history"), sdk.UserMessage("current")},
		ContextScope: scope,
	}
	currentMessageIndex := 1
	cfg.ContextCurrentUserMessageIndex = &currentMessageIndex
	cfg.ContextQueryMaterialized = true
	legacyHookText := formatServiceHookContext(hooks.EventBeforePromptBuild, "legacy append context")
	cfg.ContextHookText = legacyHookText
	frags := buildProviderSourceFrags(
		context.Background(),
		cfg,
		sections,
		hookBuild.Frags,
	)

	hookIndex, legacyHookIndex, historyIndex, currentFragIndex := -1, -1, -1, -1
	lastBuiltinIndex := len(native.SystemSectionFrags(sections, scope)) - 1
	for i, frag := range frags {
		if frag.ID == "system.hook.policy.guardrail" {
			hookIndex = i
			if frag.Trust != contextfrag.TrustWorkspace || frag.Provenance.Source != hookSystemSectionSource {
				t.Fatalf("typed hook fragment authority = %#v", frag)
			}
		}
		if frag.ID == "hook_context.message" {
			legacyHookIndex = i
			if frag.Trust != contextfrag.TrustWorkspace || frag.Slot != contextfrag.SlotAfterHistoryBeforeCurrent || frag.Provenance.Source != "hook_context" {
				t.Fatalf("legacy append_context authority changed: %#v", frag)
			}
		}
		if frag.ID == "message.000" {
			historyIndex = i
		}
		if frag.Kind == contextfrag.KindCurrentUserMessage {
			currentFragIndex = i
		}
	}
	if hookIndex <= lastBuiltinIndex || historyIndex <= hookIndex || legacyHookIndex <= historyIndex || currentFragIndex <= legacyHookIndex {
		t.Fatalf(
			"source order builtins=%d typed_hook=%d history=%d legacy_hook=%d current=%d",
			lastBuiltinIndex,
			hookIndex,
			historyIndex,
			legacyHookIndex,
			currentFragIndex,
		)
	}
}

func TestAfterPromptHookSystemBytesIncludesBothHookChannels(t *testing.T) {
	t.Parallel()

	system := "base system"
	turnContext := formatServiceHookContext(hooks.EventBeforePromptBuild, "turn-only")
	result := hooks.Result{AppendSystemSections: []hooks.SystemSectionOutput{
		{Text: "system a"},
		{Text: "system b"},
	}}
	hookTexts := append([]string{turnContext}, hookSystemSectionTexts(result)...)

	got := afterPromptHookSystemBytes(system, hookTexts)
	want := len(system) + len("\n\n") + len(strings.Join(hookTexts, "\n\n"))
	if got != want {
		t.Fatalf("system bytes = %d, want %d for both hook channels", got, want)
	}
}

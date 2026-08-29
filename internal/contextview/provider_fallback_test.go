package contextview

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestProviderViewFallbackKeepsLegacyBytesAndAudit(t *testing.T) {
	t.Parallel()
	duplicate := systemTextFrag("duplicate", "source ignored", contextfrag.KindSystemPrompt, 20)
	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	cfg := agentpkg.RunConfig{
		System: "  legacy system \n", Messages: []sdk.Message{sdk.AssistantMessage("history")},
		Query: "  current \n", InlineImages: []sdk.ImagePart{image},
		ContextSourceFrags: []contextfrag.ContextFrag{duplicate, duplicate},
	}
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if got.System != cfg.System {
		t.Fatalf("system = %q, want %q", got.System, cfg.System)
	}
	assertMessagesEqual(t, got.Messages, []sdk.Message{sdk.AssistantMessage("history"), sdk.UserMessage(cfg.Query, image)})
	if got.ContextManifest.CachePlan == nil || got.ContextManifest.Mutations == nil || got.ContextMutations == nil {
		t.Fatalf("manifest = %#v", got.ContextManifest)
	}
	records := got.ContextMutations.Records()
	if len(records) != 2 || records[0].Kind != contextfrag.MutationContextBudgetDisabled ||
		records[1].Kind != contextfrag.MutationContextViewFallback {
		t.Fatalf("records = %#v", records)
	}
}

func TestProviderViewFallbackDropsStaleFragmentOverridesFromAudit(t *testing.T) {
	t.Parallel()
	duplicate := systemTextFrag("duplicate", "source ignored", contextfrag.KindSystemPrompt, 20)
	stale := contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "message.000", Message: sdk.UserMessage("stale override"), Kind: contextfrag.KindConversationSummary,
		Slot: contextfrag.SlotHistory, CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustExternal,
		Source: "compaction_log", Collector: "history_records",
	})
	coverage := contextfrag.NewSummaryCoverage(
		contextfrag.ContextRef{Namespace: "compaction_summary", ID: "summary-1", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable},
		[]contextfrag.ContextRef{{Namespace: "bot_history_message", ID: "row-1", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable}},
	)
	stale.Coverage = &coverage
	cfg := agentpkg.RunConfig{
		System:             "legacy system",
		Messages:           []sdk.Message{sdk.AssistantMessage("actual history")},
		ContextFrags:       []contextfrag.ContextFrag{stale},
		ContextSourceFrags: []contextfrag.ContextFrag{duplicate, duplicate},
	}

	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	rendered := contextfrag.Render(got.ContextFrags)
	if !reflect.DeepEqual(rendered.Messages, got.Messages) {
		t.Fatalf("fallback audit messages = %#v, emitted messages = %#v", rendered.Messages, got.Messages)
	}
	if len(got.ContextManifest.CoverageTrace) != 0 {
		t.Fatalf("fallback audit retained stale coverage: %#v", got.ContextManifest.CoverageTrace)
	}
}

func TestApplyProviderRunConfigAcceptsEmptyProviderInput(t *testing.T) {
	t.Parallel()
	got := ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{})
	if got.System != "" || len(got.Messages) != 0 {
		t.Fatalf("empty provider bytes changed: system %q messages %#v", got.System, got.Messages)
	}
	if got.ContextMutations == nil {
		t.Fatal("empty provider input did not install a mutation ledger")
	}
	if records := got.ContextMutations.Records(); len(records) != 1 ||
		records[0].Kind != contextfrag.MutationContextBudgetDisabled {
		t.Fatalf("empty provider input audit = %#v, want only missing-window classification", records)
	}
}

func TestLegacyMaterializeQuerySplicesToolUsageBeforeWorkspace(t *testing.T) {
	t.Parallel()
	cfg := agentpkg.RunConfig{
		System:           "base\n\n## Workspace instruction files\n\nworkspace",
		ContextToolUsage: "## Tool usage\n\nUSE_TOOLS",
	}
	got := legacyMaterializeQuery(cfg)
	if !strings.Contains(got.System, "## Tool usage") {
		t.Fatalf("system = %q, want tool usage", got.System)
	}
	if strings.Index(got.System, "## Tool usage") > strings.Index(got.System, "## Workspace instruction files") {
		t.Fatalf("system = %q", got.System)
	}
}

func TestLegacyMaterializeQueryPreservesRawMemoryBeforeCurrent(t *testing.T) {
	t.Parallel()
	memory := sdk.UserMessage("<memory>raw & unescaped</memory>")
	cfg := agentpkg.RunConfig{Messages: []sdk.Message{memory}, Query: "current"}
	got := legacyMaterializeQuery(cfg)
	assertMessagesEqual(t, got.Messages, []sdk.Message{memory, sdk.UserMessage("current")})
}

func TestProviderViewFallbackPlacesHookAfterDynamicContextBeforeCurrent(t *testing.T) {
	t.Parallel()

	t.Run("pipeline current before marked memory", func(t *testing.T) {
		currentIndex := 0
		memoryIndex := 1
		out := providerViewFallback(nil, agentpkg.RunConfig{
			Messages: []sdk.Message{
				sdk.UserMessage("pipeline current"),
				sdk.UserMessage("memory recall"),
			},
			ForkContextSourceMessageIDs:    []string{"current-id", "memory-id"},
			ContextHistoryTokenEstimates:   []int{11, 22},
			ContextCurrentUserMessageIndex: &currentIndex,
			ContextMemoryMessageIndex:      &memoryIndex,
			ContextQueryMaterialized:       true,
			ContextHookText:                "workspace hook guidance",
		}, contextfrag.NewMutationLedger(), nil, "build_error", "fallback", nil)

		assertMessagesEqual(t, out.Messages, []sdk.Message{
			sdk.UserMessage("memory recall"),
			sdk.UserMessage("workspace hook guidance"),
			sdk.UserMessage("pipeline current"),
		})
		if out.ContextMemoryMessageIndex == nil || *out.ContextMemoryMessageIndex != 0 ||
			out.ContextCurrentUserMessageIndex == nil || *out.ContextCurrentUserMessageIndex != 2 {
			t.Fatalf("fallback indexes = memory %#v current %#v, want 0 and 2", out.ContextMemoryMessageIndex, out.ContextCurrentUserMessageIndex)
		}
		if !reflect.DeepEqual(out.ForkContextSourceMessageIDs, []string{"memory-id", "", "current-id"}) {
			t.Fatalf("fallback source IDs = %#v", out.ForkContextSourceMessageIDs)
		}
		if !reflect.DeepEqual(out.ContextHistoryTokenEstimates, []int{22, 0, 11}) {
			t.Fatalf("fallback token estimates = %#v", out.ContextHistoryTokenEstimates)
		}
	})

	t.Run("discuss current recovered from source fragment", func(t *testing.T) {
		current := contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID:      "discuss.message.001",
			Message: sdk.UserMessage("latest discuss user"),
			Kind:    contextfrag.KindCurrentUserMessage,
			Slot:    contextfrag.SlotHistory,
			Index:   1,
		})
		out := providerViewFallback(nil, agentpkg.RunConfig{
			Messages: []sdk.Message{
				sdk.AssistantMessage("previous answer"),
				sdk.UserMessage("latest discuss user"),
			},
			ContextSourceFrags: []contextfrag.ContextFrag{current},
			ContextHookText:    "discuss hook guidance",
		}, contextfrag.NewMutationLedger(), nil, "build_error", "fallback", nil)

		assertMessagesEqual(t, out.Messages, []sdk.Message{
			sdk.AssistantMessage("previous answer"),
			sdk.UserMessage("discuss hook guidance"),
			sdk.UserMessage("latest discuss user"),
		})
		if out.ContextCurrentUserMessageIndex == nil || *out.ContextCurrentUserMessageIndex != 2 {
			t.Fatalf("fallback current index = %#v, want 2", out.ContextCurrentUserMessageIndex)
		}
	})
}

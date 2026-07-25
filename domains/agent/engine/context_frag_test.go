package engine

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/engine/background"
	"github.com/memohai/memoh/domains/agent/tool"
)

func TestRefreshContextFragOmitsMaterializedQuery(t *testing.T) {
	t.Parallel()

	cfg := RunConfig{
		System:                   "base system",
		Query:                    "current user",
		Messages:                 []sdk.Message{sdk.UserMessage("current user")},
		ContextQueryMaterialized: true,
	}

	cfg = cfg.RefreshContextFrag()

	if manifestHasAgentKind(cfg.ContextManifest, fragment.KindCurrentUserMessage) {
		t.Fatalf("manifest should not include pending current user query after it was materialized: %#v", cfg.ContextManifest.Items)
	}
	if cfg.ContextManifest.Counts.Messages != 1 {
		t.Fatalf("manifest message count = %d, want 1", cfg.ContextManifest.Counts.Messages)
	}
}

func TestRefreshContextFragOmitsMaterializedInlineImages(t *testing.T) {
	t.Parallel()

	image := sdk.ImagePart{Image: "data:image/png;base64,abc", MediaType: "image/png"}
	cfg := RunConfig{
		System:                   "base system",
		Query:                    "current user",
		Messages:                 []sdk.Message{sdk.UserMessage("current user", image)},
		InlineImages:             []sdk.ImagePart{image},
		ContextQueryMaterialized: true,
	}

	cfg = cfg.RefreshContextFrag()

	if cfg.ContextManifest.Counts.Images != 1 {
		t.Fatalf("manifest image count = %d, want only materialized message image: %#v", cfg.ContextManifest.Counts.Images, cfg.ContextManifest.Items)
	}
	rendered := fragment.Render(cfg.ContextFrags)
	if len(rendered.InlineImages) != 0 {
		t.Fatalf("rendered inline images = %#v, want images only inside materialized message", rendered.InlineImages)
	}
}

func TestRefreshContextFragMarksToolUsageBeforeWorkspaceInstructions(t *testing.T) {
	t.Parallel()

	cfg := RunConfig{
		System: appendToolUsageToSystem(
			"base system\n\n## Workspace instruction files\n\nworkspace text",
			"## Tool usage\n\nusage text",
		),
		ContextToolUsage: "## Tool usage\n\nusage text",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
	}

	cfg = cfg.RefreshContextFrag()

	toolIndex := manifestAgentKindIndex(cfg.ContextManifest, fragment.KindToolUsage)
	workspaceIndex := manifestAgentKindIndex(cfg.ContextManifest, fragment.KindWorkspaceInstruction)
	if toolIndex < 0 {
		t.Fatalf("manifest missing tool usage item: %#v", cfg.ContextManifest.Items)
	}
	if workspaceIndex < 0 {
		t.Fatalf("manifest missing workspace instruction item: %#v", cfg.ContextManifest.Items)
	}
	if toolIndex > workspaceIndex {
		t.Fatalf("tool usage manifest index = %d, workspace index = %d; want tool usage before workspace", toolIndex, workspaceIndex)
	}
	rendered := fragment.Render(cfg.ContextFrags)
	if rendered.System != cfg.System {
		t.Fatalf("rendered system = %q, want %q", rendered.System, cfg.System)
	}
}

func TestSpawnRunConfigCarriesContextScopeAndMaterializedQuery(t *testing.T) {
	t.Parallel()

	rc := runConfigFromSpawnRunConfig(tool.SpawnRunConfig{
		Query:    "do the task",
		Messages: []sdk.Message{sdk.UserMessage("history")},
		Identity: tool.SpawnIdentity{
			BotID:             "bot-1",
			ChatID:            "chat-1",
			SessionID:         "child-1",
			ChannelIdentityID: "identity-1",
			CurrentPlatform:   "telegram",
			IsSubagent:        true,
		},
	})

	if !rc.ContextQueryMaterialized {
		t.Fatal("spawn query should be marked materialized because it is appended to Messages")
	}
	rc = rc.RefreshContextFrag()
	if manifestHasAgentKind(rc.ContextManifest, fragment.KindCurrentUserMessage) {
		t.Fatalf("manifest should not include duplicate pending current user query: %#v", rc.ContextManifest.Items)
	}
	if rc.ContextManifest.Counts.Messages != 2 {
		t.Fatalf("manifest message count = %d, want history + materialized query", rc.ContextManifest.Counts.Messages)
	}
	for _, item := range rc.ContextManifest.Items {
		if item.Scope.BotID != "bot-1" || item.Scope.SessionID != "child-1" || item.Scope.ChannelIdentityID != "identity-1" {
			t.Fatalf("manifest item lost subagent scope: %#v", item.Scope)
		}
	}
}

func TestRefreshContextFragWithDynamicMutatorsMarksPreProviderBoundary(t *testing.T) {
	t.Parallel()

	injectCh := make(chan InjectMessage)
	cfg := RunConfig{
		System:            "base system",
		Messages:          []sdk.Message{sdk.UserMessage("hi")},
		InjectCh:          injectCh,
		BackgroundManager: background.New(nil),
	}
	cfg = cfg.RefreshContextFragWithDynamicMutators(true, true, true)

	if cfg.ContextManifest.View != fragment.ViewRunConfigPreProvider {
		t.Fatalf("manifest view = %q, want %q", cfg.ContextManifest.View, fragment.ViewRunConfigPreProvider)
	}
	for _, want := range []fragment.DynamicMutator{
		fragment.DynamicMutatorInjectCh,
		fragment.DynamicMutatorReadMedia,
		fragment.DynamicMutatorBeforeModelCallHook,
		fragment.DynamicMutatorBackgroundSummary,
		fragment.DynamicMutatorMidTaskPrune,
	} {
		if !manifestHasMutator(cfg.ContextManifest, want) {
			t.Fatalf("manifest dynamic mutators = %#v, want %q", cfg.ContextManifest.DynamicMutators, want)
		}
	}
}

func manifestHasAgentKind(manifest fragment.Manifest, kind fragment.Kind) bool {
	return manifestAgentKindIndex(manifest, kind) >= 0
}

func manifestHasMutator(manifest fragment.Manifest, want fragment.DynamicMutator) bool {
	for _, got := range manifest.DynamicMutators {
		if got == want {
			return true
		}
	}
	return false
}

func manifestAgentKindIndex(manifest fragment.Manifest, kind fragment.Kind) int {
	for i, item := range manifest.Items {
		if item.Kind == kind {
			return i
		}
	}
	return -1
}

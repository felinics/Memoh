package native

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	tools "github.com/felinics/memoh/internal/agent/tool"
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

	if manifestHasAgentKind(cfg.ContextManifest, contextfrag.KindCurrentUserMessage) {
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
	rendered := contextfrag.Render(cfg.ContextFrags)
	if len(rendered.InlineImages) != 0 {
		t.Fatalf("rendered inline images = %#v, want images only inside materialized message", rendered.InlineImages)
	}
}

func TestRefreshContextFragMarksMaterializedCurrentMessage(t *testing.T) {
	t.Parallel()
	index := 1
	cfg := RunConfig{
		System:                         "base system",
		Messages:                       []sdk.Message{sdk.AssistantMessage("history"), sdk.UserMessage("current")},
		ContextCurrentUserMessageIndex: &index,
		ContextQueryMaterialized:       true,
	}
	cfg = cfg.RefreshContextFrag()
	if !manifestHasAgentKind(cfg.ContextManifest, contextfrag.KindCurrentUserMessage) {
		t.Fatalf("manifest items = %#v", cfg.ContextManifest.Items)
	}
	if cfg.ContextFrags[index+1].Slot != contextfrag.SlotCurrentUser {
		t.Fatalf("current fragment = %#v", cfg.ContextFrags[index+1])
	}
}

func TestRefreshContextFragKeepsMemoryDistinctFromCurrentMessage(t *testing.T) {
	t.Parallel()
	currentIndex := 0
	memoryIndex := 1
	cfg := RunConfig{
		Messages:                       []sdk.Message{sdk.UserMessage("current"), sdk.UserMessage("memory recall")},
		ContextCurrentUserMessageIndex: &currentIndex,
		ContextMemoryMessageIndex:      &memoryIndex,
		ContextQueryMaterialized:       true,
	}

	cfg = cfg.RefreshContextFrag()
	if cfg.ContextFrags[currentIndex].Kind != contextfrag.KindCurrentUserMessage || cfg.ContextFrags[currentIndex].Slot != contextfrag.SlotCurrentUser {
		t.Fatalf("current fragment = %#v", cfg.ContextFrags[currentIndex])
	}
	if cfg.ContextFrags[memoryIndex].Kind != contextfrag.KindMemoryRecall || cfg.ContextFrags[memoryIndex].Slot != contextfrag.SlotHistory || cfg.ContextFrags[memoryIndex].CacheClass != contextfrag.CacheNever {
		t.Fatalf("memory fragment = %#v", cfg.ContextFrags[memoryIndex])
	}
}

func TestRefreshContextFragPreservesProviderAccounting(t *testing.T) {
	t.Parallel()
	ledger := contextfrag.NewMutationLedger()
	holder := contextfrag.NewLifecycleHolder()
	plan := contextfrag.CachePlan{StablePrefixHash: "prefix", StableMessageCount: 2}
	budgetPlan := contextfrag.ContextBudgetPlan{Window: 1000, HistoryBudget: 400}
	selection := contextfrag.SelectionTrace{Selected: 1, Dropped: 1, DropReasons: map[string]int{"history_budget": 1}}
	decisions := []contextfrag.SelectionDecision{{ID: "old", Decision: contextfrag.DecisionDropped, Reason: "history_budget"}}
	cfg := RunConfig{
		System:          "base system",
		Messages:        []sdk.Message{sdk.UserMessage("hi")},
		ContextToolDefs: []contextfrag.ToolDefAccounting{{Provider: "native", Name: "read", Bytes: 40, TokenEstimate: 10}},
		ContextManifest: contextfrag.Manifest{
			CachePlan:          &plan,
			Mutations:          ledger,
			BudgetPlan:         &budgetPlan,
			Selection:          &selection,
			SelectionDecisions: decisions,
		},
		ContextLifecycle: holder,
	}
	cfg = cfg.RefreshContextFrag()
	if cfg.ContextManifest.CachePlan == nil || cfg.ContextManifest.CachePlan.StablePrefixHash != "prefix" || cfg.ContextManifest.Mutations != ledger {
		t.Fatalf("manifest accounting = %#v", cfg.ContextManifest)
	}
	if len(cfg.ContextManifest.ToolDefs) != 1 || cfg.ContextManifest.ToolDefs[0].Name != "read" {
		t.Fatalf("tool definitions = %#v", cfg.ContextManifest.ToolDefs)
	}
	if cfg.ContextManifest.BudgetPlan == nil || cfg.ContextManifest.BudgetPlan.HistoryBudget != 400 {
		t.Fatalf("budget plan = %#v", cfg.ContextManifest.BudgetPlan)
	}
	if cfg.ContextManifest.Selection == nil || cfg.ContextManifest.Selection.DropReasons["history_budget"] != 1 {
		t.Fatalf("selection = %#v", cfg.ContextManifest.Selection)
	}
	if len(cfg.ContextManifest.SelectionDecisions) != 1 || cfg.ContextManifest.SelectionDecisions[0].ID != "old" {
		t.Fatalf("selection decisions = %#v", cfg.ContextManifest.SelectionDecisions)
	}
	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.BudgetPlan == nil || snapshot.Selection.DropReasons["history_budget"] != 1 {
		t.Fatalf("lifecycle snapshot = %#v, %v", snapshot, ok)
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

	toolIndex := manifestAgentKindIndex(cfg.ContextManifest, contextfrag.KindToolUsage)
	workspaceIndex := manifestAgentKindIndex(cfg.ContextManifest, contextfrag.KindWorkspaceInstruction)
	if toolIndex < 0 {
		t.Fatalf("manifest missing tool usage item: %#v", cfg.ContextManifest.Items)
	}
	if workspaceIndex < 0 {
		t.Fatalf("manifest missing workspace instruction item: %#v", cfg.ContextManifest.Items)
	}
	if toolIndex > workspaceIndex {
		t.Fatalf("tool usage manifest index = %d, workspace index = %d; want tool usage before workspace", toolIndex, workspaceIndex)
	}
	rendered := contextfrag.Render(cfg.ContextFrags)
	if rendered.System != cfg.System {
		t.Fatalf("rendered system = %q, want %q", rendered.System, cfg.System)
	}
}

func TestSpawnRunConfigCarriesContextScopeAndMaterializedQuery(t *testing.T) {
	t.Parallel()

	rc := runConfigFromSpawnRunConfig(tools.SpawnRunConfig{
		Query:    "do the task",
		Messages: []sdk.Message{sdk.UserMessage("history")},
		Identity: tools.SpawnIdentity{
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
	if !manifestHasAgentKind(rc.ContextManifest, contextfrag.KindCurrentUserMessage) {
		t.Fatalf("manifest should classify the materialized query as current user: %#v", rc.ContextManifest.Items)
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

	if cfg.ContextManifest.View != contextfrag.ViewRunConfigPreProvider {
		t.Fatalf("manifest view = %q, want %q", cfg.ContextManifest.View, contextfrag.ViewRunConfigPreProvider)
	}
	for _, want := range []contextfrag.DynamicMutator{
		contextfrag.DynamicMutatorInjectCh,
		contextfrag.DynamicMutatorReadMedia,
		contextfrag.DynamicMutatorBeforeModelCallHook,
		contextfrag.DynamicMutatorBackgroundSummary,
	} {
		if !manifestHasMutator(cfg.ContextManifest, want) {
			t.Fatalf("manifest dynamic mutators = %#v, want %q", cfg.ContextManifest.DynamicMutators, want)
		}
	}
}

func manifestHasAgentKind(manifest contextfrag.Manifest, kind contextfrag.Kind) bool {
	return manifestAgentKindIndex(manifest, kind) >= 0
}

func manifestHasMutator(manifest contextfrag.Manifest, want contextfrag.DynamicMutator) bool {
	for _, got := range manifest.DynamicMutators {
		if got == want {
			return true
		}
	}
	return false
}

func manifestAgentKindIndex(manifest contextfrag.Manifest, kind contextfrag.Kind) int {
	for i, item := range manifest.Items {
		if item.Kind == kind {
			return i
		}
	}
	return -1
}

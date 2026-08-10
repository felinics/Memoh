package contextview

import (
	"context"
	"reflect"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	agentpkg "github.com/memohai/memoh/internal/agent/runtime/native"
)

func prefixSystemFrags() []contextfrag.ContextFrag {
	return []contextfrag.ContextFrag{
		systemTextFrag("system.prompt", "base", contextfrag.KindSystemPrompt, 20),
		systemTextFrag("system.workspace", "workspace", contextfrag.KindWorkspaceInstruction, 50),
	}
}

func TestApplyProviderRunConfigCrossTurnPrefixStable(t *testing.T) {
	t.Parallel()
	turn1Frags := append(prefixSystemFrags(), stableHistoryMessageFrag("message.000", sdk.UserMessage("history")), currentMessageFrag("message.001", "q1"))
	turn2Frags := append(prefixSystemFrags(),
		stableHistoryMessageFrag("message.000", sdk.UserMessage("history")),
		stableHistoryMessageFrag("message.001", sdk.UserMessage("q1")),
		stableHistoryMessageFrag("message.002", sdk.AssistantMessage("a1")),
		currentMessageFrag("message.003", "q2"),
	)
	turn1 := ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{ContextSourceFrags: turn1Frags})
	turn2 := ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{ContextSourceFrags: turn2Frags})
	if turn1.System != turn2.System || !reflect.DeepEqual(turn1.Messages, turn2.Messages[:len(turn1.Messages)]) {
		t.Fatalf("turn1 = %#v, turn2 = %#v", turn1, turn2)
	}
}

func TestApplyProviderRunConfigStablePrefixIgnoresTurnLocalScope(t *testing.T) {
	t.Parallel()
	sections := []agentpkg.SystemSection{{
		ID: "system.prompt", Kind: contextfrag.KindSystemPrompt, Priority: 20, Text: "same system",
	}}
	messages := []sdk.Message{sdk.UserMessage("same history"), sdk.UserMessage("same current")}
	currentIndex := 1
	build := func(scope contextfrag.Scope) agentpkg.RunConfig {
		cfg := agentpkg.RunConfig{
			Messages: messages, ContextCurrentUserMessageIndex: &currentIndex, ContextQueryMaterialized: true,
			ContextScope: scope, ContextToolUsage: "same tool usage",
		}
		cfg.ContextSourceFrags = agentpkg.SystemSectionFrags(sections, scope)
		cfg.ContextSourceFrags = append(cfg.ContextSourceFrags, CollectNonSystemProviderSourceFrags(context.Background(), cfg)...)
		return ApplyProviderRunConfig(context.Background(), nil, cfg)
	}
	firstScope := contextfrag.Scope{
		BotID: "bot-1", ChatID: "chat-1", SessionID: "session-1",
		CurrentMessageID: "telegram-msg-1001", EventID: "event-001",
	}
	secondScope := contextfrag.Scope{
		BotID: "bot-1", ChatID: "chat-1", SessionID: "session-1",
		CurrentMessageID: "telegram-msg-1002", EventID: "event-002",
	}
	first := build(firstScope)
	second := build(secondScope)

	if first.System != second.System || !reflect.DeepEqual(first.Messages, second.Messages) {
		t.Fatalf("provider bytes differ: first system %q messages %#v, second system %q messages %#v", first.System, first.Messages, second.System, second.Messages)
	}
	if first.ContextCachePlan.StablePrefixHash == "" || first.ContextCachePlan.StablePrefixHash != second.ContextCachePlan.StablePrefixHash {
		t.Fatalf("stable prefix hashes = %q and %q", first.ContextCachePlan.StablePrefixHash, second.ContextCachePlan.StablePrefixHash)
	}
	for _, item := range first.ContextManifest.Items {
		if item.Scope.CurrentMessageID != firstScope.CurrentMessageID || item.Scope.EventID != firstScope.EventID {
			t.Fatalf("first manifest lost turn scope: %#v", item.Scope)
		}
	}
	for _, item := range second.ContextManifest.Items {
		if item.Scope.CurrentMessageID != secondScope.CurrentMessageID || item.Scope.EventID != secondScope.EventID {
			t.Fatalf("second manifest lost turn scope: %#v", item.Scope)
		}
	}
}

func TestApplyProviderRunConfigImageOnlyCurrentMovesStableMessageBoundary(t *testing.T) {
	t.Parallel()
	system := systemTextFrag("system.prompt", "stable system", contextfrag.KindSystemPrompt, 20)
	history := stableHistoryMessageFrag("message.000", sdk.UserMessage("history"))
	build := func(imageData string) agentpkg.RunConfig {
		image := contextfrag.ImageFrag("current_user.images", []sdk.ImagePart{{
			Image: imageData, MediaType: "image/png",
		}}, contextfrag.Scope{}, contextfrag.SourceRunConfig)
		return ApplyProviderRunConfig(context.Background(), nil, agentpkg.RunConfig{
			ContextSourceFrags: []contextfrag.ContextFrag{system, history, image},
		})
	}
	first := build("data:image/png;base64,first")
	second := build("data:image/png;base64,second")

	if reflect.DeepEqual(first.Messages, second.Messages) {
		t.Fatalf("image-only current input did not change provider messages: %#v", first.Messages)
	}
	if first.ContextCachePlan.StableMessageCount != 0 || second.ContextCachePlan.StableMessageCount != 0 {
		t.Fatalf("stable message counts = %d and %d, want image target excluded", first.ContextCachePlan.StableMessageCount, second.ContextCachePlan.StableMessageCount)
	}
	wantHash := stablePrefixHash([]contextfrag.ContextFrag{system})
	if wantHash == "" || first.ContextCachePlan.StablePrefixHash != wantHash || second.ContextCachePlan.StablePrefixHash != wantHash {
		t.Fatalf("stable hashes = %q and %q, want system-only %q", first.ContextCachePlan.StablePrefixHash, second.ContextCachePlan.StablePrefixHash, wantHash)
	}
}

func TestApplyProviderRunConfigMemoryAndHookIsolation(t *testing.T) {
	t.Parallel()
	build := func(memory, hook string) agentpkg.RunConfig {
		frags := prefixSystemFrags()
		if hook != "" {
			hookFrags, err := (&HookContextCollector{}).Collect(context.Background(), CollectRequest{Config: HookContextConfig{Text: hook}})
			if err != nil {
				t.Fatal(err)
			}
			frags = append(frags, hookFrags...)
		}
		frags = append(frags, stableHistoryMessageFrag("history", sdk.UserMessage("history")))
		if memory != "" {
			memoryFrags, err := (&MemoryContextCollector{}).Collect(context.Background(), CollectRequest{Config: MemoryContextConfig{Text: memory}})
			if err != nil {
				t.Fatal(err)
			}
			frags = append(frags, memoryFrags...)
		}
		frags = append(frags, currentMessageFrag("current", "question"))
		return agentpkg.RunConfig{ContextSourceFrags: frags}
	}
	first := ApplyProviderRunConfig(context.Background(), nil, build("memory one", "hook one"))
	second := ApplyProviderRunConfig(context.Background(), nil, build("memory two", "hook two"))
	if first.ContextCachePlan.StablePrefixHash != second.ContextCachePlan.StablePrefixHash {
		t.Fatalf("stable hashes = %q, %q", first.ContextCachePlan.StablePrefixHash, second.ContextCachePlan.StablePrefixHash)
	}
	if first.System == second.System {
		t.Fatal("legacy prompt hook bytes must remain in System")
	}
	if first.Messages[1].Content[0].(sdk.TextPart).Text != "memory one" || second.Messages[1].Content[0].(sdk.TextPart).Text != "memory two" {
		t.Fatalf("messages = %#v / %#v", first.Messages, second.Messages)
	}
	if first.ContextCachePlan.StableMessageCount != 0 {
		t.Fatalf("stable message count = %d, want volatile hook to end prefix", first.ContextCachePlan.StableMessageCount)
	}
}

func TestApplyProviderRunConfigSameTurnIdempotent(t *testing.T) {
	t.Parallel()
	cfg := fragsFirstFixture()
	first := ApplyProviderRunConfig(context.Background(), nil, cfg)
	second := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if first.System != second.System || !reflect.DeepEqual(first.Messages, second.Messages) || !reflect.DeepEqual(first.ContextManifest.Items, second.ContextManifest.Items) {
		t.Fatalf("first = %#v, second = %#v", first, second)
	}
}

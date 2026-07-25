package fragment

import (
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"
)

func TestCompileRendersLegacyFieldsAndSplitsToolUsage(t *testing.T) {
	t.Parallel()

	system := "base system\n\n## Tool usage\n\nuse the right tool\n\n## Workspace instruction files\n\nAGENTS.md"
	messages := []sdk.Message{
		sdk.UserMessage("history"),
		sdk.AssistantMessage("reply"),
	}
	images := []sdk.ImagePart{{Image: "data:image/png;base64,abc", MediaType: "image/png"}}
	scope := Scope{
		BotID:            "bot-1",
		ChatID:           "chat-1",
		SessionID:        "sess-1",
		CurrentMessageID: "msg-1",
		EventID:          "evt-1",
		Attention:        []AttentionReason{AttentionReply},
	}

	got := Compile(CompileInput{
		Source:       "test",
		Scope:        scope,
		System:       system,
		Messages:     messages,
		Query:        "current",
		InlineImages: images,
		ToolUsage:    "## Tool usage\n\nuse the right tool",
		DynamicMutators: []DynamicMutator{
			DynamicMutatorReadMedia,
			DynamicMutatorReadMedia,
			DynamicMutatorBeforeModelCallHook,
		},
	})

	if got.Manifest.View != ViewRunConfigPreProvider {
		t.Fatalf("manifest view = %q, want %q", got.Manifest.View, ViewRunConfigPreProvider)
	}
	if len(got.Manifest.DynamicMutators) != 2 ||
		got.Manifest.DynamicMutators[0] != DynamicMutatorReadMedia ||
		got.Manifest.DynamicMutators[1] != DynamicMutatorBeforeModelCallHook {
		t.Fatalf("dynamic mutators = %#v", got.Manifest.DynamicMutators)
	}
	if got.System != system {
		t.Fatalf("rendered system = %q, want %q", got.System, system)
	}
	if got.Query != "current" {
		t.Fatalf("rendered query = %q, want current", got.Query)
	}
	if len(got.Messages) != len(messages) {
		t.Fatalf("rendered messages = %d, want %d", len(got.Messages), len(messages))
	}
	if len(got.InlineImages) != 1 || got.InlineImages[0].Image != images[0].Image {
		t.Fatalf("rendered images = %#v, want %#v", got.InlineImages, images)
	}
	if !manifestHasKind(got.Manifest, KindToolUsage) {
		t.Fatalf("manifest missing tool usage item: %#v", got.Manifest.Items)
	}
	if !manifestHasKind(got.Manifest, KindWorkspaceInstruction) {
		t.Fatalf("manifest missing workspace instruction item: %#v", got.Manifest.Items)
	}
	for _, item := range got.Manifest.Items {
		if item.Scope.EventID != "evt-1" || item.Scope.CurrentMessageID != "msg-1" {
			t.Fatalf("manifest item lost IM scope: %#v", item.Scope)
		}
	}
}

func TestCompileDoesNotInferToolUsageFromPlainSystemText(t *testing.T) {
	t.Parallel()

	got := Compile(CompileInput{
		System: "base system\n\n## Workspace instruction files\n\n## Tool usage\n\ntext from AGENTS.md",
	})

	if manifestHasKind(got.Manifest, KindToolUsage) {
		t.Fatalf("manifest should not infer tool usage from arbitrary system text: %#v", got.Manifest.Items)
	}
}

func manifestHasKind(manifest Manifest, kind Kind) bool {
	for _, item := range manifest.Items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

package acp

import (
	"testing"

	"github.com/memohai/memoh/internal/agent/runtime/acp/client"
)

func TestPromptInputApplyContextCopiesCurrentTurnProjection(t *testing.T) {
	t.Parallel()

	images := []client.PromptImage{{Data: "abc", MimeType: "image/png"}}
	references := []string{"/data/image.png"}
	input := PromptInput{BotID: "bot-1", ModelID: "model-1"}
	input.ApplyContext("inspect", images, references, true, "memoh://context/current-turn", "# Context")

	images[0].Data = "changed"
	references[0] = "changed"
	if input.Prompt != "inspect" || input.ContextURI != "memoh://context/current-turn" || input.ContextMarkdown != "# Context" {
		t.Fatalf("context scalars = %#v", input)
	}
	if input.Images[0].Data != "abc" || input.AttachmentReferences[0] != "/data/image.png" {
		t.Fatalf("context slices were not copied: %#v", input)
	}
	if !input.CanFallbackImagesToFiles {
		t.Fatal("CanFallbackImagesToFiles = false, want true")
	}
}

package message

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/markdownmedia"
	"github.com/felinics/memoh/internal/media"
)

func TestMarkdownPublicationUsesStoredAssistantText(t *testing.T) {
	const source = "![result](/data/result.png)"
	calls := 0
	s := &DBService{resolveMarkdownMedia: func(_ context.Context, botID, text string) []markdownmedia.Binding {
		calls++
		if text != source || botID != "bot" {
			t.Fatalf("unexpected source %q bot %q", text, botID)
		}
		return []markdownmedia.Binding{{Reference: markdownmedia.Parse(text)[0], Asset: media.Asset{BotID: botID, ContentHash: "hash", Mime: "image/png"}}}
	}}
	input := PersistInput{BotID: "bot", Role: "assistant", SessionMode: "chat", RunID: "run", Content: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"![result](/data/result.png)"}]}`)}
	prepared := s.prepareMarkdownMedia(context.Background(), input)
	if len(prepared.Assets) != 1 || prepared.Assets[0].Role != "markdown" || prepared.Metadata["markdown_media_source"] != source {
		t.Fatalf("prepared=%+v", prepared)
	}
	if input.Metadata != nil || len(input.Assets) != 0 {
		t.Fatal("mutated caller input")
	}
	_ = s.prepareMarkdownMedia(context.Background(), prepared)
	if calls != 1 {
		t.Fatal("prepared publication resolved again")
	}
}

func TestPrivateAndInterruptedTextDoesNotArchive(t *testing.T) {
	s := &DBService{resolveMarkdownMedia: func(context.Context, string, string) []markdownmedia.Binding {
		t.Fatal("unexpected archive")
		return nil
	}}
	for _, input := range []PersistInput{
		{Role: "assistant", SessionMode: "discuss", DisplayText: "![private](/data/private.png)"},
		{Role: "assistant", SessionMode: "chat", DisplayText: "![partial](/data/partial.png)", Metadata: map[string]any{AgentStepInterruptedMetadataKey: true}},
		{Role: "tool", SessionMode: "chat", DisplayText: "![tool](/data/tool.png)"},
	} {
		_ = s.prepareMarkdownMedia(context.Background(), input)
	}
}

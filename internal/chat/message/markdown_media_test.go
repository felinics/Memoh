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
	s := &DBService{resolveMarkdownMedia: func(_ context.Context, botID, text string, _ markdownmedia.Options) []markdownmedia.Binding {
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
	s := &DBService{resolveMarkdownMedia: func(context.Context, string, string, markdownmedia.Options) []markdownmedia.Binding {
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

func TestMarkdownPublicationJoinsVisibleTextParts(t *testing.T) {
	source := "Introduction\n[report](/data/report.txt)"
	s := &DBService{resolveMarkdownMedia: func(_ context.Context, botID, text string, _ markdownmedia.Options) []markdownmedia.Binding {
		if text != source {
			t.Fatalf("source=%q", text)
		}
		return []markdownmedia.Binding{{Reference: markdownmedia.Parse(text)[0], Asset: media.Asset{BotID: botID, ContentHash: "hash"}}}
	}}
	input := PersistInput{BotID: "bot", Role: "assistant", SessionMode: "chat", Content: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":" Introduction "},{"type":"reasoning","text":"private"},{"type":"text","text":"[report](/data/report.txt)"}]}`)}
	prepared := s.prepareMarkdownMedia(context.Background(), input)
	if prepared.Metadata["markdown_media_source"] != source || len(prepared.Assets) != 1 {
		t.Fatalf("prepared=%+v", prepared)
	}
}

func TestOnlyIMSessionsArchiveLinkedFiles(t *testing.T) {
	for platform, want := range map[string]bool{"": false, "web": false, "cli": false, "telegram": true, "discord": true} {
		var got markdownmedia.Options
		s := &DBService{resolveMarkdownMedia: func(_ context.Context, _, text string, opts markdownmedia.Options) []markdownmedia.Binding {
			got = opts
			return []markdownmedia.Binding{{Reference: markdownmedia.Parse(text)[0], Asset: media.Asset{ContentHash: "hash"}}}
		}}
		input := PersistInput{BotID: "bot", Role: "assistant", SessionMode: "chat", DisplayText: "[查看报告](/data/out/report.pdf)", Metadata: map[string]any{"platform": platform}}
		prepared := s.prepareMarkdownMedia(context.Background(), input)
		if got.Links != want {
			t.Errorf("platform %q: Links=%v want %v", platform, got.Links, want)
		}
		if len(prepared.Assets) != 1 || prepared.Assets[0].Name != "report.pdf" {
			t.Errorf("platform %q: assets=%+v", platform, prepared.Assets)
		}
	}
}

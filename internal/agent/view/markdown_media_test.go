package view

import (
	"encoding/json"
	"strings"
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/markdownmedia"
	"github.com/felinics/memoh/internal/media"
)

func TestHistoryRendersArchivedMarkdownFromLazyMetadata(t *testing.T) {
	source := "before ![图](/data/a.png) after"
	binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{BotID: "bot-1", ContentHash: strings.Repeat("a", 64)}}
	metadata, _ := json.Marshal(map[string]any{markdownmedia.MetadataKey: []markdownmedia.Binding{binding}})
	content, _ := json.Marshal(map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": source}}})
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{ID: "assistant-1", Role: "assistant", Content: content, RawMetadata: metadata}})
	data, _ := json.Marshal(turns)
	if strings.Contains(string(data), "/data/a.png") || !strings.Contains(string(data), "/bots/bot-1/media/"+strings.Repeat("a", 64)) {
		t.Fatalf("history=%s", data)
	}
}

func TestHistoryRendersMediaBeforeNormalizingSource(t *testing.T) {
	for _, source := range []string{
		"before\n\n\n![image](/data/a.png)",
		"<speech>hello</speech>\n\n![image](/data/a.png)",
	} {
		for _, withSource := range []bool{false, true} {
			binding := markdownmedia.Binding{Reference: markdownmedia.Parse(source)[0], Asset: media.Asset{BotID: "bot-1", ContentHash: strings.Repeat("a", 64)}}
			values := map[string]any{markdownmedia.MetadataKey: []markdownmedia.Binding{binding}}
			if withSource {
				values["markdown_media_source"] = source
			}
			metadata, _ := json.Marshal(values)
			content, _ := json.Marshal(map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": source}}})
			turns := convertTestMessagesToUITurns([]messagepkg.Message{{ID: "assistant-1", Role: "assistant", Content: content, RawMetadata: metadata}})
			data, _ := json.Marshal(turns)
			if strings.Contains(string(data), "/data/a.png") || strings.Contains(string(data), "<speech>") || !strings.Contains(string(data), "/bots/bot-1/media/") {
				t.Fatalf("source=%q withSource=%v history=%s", source, withSource, data)
			}
		}
	}
}

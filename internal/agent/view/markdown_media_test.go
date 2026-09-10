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

package view

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
)

func TestTerminalNoticesKeepTheirLiveIdentity(t *testing.T) {
	c := NewUIMessageStreamConverter()
	n := event.Notice{Code: "native_history_lost", Content: "Previous context was lost."}
	live := c.HandleEvent(UIMessageStreamEvent{Type: "runtime_notice", Code: n.Code, Delta: n.Content})
	text := c.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "ready"})
	terminal := c.ConvertTerminalMessages(json.RawMessage(`[{"role":"assistant","content":[{"type":"text","text":"ready"}]}]`), n)
	if len(terminal) != 2 || terminal[0].Type != UIMessageNotice || terminal[0].ID != live[0].ID || terminal[1].ID != text[0].ID {
		t.Fatalf("terminal blocks lost or replaced live notice/text: %#v", terminal)
	}
	// Repeated terminal delivery must reuse the same IDs, not append warnings.
	again := c.ConvertTerminalMessages(json.RawMessage(`[{"role":"assistant","content":[{"type":"text","text":"ready"}]}]`), n)
	if len(again) != 2 || again[0].ID != terminal[0].ID || again[1].ID != terminal[1].ID {
		t.Fatalf("terminal replay duplicated blocks: %#v", again)
	}
}

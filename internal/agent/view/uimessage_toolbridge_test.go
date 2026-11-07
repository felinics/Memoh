package view

import (
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

// A tool_call runs an on-demand tool; history and the live stream both show
// the tool it ran, so its renderer (schedule card, browser detail) applies.
func TestConvertModelMessagesShowsTheToolABridgedCallRan(t *testing.T) {
	arguments := map[string]any{"name": "daily", "pattern": "0 9 * * *"}
	messages := ConvertModelMessagesToUIAssistantMessages([]turn.ModelMessage{{
		Role: "assistant",
		Content: mustUIRawJSON(t, []map[string]any{{
			"type":       "tool-call",
			"toolCallId": "call-1",
			"toolName":   "tool_call",
			"input":      map[string]any{"name": "create_schedule", "arguments": arguments},
		}}),
	}})
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	if messages[0].Name != "create_schedule" || !reflect.DeepEqual(messages[0].Input, arguments) {
		t.Fatalf("tool block = %q %#v, want create_schedule with its own arguments", messages[0].Name, messages[0].Input)
	}
}

func TestUIMessageStreamConverterShowsTheToolABridgedCallRan(t *testing.T) {
	converter := NewUIMessageStreamConverter()
	arguments := map[string]any{"action": "snapshot"}
	got := converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_start",
		ToolName:   "tool_call",
		ToolCallID: "call-1",
		Input:      map[string]any{"name": "browser_observe", "arguments": arguments},
	})
	if len(got) != 1 || got[0].Name != "browser_observe" || !reflect.DeepEqual(got[0].Input, arguments) {
		t.Fatalf("stream block = %#v, want browser_observe with its own arguments", got)
	}
	got = converter.HandleEvent(UIMessageStreamEvent{
		Type:       "tool_call_end",
		ToolName:   "tool_call",
		ToolCallID: "call-1",
		Input:      map[string]any{"name": "browser_observe", "arguments": arguments},
		Output:     map[string]any{"ok": true},
	})
	if len(got) != 1 || got[0].Name != "browser_observe" {
		t.Fatalf("finished block = %#v, want browser_observe", got)
	}
}

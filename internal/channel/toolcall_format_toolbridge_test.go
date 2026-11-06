package channel

import "testing"

// A progress message for a tool_call names the on-demand tool it ran and
// summarizes that tool's own arguments.
func TestBuildToolCallPresentationsShowTheToolABridgedCallRan(t *testing.T) {
	t.Parallel()

	bridged := &StreamToolCall{
		Name:   "tool_call",
		CallID: "call-1",
		Input:  map[string]any{"name": "web_fetch", "arguments": map[string]any{"url": "https://example.com"}},
		Result: map[string]any{"ok": true},
	}
	direct := &StreamToolCall{
		Name:   "web_fetch",
		CallID: "call-1",
		Input:  map[string]any{"url": "https://example.com"},
		Result: map[string]any{"ok": true},
	}
	if got, want := BuildToolCallStart(bridged), BuildToolCallStart(direct); got.ToolName != want.ToolName || got.Header != want.Header {
		t.Fatalf("start = %+v, want the presentation of the direct call %+v", got, want)
	}
	if got, want := BuildToolCallEnd(bridged), BuildToolCallEnd(direct); got.ToolName != want.ToolName || got.Header != want.Header || got.Footer != want.Footer {
		t.Fatalf("end = %+v, want the presentation of the direct call %+v", got, want)
	}
	if bridged.Name != "tool_call" {
		t.Fatal("presenting a bridged call must not modify the event payload")
	}
}

package view

import "testing"

// A new owner's converter continues the blocks the previous process rendered:
// a later block takes the next ID instead of overwriting the first one, and the
// answered decision updates its existing card.
func TestConverterResumeContinuesRestoredBlocks(t *testing.T) {
	t.Parallel()
	converter := NewUIMessageStreamConverter()
	converter.Resume([]UIMessage{
		{ID: 0, Type: UIMessageText, Content: "Let me ask."},
		{ID: 1, Type: UIMessageTool, Name: "ask_user", ToolCallID: "call-ask", UserInput: &UIUserInput{UserInputID: "input-1", Status: "pending", CanRespond: true}},
	})

	answered := converter.HandleEvent(UIMessageStreamEvent{Type: "tool_call_end", ToolName: "ask_user", ToolCallID: "call-ask", Output: map[string]any{"answer": "runtime"}})
	if len(answered) != 1 || answered[0].ID != 1 || answered[0].UserInput == nil {
		t.Fatalf("answered card = %#v, want the restored card updated in place", answered)
	}

	converter.HandleEvent(UIMessageStreamEvent{Type: "text_start"})
	reply := converter.HandleEvent(UIMessageStreamEvent{Type: "text_delta", Delta: "收到：runtime"})
	if len(reply) != 1 || reply[0].ID != 2 {
		t.Fatalf("reply = %#v, want a new block after the restored ones", reply)
	}
}

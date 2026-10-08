package messageconv

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
)

// A command with "&&" and ">" must read back from a row with the same bytes
// the live turn sent: HTML escaping in the store would change the arguments
// string an OpenAI-style provider replays and break the prompt cache prefix.
func TestStoredRowsKeepLiteralHTMLCharacters(t *testing.T) {
	t.Parallel()
	input := sdk.ParseToolArguments(`{"command":"cd /data && make > out.log <&"}`)
	live := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: "c1", ToolName: "exec", Input: input}}}
	stored := SDKMessagesToModelMessages([]sdk.Message{live})
	if len(stored) != 1 {
		t.Fatalf("stored %d messages", len(stored))
	}
	if got := string(stored[0].Content); !contains(got, `&& make > out.log <&`) {
		t.Fatalf("stored row escaped the command: %s", got)
	}
	back := ModelMessageToSDKMessage(turn.ModelMessage{Role: "assistant", Content: stored[0].Content})
	call, ok := back.Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("read back %#v", back.Content)
	}
	if string(call.Input.JSON) != string(input.JSON) {
		t.Fatalf("reloaded input %s, want the live bytes %s", call.Input.JSON, input.JSON)
	}
	out, err := sdk.RawJSONOutput([]byte(`{"stdout":"a > b & c"}`))
	if err != nil {
		t.Fatal(err)
	}
	toolMsg := sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "c1", ToolName: "exec", Result: out})
	storedOut := SDKMessagesToModelMessages([]sdk.Message{toolMsg})
	if got := string(storedOut[0].Content); !contains(got, `"a > b & c"`) {
		t.Fatalf("stored output escaped: %s", got)
	}
}

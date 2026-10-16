package messageconv

import (
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
)

// Rows written before the typed SDK contract must read back and, when the
// history trimming path writes them again, keep their bytes. Every shape the
// codec rewrites is covered: arguments object, invalid-argument text, text and
// document outputs, a null output, provider namespaces and Memoh's own
// annotations.
// A legacy row reads back into SDK types and writes back in the stored shape.
// Its argument and output documents come back in RFC 8785 canonical form
// (members sorted), so the first round trip may reorder members but never
// changes a value; a second round trip is byte-stable.
func TestStoredPartsRoundTripKeepsLegacyShape(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`[{"input":{"path":"/tmp/a","n":3},"providerMetadata":{"anthropic":{"signature":"sig-1"},"approval":{"approval_id":"a1","can_approve":true,"operation":{"kind":"exec"},"short_id":2,"status":"pending"},"execution_location":{"kind":"native","name":"Server Workspace"}},"toolCallId":"c1","toolName":"exec","type":"tool-call"}]`,
		`[{"input":"not json {","toolCallId":"c2","toolName":"exec","type":"tool-call"}]`,
		`[{"result":"plain text","toolCallId":"c1","toolName":"exec","type":"tool-result"}]`,
		`[{"isError":true,"result":{"status":"expired","answers":[{"question_id":"q1"}]},"toolCallId":"c1","toolName":"ask_user","type":"tool-result"}]`,
		`[{"result":null,"toolCallId":"c3","toolName":"exec","type":"tool-result"}]`,
		`[{"providerMetadata":{"google":{"thoughtSignature":"SIG"}},"text":"answer","type":"text"}]`,
		`[{"format":"anthropic","providerMetadata":{"anthropic":{"redactedData":"BLOB"}},"text":"","type":"reasoning"}]`,
	} {
		first := roundTripStored(t, content)
		if !jsonEqual(t, first, content) {
			t.Fatalf("round trip changed a value\n got  %s\n want %s", first, content)
		}
		if second := roundTripStored(t, first); second != first {
			t.Fatalf("second round trip is not byte-stable\n got  %s\n want %s", second, first)
		}
	}
}

// A row whose documents are already canonical (members sorted) round-trips
// byte for byte on the first pass.
func TestStoredPartsRoundTripKeepsCanonicalBytes(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		`[{"input":{"n":3,"path":"/tmp/a"},"providerMetadata":{"approval":{"approval_id":"a1","status":"pending"}},"toolCallId":"c1","toolName":"exec","type":"tool-call"}]`,
		`[{"isError":true,"result":{"answers":[{"question_id":"q1"}],"status":"expired"},"toolCallId":"c1","toolName":"ask_user","type":"tool-result"}]`,
	} {
		if got := roundTripStored(t, content); got != content {
			t.Fatalf("round trip changed the row\n got  %s\n want %s", got, content)
		}
	}
}

func roundTripStored(t *testing.T, content string) string {
	t.Helper()
	stored := turn.ModelMessage{Role: roleFor(content), Content: json.RawMessage(content)}
	msg := ModelMessageToSDKMessage(stored)
	if len(msg.Content) != 1 {
		t.Fatalf("%s: decoded %d parts", content, len(msg.Content))
	}
	back := SDKMessagesToModelMessages([]sdk.Message{msg})
	if len(back) != 1 {
		t.Fatalf("%s: encoded %d messages", content, len(back))
	}
	return string(back[0].Content)
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal([]byte(a), &va); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &vb); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

// A provider namespace whose values are not strings is the one legacy shape
// the typed metadata cannot hold verbatim: its nested values come back
// encoded the way sdk.StringValues encodes them. The previous SDK wrote such
// rows for MiniMax (reasoning_details is an array), so this pins how those
// rows read back and what a rollback would find in rows written since.
func TestStoredProviderMetadataNestedValuesAreStringified(t *testing.T) {
	t.Parallel()
	content := `[{"providerMetadata":{"openai":{"item":"rs_1","nested":{"a":1}}},"text":"answer","type":"text"}]`
	msg := ModelMessageToSDKMessage(turn.ModelMessage{Role: "assistant", Content: json.RawMessage(content)})
	part, ok := msg.Content[0].(sdk.TextPart)
	if !ok || part.ProviderMetadata.Get("openai", "item") != "rs_1" || part.ProviderMetadata.Get("openai", "nested") != `{"a":1}` {
		t.Fatalf("decoded metadata = %#v", msg.Content[0])
	}
	back := SDKMessagesToModelMessages([]sdk.Message{msg})
	want := `[{"providerMetadata":{"openai":{"item":"rs_1","nested":"{\"a\":1}"}},"text":"answer","type":"text"}]`
	if got := string(back[0].Content); got != want {
		t.Fatalf("stored metadata = %s, want %s", got, want)
	}
}

func roleFor(content string) string {
	switch {
	case json.Valid([]byte(content)) && len(content) > 0 && contains(content, `"type":"tool-result"`):
		return "tool"
	default:
		return "assistant"
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool { _, ok := indexOf(s, sub); return ok })()
}

func indexOf(s, sub string) (int, bool) {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i, true
		}
	}
	return -1, false
}

// A stored string is invalid argument text. A string that parses as a JSON
// document is typed as that document and written back as one; the single
// ambiguous shape, a document that is itself a JSON string literal, reads
// back as invalid text on the second pass. Neither shape is produced by any
// provider; this pins the rule so a change to it is deliberate.
func TestStoredStringArgumentsFollowTheInvalidTextRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stored string
		first  string
		second string
	}{
		{`"hello world"`, `"hello world"`, `"hello world"`},
		{`"{\"a\":1}"`, `{"a":1}`, `{"a":1}`},
		{`"\"x\""`, `"x"`, `"x"`},
	} {
		row := `[{"input":` + tc.stored + `,"toolCallId":"c1","toolName":"exec","type":"tool-call"}]`
		first := roundTripStored(t, row)
		wantFirst := `[{"input":` + tc.first + `,"toolCallId":"c1","toolName":"exec","type":"tool-call"}]`
		if first != wantFirst {
			t.Fatalf("stored %s: first pass = %s, want %s", tc.stored, first, wantFirst)
		}
		second := roundTripStored(t, first)
		wantSecond := `[{"input":` + tc.second + `,"toolCallId":"c1","toolName":"exec","type":"tool-call"}]`
		if second != wantSecond {
			t.Fatalf("stored %s: second pass = %s, want %s", tc.stored, second, wantSecond)
		}
	}
	// The ambiguous shape: "x" as a document reads back as the text x.
	msg := ModelMessageToSDKMessage(turn.ModelMessage{Role: "assistant", Content: json.RawMessage(`[{"input":"x","toolCallId":"c1","toolName":"exec","type":"tool-call"}]`)})
	if call := msg.Content[0].(sdk.ToolCallPart); call.Input.Valid() || call.Input.Text != "x" {
		t.Fatalf("string literal document read back as %#v, want the invalid text x", call.Input)
	}
}

// Outputs follow the same rule as arguments: a stored string is text, any
// other value is a document. A JSON output that is itself a string literal is
// the one shape that reads back as text; the native path never stores one,
// and this pins the rule so a change to it is deliberate.
func TestStoredOutputsFollowTheTextRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ stored, want string }{
		{`"plain text"`, `"plain text"`},
		{`{"ok":true}`, `{"ok":true}`},
		{`[1,2]`, `[1,2]`},
		{`42`, `42`},
		{`null`, `null`},
	} {
		row := `[{"result":` + tc.stored + `,"toolCallId":"c1","toolName":"exec","type":"tool-result"}]`
		want := `[{"result":` + tc.want + `,"toolCallId":"c1","toolName":"exec","type":"tool-result"}]`
		if got := roundTripStored(t, row); got != want {
			t.Fatalf("stored %s: round trip = %s, want %s", tc.stored, got, want)
		}
	}
	literal, err := sdk.JSONOutput("x")
	if err != nil {
		t.Fatal(err)
	}
	live := sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "c1", ToolName: "exec", Result: literal})
	stored := SDKMessagesToModelMessages([]sdk.Message{live})[0]
	reloaded := ModelMessageToSDKMessage(stored).Content[0].(sdk.ToolResultPart)
	if reloaded.Result.IsJSON() || reloaded.Result.Text != "x" {
		t.Fatalf("string-literal document read back as %#v, want the text x", reloaded.Result)
	}
}

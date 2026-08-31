package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocolgen"
)

func TestDecodeInboundClassification(t *testing.T) {
	tests := []struct {
		name string
		line string
		kind InboundKind
	}{
		{
			name: "server request",
			line: `{"id":42,"method":"item/commandExecution/requestApproval","params":{"itemId":"item_1","threadId":"th_1","turnId":"turn_1","startedAtMs":1724800000000,"command":"rm -rf build"}}`,
			kind: InboundRequest,
		},
		{
			name: "notification",
			line: `{"method":"item/agentMessage/delta","params":{"delta":"hello","itemId":"item_2","threadId":"th_1","turnId":"turn_1"}}`,
			kind: InboundNotification,
		},
		{
			name: "response",
			line: `{"id":7,"result":{"thread":{"id":"th_1"}}}`,
			kind: InboundResponse,
		},
		{
			name: "error response",
			line: `{"id":"req-9","error":{"code":-32001,"message":"overloaded"}}`,
			kind: InboundResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := DecodeInbound([]byte(tt.line))
			if err != nil {
				t.Fatal(err)
			}
			if in.Kind != tt.kind {
				t.Fatalf("kind = %v, want %v", in.Kind, tt.kind)
			}
		})
	}

	if _, err := DecodeInbound([]byte(`{"jsonrpc":"2.0"}`)); err == nil {
		t.Fatal("want error for line with neither method nor id")
	}
}

func TestRequestIDPreservesWireBytes(t *testing.T) {
	// A numeric id must echo back as a number, not a string.
	in, err := DecodeInbound([]byte(`{"id":42,"method":"item/commandExecution/requestApproval","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := json.Marshal(Response{ID: in.ID, Result: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(resp, []byte(`"id":42`)) {
		t.Fatalf("numeric id not preserved: %s", resp)
	}

	// A string id stays a string.
	in, err = DecodeInbound([]byte(`{"id":"probe-1","method":"x","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.ID.Key() != `"probe-1"` {
		t.Fatalf("string id key = %s", in.ID.Key())
	}

	// A zero id refuses to marshal rather than emitting invalid JSON.
	if _, err := json.Marshal(Response{Result: map[string]any{}}); err == nil {
		t.Fatal("want error marshaling zero RequestID")
	}
}

func TestServerRequestApprovalRoundTrip(t *testing.T) {
	line := `{"id":3,"method":"item/commandExecution/requestApproval","params":{"itemId":"item_1","threadId":"th_1","turnId":"turn_1","startedAtMs":1724800000000,"command":"cargo test","cwd":"/workspace","reason":"needs network","proposedExecpolicyAmendment":["cargo","test"]}}`
	in, err := DecodeInbound([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	decoded, ok, err := DecodeServerRequestParams(in.Method, in.Params)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	params, isTyped := decoded.(*CommandExecutionRequestApprovalParams)
	if !isTyped {
		t.Fatalf("decoded type %T", decoded)
	}
	if params.ItemID != "item_1" || params.Command == nil || *params.Command != "cargo test" {
		t.Fatalf("unexpected params: %+v", params)
	}
	if params.StartedAtMs != 1724800000000 {
		t.Fatalf("startedAtMs = %d", params.StartedAtMs)
	}

	// Unit decision answer.
	resp, err := json.Marshal(Response{ID: in.ID, Result: CommandExecutionRequestApprovalResponse{
		Decision: CommandExecutionApprovalDecision{Unit: CommandExecutionApprovalDecisionUnitAccept},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":3,"result":{"decision":"accept"}}`
	if string(resp) != want {
		t.Fatalf("response = %s, want %s", resp, want)
	}
}

func TestMixedUnionForms(t *testing.T) {
	// Bare string form.
	var d CommandExecutionApprovalDecision
	if err := json.Unmarshal([]byte(`"acceptForSession"`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Unit != "acceptForSession" {
		t.Fatalf("unit = %q", d.Unit)
	}

	// Object form; note the snake_case payload field on this one type.
	var d2 CommandExecutionApprovalDecision
	payload := `{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["cargo","test"]}}`
	if err := json.Unmarshal([]byte(payload), &d2); err != nil {
		t.Fatal(err)
	}
	if d2.AcceptWithExecpolicyAmendment == nil || len(d2.AcceptWithExecpolicyAmendment.ExecpolicyAmendment) != 2 {
		t.Fatalf("payload variant not decoded: %+v", d2)
	}
	out, err := json.Marshal(d2)
	if err != nil {
		t.Fatal(err)
	}
	var back CommandExecutionApprovalDecision
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.AcceptWithExecpolicyAmendment == nil || len(back.AcceptWithExecpolicyAmendment.ExecpolicyAmendment) != 2 {
		t.Fatalf("round trip lost payload: %s", out)
	}

	// Unknown object variant is tolerated and re-marshals as its raw bytes.
	var d3 CommandExecutionApprovalDecision
	unknown := `{"futureDecisionKind":{"x":1}}`
	if err := json.Unmarshal([]byte(unknown), &d3); err != nil {
		t.Fatal(err)
	}
	if d3.Unit != "" || d3.AcceptWithExecpolicyAmendment != nil {
		t.Fatalf("unknown variant should not populate fields: %+v", d3)
	}
	out, err = json.Marshal(d3)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != unknown {
		t.Fatalf("raw echo = %s, want %s", out, unknown)
	}

	// An empty union refuses to marshal.
	if _, err := json.Marshal(CommandExecutionApprovalDecision{}); err == nil {
		t.Fatal("want error for empty union")
	}
}

func TestTaggedUnionForms(t *testing.T) {
	// Known variant.
	var item ThreadItem
	userMsg := `{"type":"userMessage","id":"item_1","content":[{"type":"text","text":"hi"}]}`
	if err := json.Unmarshal([]byte(userMsg), &item); err != nil {
		t.Fatal(err)
	}
	if item.Tag != ThreadItemTagUserMessage || item.UserMessage == nil {
		t.Fatalf("decode: %+v", item)
	}
	if len(item.UserMessage.Content) != 1 || item.UserMessage.Content[0].Text == nil || item.UserMessage.Content[0].Text.Text != "hi" {
		t.Fatalf("nested union: %+v", item.UserMessage.Content)
	}

	// Marshal splices the tag back in.
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"type":"userMessage"`) {
		t.Fatalf("tag missing: %s", out)
	}

	// Unknown variant keeps tag and raw, and echoes verbatim.
	var future ThreadItem
	futureJSON := `{"type":"quantumMessage","id":"item_9","payload":{"a":1}}`
	if err := json.Unmarshal([]byte(futureJSON), &future); err != nil {
		t.Fatal(err)
	}
	if future.Tag != "quantumMessage" {
		t.Fatalf("tag = %q", future.Tag)
	}
	out, err = json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != futureJSON {
		t.Fatalf("raw echo = %s", out)
	}
}

func TestDecodeServerNotificationParams(t *testing.T) {
	decoded, ok, err := DecodeServerNotificationParams("item/agentMessage/delta", []byte(`{"delta":"hi","itemId":"i","threadId":"t","turnId":"u"}`))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	delta, isTyped := decoded.(*AgentMessageDeltaNotification)
	if !isTyped || delta.Delta != "hi" {
		t.Fatalf("decoded: %#v", decoded)
	}

	// Methods outside the typed subset are reported, not failed.
	_, ok, err = DecodeServerNotificationParams("thread/realtime/sdp", []byte(`{}`))
	if err != nil || ok {
		t.Fatalf("unknown method: ok=%v err=%v", ok, err)
	}
}

func TestUnionDecodeIntoReusedValue(t *testing.T) {
	// Reusing a variable across decodes must not leak the previous variant.
	var item ThreadItem
	if err := json.Unmarshal([]byte(`{"type":"agentMessage","id":"a","text":"one"}`), &item); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"r"}`), &item); err != nil {
		t.Fatal(err)
	}
	if item.AgentMessage != nil || item.Reasoning == nil || item.Tag != ThreadItemTagReasoning {
		t.Fatalf("stale variant survived re-decode: %+v", item)
	}
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"type":"reasoning"`) {
		t.Fatalf("re-marshal produced the old variant: %s", out)
	}

	// encoding/json reuses slice elements by capacity without zeroing them.
	items := []ThreadItem{}
	if err := json.Unmarshal([]byte(`[{"type":"agentMessage","id":"a","text":"one"},{"type":"plan","id":"p","text":"x"}]`), &items); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`[{"type":"reasoning","id":"r"}]`), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AgentMessage != nil || items[0].Reasoning == nil {
		t.Fatalf("slice element reuse leaked stale variant: %+v", items)
	}

	// Mixed union: a stale Unit must not shadow a later object variant.
	var d CommandExecutionApprovalDecision
	if err := json.Unmarshal([]byte(`"accept"`), &d); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["x"]}}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Unit != "" || d.AcceptWithExecpolicyAmendment == nil {
		t.Fatalf("stale unit survived re-decode: %+v", d)
	}
	out, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "accept\"") && !strings.Contains(string(out), "acceptWithExecpolicyAmendment") {
		t.Fatalf("re-marshal produced the stale unit: %s", out)
	}
}

func TestUnionRawIsStable(t *testing.T) {
	// Bytes handed out by Raw() must survive later decodes into the variable.
	var item ThreadItem
	first := `{"type":"futureMessage","id":"f","payload":{"a":1}}`
	if err := json.Unmarshal([]byte(first), &item); err != nil {
		t.Fatal(err)
	}
	saved := item.Raw()
	if err := json.Unmarshal([]byte(`{"type":"plan","id":"p","text":"x"}`), &item); err != nil {
		t.Fatal(err)
	}
	if string(saved) != first {
		t.Fatalf("saved Raw() bytes were rewritten by a later decode: %s", saved)
	}
}

func TestOutboundEnvelopeMarshal(t *testing.T) {
	// nil params are omitted entirely.
	out, err := json.Marshal(Request{ID: NewRequestID(1), Method: "account/rateLimits/read"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "params") {
		t.Fatalf("nil params not omitted: %s", out)
	}

	// A typed-nil pointer is also omitted, not sent as `"params": null`.
	var p *TurnStartParams
	out, err = json.Marshal(Request{ID: NewRequestID(2), Method: MethodTurnStart, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "params") {
		t.Fatalf("typed-nil params not omitted: %s", out)
	}

	// Required collection fields normalize nil to empty on the way out.
	out, err = json.Marshal(Request{ID: NewRequestID(3), Method: MethodTurnStart, Params: TurnStartParams{ThreadID: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"input":[]`) {
		t.Fatalf("required collection marshaled as null: %s", out)
	}

	out, err = json.Marshal(Notification{Method: MethodInitialized})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"method":"initialized"}` {
		t.Fatalf("notification = %s", out)
	}
}

func TestRequestIDValueCopyStable(t *testing.T) {
	var id RequestID
	if err := json.Unmarshal([]byte(`101`), &id); err != nil {
		t.Fatal(err)
	}
	saved := id
	if err := json.Unmarshal([]byte(`202`), &id); err != nil {
		t.Fatal(err)
	}
	if saved.Key() != "101" {
		t.Fatalf("value copy drifted to %s", saved.Key())
	}
}

func TestDecodeInboundNullID(t *testing.T) {
	// A parse-error style response with id:null is an orphan response, not a
	// correlatable one and not an error.
	in, err := DecodeInbound([]byte(`{"id":null,"error":{"code":-32700,"message":"parse error"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != InboundResponse || !in.ID.IsZero() || in.Err == nil {
		t.Fatalf("null-id error line: kind=%v id=%q err=%v", in.Kind, in.ID.Key(), in.Err)
	}

	// id:null plus a method is a notification — never a request to answer.
	in, err = DecodeInbound([]byte(`{"id":null,"method":"m","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != InboundNotification {
		t.Fatalf("null-id method line: kind=%v", in.Kind)
	}
}

func TestNewResponseForMethod(t *testing.T) {
	resp, ok := NewResponseForMethod(MethodThreadStart)
	if !ok {
		t.Fatal("thread/start should have a generated response")
	}
	if _, isTyped := resp.(*ThreadStartResponse); !isTyped {
		t.Fatalf("response type %T", resp)
	}
	if _, ok := NewResponseForMethod("initialize"); ok {
		t.Fatal("initialize response is hand-written, not generated")
	}
}

// Keep the checked-in protocol sources pinned to the vendored Codex schema.
func TestGeneratedFilesAreFresh(t *testing.T) {
	files, err := protocolgen.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(name) //nolint:gosec // fixed generated paths owned by this package
		if err != nil {
			t.Fatalf("%s: %v (run `mise run codex-protocol-generate`)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run `mise run codex-protocol-generate`", name)
		}
	}
}

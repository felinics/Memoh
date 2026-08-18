package application

import (
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
)

func TestRestoreVisibleTextSnapshotRecoversEmptyAbortedMessages(t *testing.T) {
	snap := terminalSnapshot{aborted: true, visibleOutput: true}
	restoreVisibleTextSnapshot(&snap, "partial answer")

	if len(snap.sdkMessages) != 1 {
		t.Fatalf("messages = %d, want 1", len(snap.sdkMessages))
	}
	if snap.sdkMessages[0].Role != sdk.MessageRoleAssistant {
		t.Fatalf("role = %q, want assistant", snap.sdkMessages[0].Role)
	}
	part, ok := snap.sdkMessages[0].Content[0].(sdk.TextPart)
	if !ok || part.Text != "partial answer" {
		t.Fatalf("content = %#v, want recovered partial answer", snap.sdkMessages[0].Content)
	}
}

func TestRestoreVisibleTextSnapshotDoesNotReplaceConcreteSnapshot(t *testing.T) {
	snap := terminalSnapshot{
		aborted:       true,
		visibleOutput: true,
		sdkMessages:   []sdk.Message{sdk.AssistantMessage("durable answer")},
	}
	restoreVisibleTextSnapshot(&snap, "streamed answer")

	part := snap.sdkMessages[0].Content[0].(sdk.TextPart)
	if part.Text != "durable answer" {
		t.Fatalf("content = %q, want durable answer", part.Text)
	}
}

func TestRecordVisibleAgentTextKeepsPlainTextFastPath(t *testing.T) {
	var buf strings.Builder
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "hello "})
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "world"})

	if got := buf.String(); got != "hello world" {
		t.Fatalf("visible text = %q, want %q", got, "hello world")
	}
}

func TestRecoveryPreservesMixedTextAndReasoningOrder(t *testing.T) {
	var buf strings.Builder
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "hello "})
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventReasoningDelta, Delta: "thinking"})
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "world"})

	snap := terminalSnapshot{aborted: true, visibleOutput: true}
	restoreVisibleTextSnapshot(&snap, buf.String())

	if len(snap.sdkMessages) != 1 || len(snap.sdkMessages[0].Content) != 3 {
		t.Fatalf("recovered messages = %#v, want one assistant with three ordered parts", snap.sdkMessages)
	}
	first, ok := snap.sdkMessages[0].Content[0].(sdk.TextPart)
	if !ok || first.Text != "hello " {
		t.Fatalf("first part = %#v, want hello text", snap.sdkMessages[0].Content[0])
	}
	reasoning, ok := snap.sdkMessages[0].Content[1].(sdk.ReasoningPart)
	if !ok || reasoning.Text != "thinking" {
		t.Fatalf("second part = %#v, want reasoning", snap.sdkMessages[0].Content[1])
	}
	last, ok := snap.sdkMessages[0].Content[2].(sdk.TextPart)
	if !ok || last.Text != "world" {
		t.Fatalf("third part = %#v, want world text", snap.sdkMessages[0].Content[2])
	}
}

func TestRecoveryPreservesToolOnlyOutput(t *testing.T) {
	var buf strings.Builder
	recordVisibleAgentText(&buf, native.StreamEvent{
		Type:       native.EventToolCallStart,
		ToolCallID: "call-1",
		ToolName:   "read",
		Input:      map[string]any{"path": "/tmp/a"},
	})
	recordVisibleAgentText(&buf, native.StreamEvent{
		Type:       native.EventToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   "read",
		Result:     "ok",
	})

	snap := terminalSnapshot{aborted: true, visibleOutput: true}
	restoreVisibleTextSnapshot(&snap, buf.String())

	if len(snap.sdkMessages) != 2 {
		t.Fatalf("recovered messages = %#v, want assistant tool call + tool result", snap.sdkMessages)
	}
	if snap.sdkMessages[0].Role != sdk.MessageRoleAssistant || snap.sdkMessages[1].Role != sdk.MessageRoleTool {
		t.Fatalf("roles = %q, %q, want assistant/tool", snap.sdkMessages[0].Role, snap.sdkMessages[1].Role)
	}
	call, ok := snap.sdkMessages[0].Content[0].(sdk.ToolCallPart)
	if !ok || call.ToolCallID != "call-1" || call.ToolName != "read" {
		t.Fatalf("tool call = %#v", snap.sdkMessages[0].Content[0])
	}
	result, ok := snap.sdkMessages[1].Content[0].(sdk.ToolResultPart)
	if !ok || result.ToolCallID != "call-1" || result.Result != "ok" {
		t.Fatalf("tool result = %#v", snap.sdkMessages[1].Content[0])
	}
}

func TestShouldPersistTerminalEventKeepsThreeArgumentCompatibility(t *testing.T) {
	event := native.StreamEvent{
		Type:     native.EventAgentEnd,
		Messages: []byte{1},
	}
	if !shouldPersistTerminalEvent(event, nil, true) {
		t.Fatal("three-argument compatibility call rejected concrete terminal output")
	}
}

func TestShouldPersistVisibleExplicitCancellationButNotEmptyCancellation(t *testing.T) {
	abort := native.StreamEvent{Type: native.EventAgentAbort}
	if !shouldPersistTerminalEvent(abort, nil, true) {
		t.Fatal("visible explicit cancellation should persist recovered output")
	}
	if shouldPersistTerminalEvent(abort, nil, false) {
		t.Fatal("empty explicit cancellation should not synthesize history")
	}
}

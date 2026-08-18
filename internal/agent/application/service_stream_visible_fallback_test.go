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

func TestRecordVisibleAgentTextKeepsDeltaOrder(t *testing.T) {
	var buf strings.Builder
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "hello "})
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventReasoningDelta, Delta: "hidden"})
	recordVisibleAgentText(&buf, native.StreamEvent{Type: native.EventTextDelta, Delta: "world"})

	if got := buf.String(); got != "hello world" {
		t.Fatalf("visible text = %q, want %q", got, "hello world")
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

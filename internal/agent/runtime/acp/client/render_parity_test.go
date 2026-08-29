package client

import (
	"encoding/json"
	"reflect"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

// renderScript renders a sequence of runtime events to final UI messages.
func renderScript(t *testing.T, events []event.StreamEvent) []chatview.UIMessage {
	t.Helper()
	converter := chatview.NewUIMessageStreamConverter()
	finals := map[int]chatview.UIMessage{}
	order := []int{}
	for _, ev := range events {
		for _, ui := range converter.HandleEvent(chatview.UIStreamEventFromAgentEvent(ev)) {
			if _, seen := finals[ui.ID]; !seen {
				order = append(order, ui.ID)
			}
			finals[ui.ID] = ui
		}
	}
	out := make([]chatview.UIMessage, 0, len(finals))
	for _, id := range order {
		out = append(out, finals[id])
	}
	return out
}

func assertUIMessagesEqual(t *testing.T, native, acpSide []chatview.UIMessage) {
	t.Helper()
	nativeJSON, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal native UI messages: %v", err)
	}
	acpJSON, err := json.Marshal(acpSide)
	if err != nil {
		t.Fatalf("marshal ACP UI messages: %v", err)
	}
	var nativeNorm, acpNorm any
	if err := json.Unmarshal(nativeJSON, &nativeNorm); err != nil {
		t.Fatalf("unmarshal native: %v", err)
	}
	if err := json.Unmarshal(acpJSON, &acpNorm); err != nil {
		t.Fatalf("unmarshal acp: %v", err)
	}
	if !reflect.DeepEqual(nativeNorm, acpNorm) {
		t.Fatalf("rendering parity broken:\n  native: %s\n  acp:    %s", nativeJSON, acpJSON)
	}
}

// acpProducedEvents builds one ACP-side exec interaction.
func acpProducedEvents(t *testing.T) []event.StreamEvent {
	t.Helper()
	events := make([]event.StreamEvent, 0, 6)

	// Approval card events come from the Memoh side of the boundary.
	callbacks, collector := parityCallbacks(&parityApproval{}, "run-1")
	pending := toolapproval.Request{
		ID:         "approval-1",
		ShortID:    1,
		ToolCallID: "call-1",
		ToolName:   "exec",
		ToolInput:  map[string]any{"command": "make test"},
		Status:     toolapproval.StatusPending,
	}
	callbacks.emitToolApprovalRequest(pending)
	approved := pending
	approved.Status = toolapproval.StatusApproved
	callbacks.emitToolApprovalRequest(approved)
	events = append(events, collector.result().Events...)

	// Tool lifecycle and text come from the agent side of the boundary - the
	// notification mapper.
	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events = append(events, mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.StartToolCall(
			acp.ToolCallId("call-1"),
			"Shell",
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{"command": "make test"}),
		),
	})...)
	events = append(events, mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("call-1"),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateRawOutput(map[string]any{"stdout": "ok\n", "exit_code": 0}),
		),
	})...)
	events = append(events, mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateAgentMessage(acp.TextBlock("Done.")),
	})...)
	return events
}

// nativeShapedEvents is the same interaction as the native runtime emits it.
func nativeShapedEvents() []event.StreamEvent {
	pending := toolapproval.Request{
		ID:         "approval-1",
		ShortID:    1,
		ToolCallID: "call-1",
		ToolName:   "exec",
		ToolInput:  map[string]any{"command": "make test"},
		Status:     toolapproval.StatusPending,
	}
	approved := pending
	approved.Status = toolapproval.StatusApproved
	return []event.StreamEvent{
		{
			Type:       event.ToolApprovalRequest,
			ToolName:   "exec",
			ToolCallID: "call-1",
			Input:      map[string]any{"command": "make test"},
			ApprovalID: "approval-1",
			ShortID:    1,
			Status:     toolapproval.StatusPending,
			Metadata:   map[string]any{"approval": toolapproval.RequestMetadata(pending)},
		},
		{
			Type:       event.ToolApprovalRequest,
			ToolName:   "exec",
			ToolCallID: "call-1",
			Input:      map[string]any{"command": "make test"},
			ApprovalID: "approval-1",
			ShortID:    1,
			Status:     toolapproval.StatusApproved,
			Metadata:   map[string]any{"approval": toolapproval.RequestMetadata(approved)},
		},
		{
			Type:       event.ToolCallStart,
			ToolName:   "exec",
			ToolCallID: "call-1",
			Input:      map[string]any{"command": "make test"},
		},
		{
			Type:       event.ToolCallEnd,
			ToolName:   "exec",
			ToolCallID: "call-1",
			Input:      map[string]any{"command": "make test"},
			Result:     map[string]any{"stdout": "ok\n", "exit_code": 0},
		},
		{Type: event.TextDelta, Delta: "Done."},
	}
}

func TestRenderingParityExecApprovalLifecycle(t *testing.T) {
	t.Parallel()

	native := renderScript(t, nativeShapedEvents())
	acpSide := renderScript(t, acpProducedEvents(t))

	if len(native) == 0 {
		t.Fatalf("native script rendered no UI messages")
	}
	assertUIMessagesEqual(t, native, acpSide)
}

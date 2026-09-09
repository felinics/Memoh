package client

import (
	"strconv"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func TestACPGenericExecuteToolMapsToNativeExecEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	start := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.StartToolCall(
			acp.ToolCallId("call-1"),
			"Shell",
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{"command": "date '+%Y-%m-%d %H:%M:%S %Z'"}),
		),
	})
	if len(start) != 1 {
		t.Fatalf("start events = %#v, want 1", start)
	}
	if start[0].Type != event.ToolCallStart || start[0].ToolName != "exec" || start[0].ToolCallID != "call-1" {
		t.Fatalf("start event = %#v, want native exec start", start[0])
	}
	input, ok := start[0].Input.(map[string]any)
	if !ok || input["command"] != "date '+%Y-%m-%d %H:%M:%S %Z'" {
		t.Fatalf("start input = %#v", start[0].Input)
	}

	end := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("call-1"),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateRawOutput(map[string]any{
				"stdout":    "2026-05-28 14:51:39 UTC\n",
				"exit_code": 0,
			}),
		),
	})
	if len(end) != 1 {
		t.Fatalf("end events = %#v, want 1", end)
	}
	if end[0].Type != event.ToolCallEnd || end[0].ToolName != "exec" || end[0].Error != "" {
		t.Fatalf("end event = %#v, want native exec end", end[0])
	}
	result, ok := end[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("end result = %#v, want object", end[0].Result)
	}
	if result["stdout"] != "2026-05-28 14:51:39 UTC\n" || result["exit_code"] != 0 {
		t.Fatalf("end result = %#v", result)
	}
}

func TestACPGenericExecuteCompletionWithoutStartEmitsStartThenEnd(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("call-1"),
			acp.WithUpdateKind(acp.ToolKindExecute),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateRawInput(map[string]any{"cmd": "pwd"}),
			acp.WithUpdateRawOutput("workspace\n"),
		),
	})
	if len(events) != 2 {
		t.Fatalf("events = %#v, want start + end", events)
	}
	if events[0].Type != event.ToolCallStart || events[0].ToolName != "exec" {
		t.Fatalf("first event = %#v, want exec start", events[0])
	}
	if events[1].Type != event.ToolCallEnd || events[1].ToolName != "exec" {
		t.Fatalf("second event = %#v, want exec end", events[1])
	}
	result, ok := events[1].Result.(map[string]any)
	if !ok || result["stdout"] != "workspace\n" {
		t.Fatalf("end result = %#v", events[1].Result)
	}
}

func TestACPGenericExecuteTerminalTitles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title       string
		rawInput    any
		wantCommand string
	}{
		{title: "Terminal"},
		{title: "terminal: pwd", rawInput: map[string]any{}, wantCommand: "pwd"},
	}
	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			t.Parallel()
			mapper := newACPToolEventMapper(acpprofile.QuirksFor(acpprofile.AgentACPID))
			events := mapper.eventsFromNotification(acp.SessionNotification{
				Update: acp.StartToolCall(
					acp.ToolCallId("call-1"),
					test.title,
					acp.WithStartKind(acp.ToolKindExecute),
					acp.WithStartStatus(acp.ToolCallStatusInProgress),
					acp.WithStartRawInput(test.rawInput),
				),
			})
			if test.wantCommand == "" {
				if len(events) != 0 {
					t.Fatalf("events = %#v, want none", events)
				}
				return
			}
			if len(events) != 1 || events[0].ToolName != "exec" {
				t.Fatalf("events = %#v, want one exec event", events)
			}
			input, ok := events[0].Input.(map[string]any)
			if !ok || input["command"] != test.wantCommand {
				t.Fatalf("input = %#v, want command %q", events[0].Input, test.wantCommand)
			}
		})
	}
}

func TestACPGenericExecuteTerminalTitleUsesRawCommand(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.StartToolCall(
			acp.ToolCallId("call-1"),
			"Terminal",
			acp.WithStartKind(acp.ToolKindExecute),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{"command": "pwd"}),
		),
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want 1", events)
	}
	if events[0].Type != event.ToolCallStart || events[0].ToolName != "exec" {
		t.Fatalf("event = %#v, want native exec start", events[0])
	}
	input, ok := events[0].Input.(map[string]any)
	if !ok || input["command"] != "pwd" {
		t.Fatalf("input = %#v, want command pwd", events[0].Input)
	}
}

func TestACPGenericEditWithContentMapsToNativeWriteEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	start := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.StartToolCall(
			acp.ToolCallId("write-1"),
			"Write /data/test.txt",
			acp.WithStartKind(acp.ToolKindEdit),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(map[string]any{
				"file_path": "/data/test.txt",
				"content":   "hello from claude\n",
			}),
		),
	})
	if len(start) != 1 {
		t.Fatalf("start events = %#v, want 1", start)
	}
	if start[0].Type != event.ToolCallStart || start[0].ToolName != "write" || start[0].ToolCallID != "write-1" {
		t.Fatalf("start event = %#v, want native write start", start[0])
	}
	input, ok := start[0].Input.(map[string]any)
	if !ok {
		t.Fatalf("start input = %#v, want object", start[0].Input)
	}
	if input["path"] != "/data/test.txt" || input["content"] != "hello from claude\n" {
		t.Fatalf("start input = %#v", input)
	}
	if input["content_bytes"] != len("hello from claude\n") || input["content_line_count"] != 2 {
		t.Fatalf("start input content metadata = %#v", input)
	}

	end := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("write-1"),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		),
	})
	if len(end) != 1 {
		t.Fatalf("end events = %#v, want 1", end)
	}
	if end[0].Type != event.ToolCallEnd || end[0].ToolName != "write" || end[0].Error != "" {
		t.Fatalf("end event = %#v, want native write end", end[0])
	}
}

func TestACPGenericEditDiffMapsToNativeEditEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("edit-1"),
			acp.WithUpdateKind(acp.ToolKindEdit),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{
				acp.ToolDiffContent("/data/test.txt", "new text\n", "old text\n"),
			}),
		),
	})
	if len(events) != 2 {
		t.Fatalf("events = %#v, want start + end", events)
	}
	if events[0].Type != event.ToolCallStart || events[0].ToolName != "edit" {
		t.Fatalf("first event = %#v, want edit start", events[0])
	}
	input, ok := events[0].Input.(map[string]any)
	if !ok {
		t.Fatalf("start input = %#v, want object", events[0].Input)
	}
	if input["path"] != "/data/test.txt" || input["old_text"] != "old text\n" || input["new_text"] != "new text\n" {
		t.Fatalf("start input = %#v", input)
	}
	if events[1].Type != event.ToolCallEnd || events[1].ToolName != "edit" {
		t.Fatalf("second event = %#v, want edit end", events[1])
	}
}

func TestACPGenericEditDiffWithoutOldTextMapsToNativeWriteEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateToolCall(
			acp.ToolCallId("write-1"),
			acp.WithUpdateKind(acp.ToolKindEdit),
			acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
			acp.WithUpdateContent([]acp.ToolCallContent{
				acp.ToolDiffContent("/data/new.txt", "new file\n"),
			}),
		),
	})
	if len(events) != 2 {
		t.Fatalf("events = %#v, want start + end", events)
	}
	if events[0].Type != event.ToolCallStart || events[0].ToolName != "write" {
		t.Fatalf("first event = %#v, want write start", events[0])
	}
	input, ok := events[0].Input.(map[string]any)
	if !ok {
		t.Fatalf("start input = %#v, want object", events[0].Input)
	}
	if input["path"] != "/data/new.txt" || input["content"] != "new file\n" {
		t.Fatalf("start input = %#v", input)
	}
	if events[1].Type != event.ToolCallEnd || events[1].ToolName != "write" {
		t.Fatalf("second event = %#v, want write end", events[1])
	}
}

func TestACPAgentThoughtMapsToReasoningDelta(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdateAgentThoughtText("I should inspect the workspace first."),
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want 1", events)
	}
	if events[0].Type != event.ReasoningDelta || events[0].Delta != "I should inspect the workspace first." {
		t.Fatalf("event = %#v, want reasoning delta", events[0])
	}
}

func TestACPPlanMapsToReasoningDelta(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	events := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdatePlan(
			acp.PlanEntry{Content: "Inspect the workspace", Status: acp.PlanEntryStatusInProgress},
			acp.PlanEntry{Content: "Apply the change", Status: acp.PlanEntryStatusPending},
		),
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want 1", events)
	}
	if events[0].Type != event.ReasoningDelta {
		t.Fatalf("event = %#v, want reasoning delta", events[0])
	}
	want := "Plan:\n- [in_progress] Inspect the workspace\n- [pending] Apply the change"
	if events[0].Delta != want {
		t.Fatalf("plan delta = %q, want %q", events[0].Delta, want)
	}

	repeated := mapper.eventsFromNotification(acp.SessionNotification{
		Update: acp.UpdatePlan(
			acp.PlanEntry{Content: "Inspect the workspace", Status: acp.PlanEntryStatusInProgress},
			acp.PlanEntry{Content: "Apply the change", Status: acp.PlanEntryStatusPending},
		),
	})
	if len(repeated) != 0 {
		t.Fatalf("repeated plan events = %#v, want none", repeated)
	}
}

func TestEventCollectorBoundsStoredEvents(t *testing.T) {
	t.Parallel()

	collector := newEventCollector()
	for i := 0; i < maxCollectedStreamEvents+10; i++ {
		collector.record(event.StreamEvent{
			Type:       event.ToolCallStart,
			ToolCallID: string(rune('a' + (i % 26))),
			ToolName:   "exec",
		})
	}

	result := collector.result()
	if len(result.Events) != maxCollectedStreamEvents {
		t.Fatalf("stored events = %d, want %d", len(result.Events), maxCollectedStreamEvents)
	}
}

func TestACPToolEventMapperBoundsTrackedTools(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	for i := 0; i < maxTrackedACPToolStates+10; i++ {
		_ = mapper.eventsFromNotification(acp.SessionNotification{
			Update: acp.StartToolCall(
				acp.ToolCallId("call-"+strconv.Itoa(i)),
				"Shell",
				acp.WithStartKind(acp.ToolKindExecute),
				acp.WithStartStatus(acp.ToolCallStatusInProgress),
				acp.WithStartRawInput(map[string]any{"command": "pwd"}),
			),
		})
	}
	if len(mapper.tools) > maxTrackedACPToolStates {
		t.Fatalf("tracked tools = %d, want <= %d", len(mapper.tools), maxTrackedACPToolStates)
	}
}

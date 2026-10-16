package client

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
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

// rawOutput is the agent's own shape: here the output bytes come as a number
// array and only content carries the text.
func TestACPToolResultTakesTheNativeToolShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    acp.ToolKind
		input   map[string]any
		status  acp.ToolCallStatus
		content string
		output  any
		refusal string
		want    map[string]any
	}{
		{
			name: "exec", kind: acp.ToolKindExecute, input: map[string]any{"command": "pwd"},
			status: acp.ToolCallStatusCompleted, content: "/data\n",
			output: map[string]any{"type": "Bash", "output": []any{47, 100, 97, 116, 97, 10}, "exit_code": 0},
			want:   map[string]any{"stdout": "/data\n", "exit_code": 0},
		},
		{
			name: "read", kind: acp.ToolKindRead, input: map[string]any{"path": "/data/a.txt"},
			status: acp.ToolCallStatusCompleted, content: "1→alpha\n",
			output: map[string]any{"type": "ReadFile", "FileContent": map[string]any{"raw_output": "alpha\n"}},
			want:   map[string]any{"content": "1→alpha\n"},
		},
		{
			name: "edit", kind: acp.ToolKindEdit, input: map[string]any{"path": "/data/a.txt"},
			status: acp.ToolCallStatusCompleted,
			output: map[string]any{"type": "SearchReplace"},
			want:   map[string]any{"ok": true},
		},
		{
			name: "rejected exec", kind: acp.ToolKindExecute, input: map[string]any{"command": "rm a.txt"},
			status: acp.ToolCallStatusFailed, content: "User rejected the execution for tool `run_terminal_command`",
			want: map[string]any{"isError": true, "content": []map[string]any{{
				"type": "text", "text": "User rejected the execution for tool `run_terminal_command`",
			}}},
		},
		{
			name: "failure told only in rawOutput", kind: acp.ToolKindRead, input: map[string]any{"path": "/data/a.txt"},
			status: acp.ToolCallStatusFailed, output: "file does not exist",
			want: map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "file does not exist"}}},
		},
		{
			name: "failure told only in a rawOutput object", kind: acp.ToolKindEdit, input: map[string]any{"path": "/data/a.txt"},
			status: acp.ToolCallStatusFailed, output: map[string]any{"error": "tool_execution_failed", "message": "permission denied"},
			want: map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "permission denied"}}},
		},
		{
			name: "refused write", kind: acp.ToolKindEdit, input: map[string]any{"path": "/tmp/a.txt"},
			status: acp.ToolCallStatusFailed, content: "User rejected the execution for tool `write`",
			refusal: `path "/tmp/a.txt" escapes workspace root "/data"`,
			want: map[string]any{"isError": true, "content": []map[string]any{{
				"type": "text", "text": `Memoh refused this call: path "/tmp/a.txt" escapes workspace root "/data"`,
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
			mapper.eventsFromNotification(acp.SessionNotification{Update: acp.UpdateToolCall(
				acp.ToolCallId("call-1"),
				acp.WithUpdateKind(tt.kind),
				acp.WithUpdateStatus(acp.ToolCallStatusInProgress),
				acp.WithUpdateRawInput(tt.input),
			)})
			if tt.refusal != "" {
				mapper.refuse("", "call-1", tt.refusal)
			}
			opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(tt.status)}
			if tt.content != "" {
				opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(tt.content))}))
			}
			if tt.output != nil {
				opts = append(opts, acp.WithUpdateRawOutput(tt.output))
			}
			events := mapper.eventsFromNotification(acp.SessionNotification{Update: acp.UpdateToolCall(acp.ToolCallId("call-1"), opts...)})
			if len(events) != 1 || events[0].Type != event.ToolCallEnd {
				t.Fatalf("events = %#v, want one end", events)
			}
			if !resultHas(events[0].Result, tt.want) {
				t.Fatalf("result = %#v, want it to carry %#v", events[0].Result, tt.want)
			}
		})
	}
}

// resultHas reports that the native shape is present in a tool result. What
// the agent reported beyond it travels alongside and is not pinned here.
func resultHas(result any, want map[string]any) bool {
	got, ok := result.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range want {
		if !reflect.DeepEqual(got[key], value) {
			return false
		}
	}
	return true
}

// The agent's own fields stay with the result: nothing reads them today, and
// history must not quietly lose them.
func TestACPToolResultKeepsWhatTheAgentReportedBeyondTheNativeShape(t *testing.T) {
	t.Parallel()
	mapper := newACPToolEventMapper(acpprofile.ToolQuirks{})
	mapper.eventsFromNotification(acp.SessionNotification{Update: acp.StartToolCall(acp.ToolCallId("call-1"), "pwd",
		acp.WithStartKind(acp.ToolKindExecute), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(map[string]any{"command": "pwd"}),
	)})
	events := mapper.eventsFromNotification(acp.SessionNotification{Update: acp.UpdateToolCall(acp.ToolCallId("call-1"),
		acp.WithUpdateStatus(acp.ToolCallStatusCompleted),
		acp.WithUpdateRawOutput(map[string]any{"output": "/data\n", "exit_code": 0, "duration_ms": 12, "truncated": false}),
	)})
	want := map[string]any{"stdout": "/data\n", "exit_code": 0, "duration_ms": 12, "truncated": false}
	if len(events) != 1 || !reflect.DeepEqual(events[0].Result, want) {
		t.Fatalf("result = %#v, want %#v", events[0].Result, want)
	}
}

// testdata/grok-tool-calls.jsonl is one Grok 1.0.52 turn as its tool updates
// arrived: exec, read, write and edit, each succeeding and failing.
func TestACPToolResultsOfARecordedGrokTurn(t *testing.T) {
	t.Parallel()

	recorded, err := os.ReadFile("testdata/grok-tool-calls.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	failure := func(text string) map[string]any {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": text}}}
	}
	want := []any{
		map[string]any{"stdout": "hello\n", "exit_code": 0},
		map[string]any{"stdout": "ls: cannot access '/nonexistent-dir': No such file or directory\n", "exit_code": 2},
		map[string]any{"content": "1→round3\n"},
		failure("Error: /data/no-such-file.txt does not exist.\nNote: your current working directory is /data"),
		map[string]any{"ok": true},
		map[string]any{"ok": true},
		failure("The string to replace was not found in the file, use the read_file tool to see the correct string. The user may have changed the file since you last read it."),
		failure("Tool `write` failed: IO Error: No such file or directory (os error 2)"),
	}
	// A session on a shared connection maps with the default quirks.
	mapper := newACPToolEventMapper(acpprofile.ToolQuirks{})
	var got []any
	for _, line := range bytes.Split(bytes.TrimSpace(recorded), []byte("\n")) {
		var update acp.SessionNotification
		if err := json.Unmarshal(line, &update); err != nil {
			t.Fatal(err)
		}
		for _, ev := range mapper.eventsFromNotification(update) {
			if ev.Type == event.ToolCallEnd {
				got = append(got, ev.Result)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("results =\n%#v\nwant %d results", got, len(want))
	}
	for i := range want {
		if !resultHas(got[i], want[i].(map[string]any)) {
			t.Fatalf("result %d = %#v, want it to carry %#v", i, got[i], want[i])
		}
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

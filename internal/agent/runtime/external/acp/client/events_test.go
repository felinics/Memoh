package client

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// eventLog is a prompt sink that keeps every event the session hands it,
// including terminal decisions recorded after the prompt was stopped.
type eventLog struct {
	mu     sync.Mutex
	events []event.StreamEvent
}

func (l *eventLog) EmitStreamEvent(ev event.StreamEvent) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *eventLog) RecordTerminalDecision(ev event.StreamEvent) { l.EmitStreamEvent(ev) }

func (l *eventLog) all() []event.StreamEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]event.StreamEvent(nil), l.events...)
}

func TestACPGenericExecuteToolMapsToNativeExecEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper()
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
	mapper := newACPToolEventMapper()
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

func TestACPGenericEditDiffMapsToNativeEditEvents(t *testing.T) {
	t.Parallel()

	mapper := newACPToolEventMapper()
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

package codex

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/toolresult"
	"github.com/felinics/memoh/internal/agent/toolresult/conformance"
	"github.com/felinics/memoh/internal/runtimekind"
)

// Thread items as app-server sends them in item/started and item/completed.
// Each sample is the started item followed by the completed one.
var codexToolSamples = []struct{ name, started, completed string }{
	{
		name:      "command with output",
		started:   `{"type":"commandExecution","id":"call_exec_ok","command":"/bin/bash -lc \"TZ='Asia/Shanghai' date '+%Y-%m-%d %H:%M:%S %Z'\"","cwd":"/data","processId":"4121","source":"agent","status":"inProgress","commandActions":[{"type":"unknown","command":"date"}]}`,
		completed: `{"type":"commandExecution","id":"call_exec_ok","command":"/bin/bash -lc \"TZ='Asia/Shanghai' date '+%Y-%m-%d %H:%M:%S %Z'\"","cwd":"/data","processId":"4121","source":"agent","status":"completed","commandActions":[{"type":"unknown","command":"date"}],"aggregatedOutput":"2026-10-24 01:24:46 CST\n","exitCode":0,"durationMs":31}`,
	},
	{
		name:      "command exiting non-zero",
		started:   `{"type":"commandExecution","id":"call_exec_exit2","command":"/bin/bash -lc \"ls /nonexistent-dir\"","cwd":"/data","source":"agent","status":"inProgress","commandActions":[]}`,
		completed: `{"type":"commandExecution","id":"call_exec_exit2","command":"/bin/bash -lc \"ls /nonexistent-dir\"","cwd":"/data","source":"agent","status":"failed","commandActions":[],"aggregatedOutput":"ls: cannot access '/nonexistent-dir': No such file or directory\n","exitCode":2,"durationMs":9}`,
	},
	{
		name:      "command without output",
		started:   `{"type":"commandExecution","id":"call_exec_quiet","command":"/bin/bash -lc true","cwd":"/data","source":"agent","status":"inProgress","commandActions":[]}`,
		completed: `{"type":"commandExecution","id":"call_exec_quiet","command":"/bin/bash -lc true","cwd":"/data","source":"agent","status":"completed","commandActions":[],"aggregatedOutput":"","exitCode":0,"durationMs":3}`,
	},
	{
		name:      "completed command without exit code",
		started:   `{"type":"commandExecution","id":"call_exec_nocode","command":"/bin/bash -lc pwd","cwd":"/data","source":"agent","status":"inProgress","commandActions":[]}`,
		completed: `{"type":"commandExecution","id":"call_exec_nocode","command":"/bin/bash -lc pwd","cwd":"/data","source":"agent","status":"completed","commandActions":[],"aggregatedOutput":"/data\n"}`,
	},
	{
		name:      "declined command",
		started:   `{"type":"commandExecution","id":"call_exec_declined","command":"/bin/bash -lc \"rm -rf /data/x\"","cwd":"/data","source":"agent","status":"inProgress","commandActions":[]}`,
		completed: `{"type":"commandExecution","id":"call_exec_declined","command":"/bin/bash -lc \"rm -rf /data/x\"","cwd":"/data","source":"agent","status":"declined","commandActions":[]}`,
	},
	{
		name:      "file changes",
		started:   `{"type":"fileChange","id":"call_patch_ok","status":"inProgress","changes":[{"path":"/data/a.txt","kind":{"type":"add"},"diff":"hello\n"},{"path":"/data/b.txt","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-x\n+y\n"},{"path":"/data/c.txt","kind":{"type":"delete"},"diff":"gone\n"}]}`,
		completed: `{"type":"fileChange","id":"call_patch_ok","status":"completed","changes":[{"path":"/data/a.txt","kind":{"type":"add"},"diff":"hello\n"},{"path":"/data/b.txt","kind":{"type":"update","move_path":null},"diff":"@@ -1 +1 @@\n-x\n+y\n"},{"path":"/data/c.txt","kind":{"type":"delete"},"diff":"gone\n"}]}`,
	},
	{
		name:      "declined file changes",
		started:   `{"type":"fileChange","id":"call_patch_declined","status":"inProgress","changes":[{"path":"/data/a.txt","kind":{"type":"add"},"diff":"hello\n"}]}`,
		completed: `{"type":"fileChange","id":"call_patch_declined","status":"declined","changes":[{"path":"/data/a.txt","kind":{"type":"add"},"diff":"hello\n"}]}`,
	},
	{
		name:      "web search",
		started:   `{"type":"webSearch","id":"ws_1","query":"","action":{"type":"other"}}`,
		completed: `{"type":"webSearch","id":"ws_1","query":"example domain iana","action":{"type":"search","query":"example domain iana"}}`,
	},
}

func codexSampleEvents(t *testing.T) []event.StreamEvent {
	t.Helper()
	turn := newTurnState(t.Context(), external.PromptInput{Sink: &captureSink{}}, "thread", nil, nil, nil, nil, slog.Default())
	defer turn.close()
	for _, sample := range codexToolSamples {
		for i, raw := range []string{sample.started, sample.completed} {
			var item protocol.ThreadItem
			if err := json.Unmarshal([]byte(raw), &item); err != nil {
				t.Fatalf("%s: %v", sample.name, err)
			}
			if i == 0 {
				turn.handleItemStarted(&item)
			} else {
				turn.handleItemCompleted(&item)
			}
		}
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	return append([]event.StreamEvent(nil), turn.events...)
}

func TestCodexToolResultsAreNativeFormat(t *testing.T) {
	events := codexSampleEvents(t)
	conformance.Record(t, string(runtimekind.Codex), events, conformance.Options{
		EmptyOutput: []string{"call_exec_quiet", "ws_1"},
	})
}

func TestCodexCommandKeepsExitCodeAndOutput(t *testing.T) {
	ends := map[string]event.StreamEvent{}
	for _, ev := range codexSampleEvents(t) {
		if ev.Type == event.ToolCallEnd {
			ends[ev.ToolCallID] = ev
		}
	}

	ok, err := toolresult.DecodeStrict[toolresult.Exec](ends["call_exec_ok"].Result)
	if err != nil || ok.Stdout != "2026-10-24 01:24:46 CST\n" || ok.Stderr != "" || ok.ExitCode == nil || *ok.ExitCode != 0 {
		t.Fatalf("command result = %#v, %v", ends["call_exec_ok"].Result, err)
	}

	exit2 := ends["call_exec_exit2"]
	if exit2.Status == "failed" || exit2.Error != "" {
		t.Fatalf("a command that ran and exited non-zero is not a failed call: %#v", exit2)
	}
	got, err := toolresult.DecodeStrict[toolresult.Exec](exit2.Result)
	if err != nil || got.ExitCode == nil || *got.ExitCode != 2 || got.Stdout == "" {
		t.Fatalf("non-zero exit result = %#v, %v", exit2.Result, err)
	}

	// The protocol did not report an exit code, so none is recorded.
	nocode := ends["call_exec_nocode"]
	if _, has := nocode.Result.(map[string]any)["exit_code"]; has || nocode.Status == "failed" {
		t.Fatalf("completed command without exitCode = %#v", nocode)
	}
	if got, err := toolresult.DecodeStrict[toolresult.Exec](nocode.Result); err != nil || got.Stdout != "/data\n" || got.ExitCode != nil {
		t.Fatalf("result = %#v, %v", nocode.Result, err)
	}

	declined := ends["call_exec_declined"]
	if declined.Status != "failed" {
		t.Fatalf("a declined command never ran: %#v", declined)
	}
}

func TestCodexFileChangeIsApplyPatch(t *testing.T) {
	var start, end event.StreamEvent
	for _, ev := range codexSampleEvents(t) {
		if ev.ToolCallID != "call_patch_ok" {
			continue
		}
		switch ev.Type {
		case event.ToolCallStart:
			start = ev
		case event.ToolCallEnd:
			end = ev
		}
	}
	if start.ToolName != "apply_patch" || end.ToolName != "apply_patch" {
		t.Fatalf("file change names = %q / %q, want apply_patch", start.ToolName, end.ToolName)
	}
	input, _ := start.Input.(map[string]any)
	changes, _ := input["changes"].([]any)
	if len(changes) != 3 {
		t.Fatalf("apply_patch input = %#v, want the three changes", start.Input)
	}
	result, err := toolresult.DecodeStrict[toolresult.ApplyPatch](end.Result)
	if err != nil || len(result.Added) != 1 || len(result.Modified) != 1 || len(result.Deleted) != 1 || len(result.Files) != 3 {
		t.Fatalf("apply_patch result = %#v, %v", end.Result, err)
	}
}

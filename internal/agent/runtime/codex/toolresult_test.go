package codex

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestToolResultsReachNativeDisplayFields(t *testing.T) {
	for _, tc := range []struct{ name, raw, tool, field string }{
		{"command", `{"type":"commandExecution","id":"cmd","command":"echo hello","status":"completed","aggregatedOutput":"hello","exitCode":0}`, "exec", "stdout"},
		{"failed command", `{"type":"commandExecution","id":"cmd","command":"exit 2","status":"failed","aggregatedOutput":"diagnostic","exitCode":2}`, "exec", "stdout"},
		{"unknown exit code", `{"type":"commandExecution","id":"cmd","command":"pwd","status":"completed","aggregatedOutput":"/data"}`, "exec", "stdout"},
		{"file changes", `{"type":"fileChange","id":"patch","status":"completed","changes":[{"path":"/data/a","kind":{"type":"add"},"diff":"hello"}]}`, "apply_patch", "changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := newTurnState(t.Context(), external.PromptInput{Sink: &captureSink{}}, "thread", nil, nil, nil, nil, slog.Default())
			defer turn.close()
			var item protocol.ThreadItem
			if err := json.Unmarshal([]byte(tc.raw), &item); err != nil {
				t.Fatal(err)
			}
			turn.handleItemStarted(&item)
			turn.handleItemCompleted(&item)
			turn.mu.Lock()
			defer turn.mu.Unlock()
			var end *event.StreamEvent
			for i := range turn.events {
				ev := &turn.events[i]
				if ev.Type == event.ToolCallStart && ev.ToolName != tc.tool {
					t.Fatalf("start tool = %s", ev.ToolName)
				}
				if ev.Type == event.ToolCallEnd {
					end = ev
				}
			}
			if end == nil || end.ToolName != tc.tool {
				t.Fatalf("missing %s completion", tc.tool)
			}
			result := end.Result.(map[string]any)
			if result[tc.field] == nil || result[tc.field] == "" {
				t.Fatalf("output missing: %#v", result)
			}
			if item.CommandExecution != nil {
				cmd := item.CommandExecution
				if result["stdout"] != *cmd.AggregatedOutput {
					t.Fatalf("output lost: %#v", result)
				}
				code, present := result["exit_code"]
				if present != (cmd.ExitCode != nil) || (present && code != *cmd.ExitCode) {
					t.Fatalf("exit code changed: %#v", result)
				}
				if cmd.ExitCode != nil && *cmd.ExitCode != 0 && end.Status != "failed" {
					t.Fatal("nonzero status changed")
				}
			}
		})
	}
}

package codex

import (
	"encoding/json"
	"strings"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/toolresult"
)

// Codex items are reported under the native tool names exec, apply_patch and
// web_search, so each item completes with that tool's result format.

const (
	toolExec       = "exec"
	toolApplyPatch = "apply_patch"
	toolWebSearch  = "web_search"
)

// commandExecutionEnd completes an exec call. A command that ran and exited
// non-zero is an ordinary exec result; only a command that never ran (declined,
// failed to start, interrupted) fails the call.
func commandExecutionEnd(cmd *protocol.CommandExecutionThreadItem, name string) event.StreamEvent {
	ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: cmd.ID, ToolName: name}
	output := ""
	if cmd.AggregatedOutput != nil {
		output = *cmd.AggregatedOutput
	}
	switch {
	case cmd.Status == protocol.CommandExecutionStatusDeclined:
		return failedEnd(ev, "the command was declined"+outputSuffix(output))
	case cmd.ExitCode != nil:
		// aggregatedOutput merges stdout and stderr, so it is all reported as
		// stdout.
		ev.Result = toolresult.Exec{Stdout: output, ExitCode: toolresult.Ptr(int(*cmd.ExitCode))}.Map()
	case cmd.Status == protocol.CommandExecutionStatusCompleted:
		ev.Result = toolresult.Exec{Stdout: output}.Map()
	case cmd.Status == protocol.CommandExecutionStatusFailed:
		return failedEnd(ev, "the command failed to run"+outputSuffix(output))
	default:
		return failedEnd(ev, "the command did not finish"+outputSuffix(output))
	}
	return ev
}

// fileChangeEnd completes an apply_patch call.
func fileChangeEnd(fc *protocol.FileChangeThreadItem, name string) event.StreamEvent {
	ev := event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: fc.ID, ToolName: name}
	switch fc.Status {
	case protocol.PatchApplyStatusCompleted:
		ev.Result = applyPatchResult(fc.Changes).Map()
		return ev
	case protocol.PatchApplyStatusDeclined:
		return failedEnd(ev, "the file changes were declined")
	case protocol.PatchApplyStatusFailed:
		return failedEnd(ev, "the file changes failed to apply")
	default:
		return failedEnd(ev, "the file changes did not finish")
	}
}

// fileChangeToolInput is the apply_patch input: the item's change list, each
// change with its path, kind and unified diff.
func fileChangeToolInput(fc *protocol.FileChangeThreadItem) map[string]any {
	changes := fc.Changes
	if changes == nil {
		changes = []protocol.FileUpdateChange{}
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return map[string]any{"changes": []any{}}
	}
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"changes": []any{}}
	}
	return map[string]any{"changes": decoded}
}

func applyPatchResult(changes []protocol.FileUpdateChange) toolresult.ApplyPatch {
	var added, modified, deleted []string
	for _, change := range changes {
		switch change.Kind.Tag {
		case protocol.PatchChangeKindTagAdd:
			added = append(added, change.Path)
		case protocol.PatchChangeKindTagDelete:
			deleted = append(deleted, change.Path)
		default:
			modified = append(modified, change.Path)
		}
	}
	return toolresult.NewApplyPatch(added, modified, deleted)
}

// webSearchEnd completes a web_search call. The item's results are opaque JSON
// with no defined fields, so only the query is reported.
func webSearchEnd(search *protocol.WebSearchThreadItem, name string) event.StreamEvent {
	return event.StreamEvent{
		Type: event.ToolCallEnd, ToolCallID: search.ID, ToolName: name,
		Result: toolresult.WebSearch{Query: search.Query}.Map(),
	}
}

func failedEnd(ev event.StreamEvent, text string) event.StreamEvent {
	ev.Status = "failed"
	ev.Error = text
	ev.Result = toolresult.NewFailure(text).Map()
	return ev
}

func outputSuffix(output string) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	return ":\n" + output
}

package client

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/toolresult"
	"github.com/felinics/memoh/internal/agent/toolresult/conformance"
	"github.com/felinics/memoh/internal/runtimekind"
)

// A turn of a generic ACP agent: tool calls reported through tool_call
// updates, plus a command run through the client's terminal.
func genericACPSampleEvents(t *testing.T) []event.StreamEvent {
	t.Helper()
	var events []event.StreamEvent
	mapper := newACPToolEventMapper(acpprofile.DefaultToolQuirks())
	call := func(id string, kind acp.ToolKind, input map[string]any, status acp.ToolCallStatus, content string, output any) {
		events = append(events, mapper.eventsFromNotification(acp.SessionNotification{Update: acp.StartToolCall(
			acp.ToolCallId(id), "tool", acp.WithStartKind(kind), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(input),
		)})...)
		opts := []acp.ToolCallUpdateOpt{acp.WithUpdateStatus(status)}
		if content != "" {
			opts = append(opts, acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(content))}))
		}
		if output != nil {
			opts = append(opts, acp.WithUpdateRawOutput(output))
		}
		events = append(events, mapper.eventsFromNotification(acp.SessionNotification{Update: acp.UpdateToolCall(acp.ToolCallId(id), opts...)})...)
	}
	call("exec-ok", acp.ToolKindExecute, map[string]any{"command": "pwd"}, acp.ToolCallStatusCompleted, "",
		map[string]any{"stdout": "/data\n", "exit_code": 0})
	call("exec-exit1", acp.ToolKindExecute, map[string]any{"command": "grep x a.txt"}, acp.ToolCallStatusCompleted, "",
		map[string]any{"stdout": "", "stderr": "grep: a.txt: No such file or directory\n", "exit_code": 2})
	call("exec-rejected", acp.ToolKindExecute, map[string]any{"command": "rm a.txt"}, acp.ToolCallStatusFailed,
		"User rejected the execution", nil)
	call("read-ok", acp.ToolKindRead, map[string]any{"path": "/data/a.txt"}, acp.ToolCallStatusCompleted, "alpha\nbeta\n", nil)

	emitter := &toolEventEmitter{}
	emitter.setPromptState(nil, EventSinkFunc(func(ev event.StreamEvent) { events = append(events, ev) }), ToolOutputLimit{})
	manager := &terminalManager{events: emitter}
	exitCode := 0
	manager.emitTerminalEnd(&terminal{
		id: "terminal-1", input: map[string]any{"command": "date"},
		output: "Sat Oct 24 01:24:46 CST 2026\n", exitCode: &exitCode, done: make(chan struct{}),
	})
	return events
}

func TestACPAgentToolResultsAreNativeFormat(t *testing.T) {
	conformance.Record(t, string(runtimekind.ACPAgent), genericACPSampleEvents(t), conformance.Options{Extras: true})
}

func TestGrokToolResultsAreNativeFormat(t *testing.T) {
	recorded, err := os.ReadFile("testdata/grok-tool-calls.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var events []event.StreamEvent
	mapper := newACPToolEventMapper(acpprofile.ToolQuirks{})
	for _, line := range bytes.Split(bytes.TrimSpace(recorded), []byte("\n")) {
		var update acp.SessionNotification
		if err := json.Unmarshal(line, &update); err != nil {
			t.Fatal(err)
		}
		events = append(events, mapper.eventsFromNotification(update)...)
	}
	conformance.Record(t, string(runtimekind.GrokBuild), events, conformance.Options{Extras: true})
}

// A failed ACP call keeps the failure record; what the agent reported besides
// stays beside it.
func TestACPFailureKeepsAgentFieldsBesideTheRecord(t *testing.T) {
	var failed event.StreamEvent
	for _, ev := range genericACPSampleEvents(t) {
		if ev.Type == event.ToolCallEnd && ev.ToolCallID == "exec-rejected" {
			failed = ev
		}
	}
	failure, err := toolresult.DecodeStrict[toolresult.Failure](failed.Result)
	if err != nil || failure.Text() != "User rejected the execution" {
		t.Fatalf("result = %#v, %v", failed.Result, err)
	}
}

// An exit code is recorded only when the agent reported one.
func TestACPExecResultHasExitCodeOnlyWhenReported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want *int
	}{
		{"not reported", map[string]any{"stdout": "hi\n"}, nil},
		{"reported", map[string]any{"stdout": "hi\n", "exit_code": 3}, toolresult.Ptr(3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mapper := newACPToolEventMapper(acpprofile.ToolQuirks{})
			mapper.eventsFromNotification(acp.SessionNotification{Update: acp.StartToolCall(acp.ToolCallId("c"), "echo",
				acp.WithStartKind(acp.ToolKindExecute), acp.WithStartStatus(acp.ToolCallStatusInProgress), acp.WithStartRawInput(map[string]any{"command": "echo hi"}),
			)})
			events := mapper.eventsFromNotification(acp.SessionNotification{Update: acp.UpdateToolCall(acp.ToolCallId("c"),
				acp.WithUpdateStatus(acp.ToolCallStatusCompleted), acp.WithUpdateRawOutput(tc.raw),
			)})
			if len(events) != 1 {
				t.Fatalf("events = %#v", events)
			}
			_, has := events[0].Result.(map[string]any)["exit_code"]
			got, err := toolresult.DecodeStrict[toolresult.Exec](events[0].Result)
			if err != nil || has != (tc.want != nil) || !reflect.DeepEqual(got.ExitCode, tc.want) {
				t.Fatalf("result = %#v, %v", events[0].Result, err)
			}
		})
	}
}

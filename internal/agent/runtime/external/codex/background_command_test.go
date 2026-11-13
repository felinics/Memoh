package codex

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
)

// The fixtures are app-server notifications recorded from Codex 0.167.2:
//
//   - background_same_turn: `sleep 20; exit 1` is left running while the model
//     runs another command; its exit is reported after that command's.
//   - background_cross_turn_1 / _2: turn 1 leaves `sleep 45; echo done-a;
//     exit 3` running and ends; turn 2 waits for it. The exit arrives during
//     turn 2, stamped with turn 1's id.

func replay(t *testing.T, turn *turnState, path string, skip func(method string, params json.RawMessage) bool) {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: path is a fixture under testdata, named by the test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		var line struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if skip != nil && skip(line.Method, line.Params) {
			continue
		}
		notify(t, turn, line.Method, string(line.Params))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func notify(t *testing.T, turn *turnState, method, params string) {
	t.Helper()
	decoded, ok, err := protocol.DecodeServerNotificationParams(method, []byte(params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if ok {
		turn.handleNotification(decoded)
	}
}

func newReplayTurn(t *testing.T, input external.PromptInput) (*turnState, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	input.Sink = sink
	return newTurnState(t.Context(), input, "thread", nil, nil, nil, nil, slog.Default()), sink
}

// finishReplay settles the turn as a normal end does and returns its
// transcript.
func finishReplay(turn *turnState) []sdk.Message {
	turn.settleBackgroundCommands()
	out := turn.stream.Transcript("")
	turn.close()
	return out
}

// commandCall returns the id of the command call whose input contains text.
func commandCall(t *testing.T, transcript []sdk.Message, text string) string {
	t.Helper()
	for _, msg := range transcript {
		for _, part := range msg.Content {
			call, ok := part.(sdk.ToolCallPart)
			if ok && strings.Contains(call.Input.String(), text) {
				return call.ToolCallID
			}
		}
	}
	t.Fatalf("no call running %q in %#v", text, transcript)
	return ""
}

// resultFor returns the result recorded for callID, failing unless it is in
// the tool messages directly after the message that made the call.
func resultFor(t *testing.T, transcript []sdk.Message, callID string) map[string]any {
	t.Helper()
	for i, msg := range transcript {
		if msg.Role != sdk.MessageRoleAssistant || !messageMakesCall(msg, callID) {
			continue
		}
		for j := i + 1; j < len(transcript) && transcript[j].Role == sdk.MessageRoleTool; j++ {
			for _, part := range transcript[j].Content {
				result, ok := part.(sdk.ToolResultPart)
				if !ok || result.ToolCallID != callID {
					continue
				}
				var value map[string]any
				if err := json.Unmarshal(result.Result.JSON, &value); err != nil {
					t.Fatalf("result of %s: %v", callID, err)
				}
				return value
			}
		}
		t.Fatalf("result of %s is not next to its call: %#v", callID, transcript)
	}
	t.Fatalf("no call %s", callID)
	return nil
}

func messageMakesCall(msg sdk.Message, callID string) bool {
	for _, part := range msg.Content {
		if call, ok := part.(sdk.ToolCallPart); ok && call.ToolCallID == callID {
			return true
		}
	}
	return false
}

func endsFor(sink *captureSink, callID string) []event.StreamEvent {
	var ends []event.StreamEvent
	for _, ev := range sink.events {
		if ev.Type == event.ToolCallEnd && ev.ToolCallID == callID {
			ends = append(ends, ev)
		}
	}
	return ends
}

// A command left running while the model went on reports its exit later. Its
// real result belongs with its call, not after the model's later work, where
// the transcript repair would close the call as interrupted and drop it.
func TestBackgroundCommandResultStaysWithItsCall(t *testing.T) {
	turn, _ := newReplayTurn(t, external.PromptInput{})
	replay(t, turn, "testdata/background_same_turn.jsonl", nil)
	transcript := finishReplay(turn)

	result := resultFor(t, transcript, commandCall(t, transcript, "sleep 20; exit 1"))
	if result["exit_code"] != float64(1) || result["status"] == backgroundCommandStatus {
		t.Fatalf("background command result = %#v, want its real exit", result)
	}
	if result := resultFor(t, transcript, commandCall(t, transcript, "B ran")); result["exit_code"] != float64(0) {
		t.Fatalf("foreground command result = %#v", result)
	}
}

// A command still running when the turn ends normally is recorded as running
// in the background, the one thing known about it then.
func TestCommandLeftRunningAtTurnEndIsRecordedAsRunning(t *testing.T) {
	turn, _ := newReplayTurn(t, external.PromptInput{})
	replay(t, turn, "testdata/background_cross_turn_1.jsonl", nil)
	transcript := finishReplay(turn)

	result := resultFor(t, transcript, commandCall(t, transcript, "sleep 45"))
	if result["status"] != backgroundCommandStatus || result["process_id"] != "18438" {
		t.Fatalf("result = %#v, want running in the background as process 18438", result)
	}
}

// The exit of a command an earlier turn started arrives during a later turn,
// stamped with the earlier turn's id. The later turn did not start it and has
// no call for it, so it records nothing for it.
func TestLaterTurnDoesNotAdoptEarlierCommands(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{})
	replay(t, turn, "testdata/background_cross_turn_2.jsonl", nil)
	transcript := finishReplay(turn)

	const earlier = "exec-d1a887fa-7bf9-4814-92f4-a9fe95f48600"
	for _, ev := range sink.events {
		if ev.ToolCallID == earlier {
			t.Fatalf("turn 2 took an event of turn 1's command: %#v", ev)
		}
	}
	for _, msg := range transcript {
		for _, part := range msg.Content {
			if result, ok := part.(sdk.ToolResultPart); ok && result.ToolCallID == earlier {
				t.Fatalf("orphan result in turn 2: %#v", transcript)
			}
		}
	}
}

// A stopped turn ends its commands; it must not claim they are still running.
func TestStoppedTurnDoesNotClaimCommandsRun(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{})
	var turnID string
	replay(t, turn, "testdata/background_cross_turn_1.jsonl", func(method string, params json.RawMessage) bool {
		if method != "turn/completed" {
			return false
		}
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(params, &p)
		turnID = p.Turn.ID
		return true
	})
	notify(t, turn, "turn/completed", `{"threadId":"thread","turn":{"id":"`+turnID+`","items":[],"status":"interrupted"}}`)
	finishReplay(turn)

	for _, ev := range sink.events {
		if ev.Type == event.ToolCallEnd {
			t.Fatalf("a stopped turn recorded %#v", ev)
		}
	}
}

// A goal continues in further Codex turns within one Memoh turn. A command an
// earlier continuation started still belongs to this turn.
func TestContinuationKeepsItsCommands(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{Command: "goal"})
	notify(t, turn, "turn/started", `{"threadId":"thread","turn":{"id":"turn-a","items":[],"status":"inProgress"}}`)
	notify(t, turn, "item/started", `{"threadId":"thread","turnId":"turn-a","item":{"type":"commandExecution","id":"cmd","command":"make","cwd":"/workspace","processId":"7","status":"inProgress","commandActions":[]}}`)
	notify(t, turn, "turn/started", `{"threadId":"thread","turn":{"id":"turn-b","items":[],"status":"inProgress"}}`)
	notify(t, turn, "item/commandExecution/outputDelta", `{"threadId":"thread","turnId":"turn-a","itemId":"cmd","delta":"built\n"}`)
	notify(t, turn, "item/completed", `{"threadId":"thread","turnId":"turn-a","item":{"type":"commandExecution","id":"cmd","command":"make","cwd":"/workspace","processId":"7","status":"completed","exitCode":0,"aggregatedOutput":"built\n","commandActions":[]}}`)
	finishReplay(turn)

	progress := false
	for _, ev := range sink.events {
		if ev.Type == event.ToolCallProgress && ev.ToolCallID == "cmd" {
			progress = true
		}
	}
	ends := endsFor(sink, "cmd")
	if !progress || len(ends) != 1 || ends[0].Result.(map[string]any)["exit_code"] != int64(0) {
		t.Fatalf("progress %t, ends %#v: want the continuation to keep its command", progress, ends)
	}
}

// Before a new turn learns its id, it must not take an earlier turn's command
// exit, which has no call in it.
func TestNewTurnIgnoresEarlierCommandExitBeforeItsID(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{})
	notify(t, turn, "item/completed", `{"threadId":"thread","turnId":"earlier","item":{"type":"commandExecution","id":"old","command":"sleep 9","cwd":"/workspace","processId":"9","status":"completed","exitCode":0,"commandActions":[]}}`)
	notify(t, turn, "item/commandExecution/outputDelta", `{"threadId":"thread","turnId":"earlier","itemId":"old","delta":"x"}`)
	finishReplay(turn)
	for _, ev := range sink.events {
		if ev.ToolCallID == "old" {
			t.Fatalf("new turn took %#v", ev)
		}
	}
}

// The exit that arrives after the turn end settled the command is not
// recorded a second time.
func TestCommandEndIsRecordedOnce(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{})
	notify(t, turn, "turn/started", `{"threadId":"thread","turn":{"id":"turn-a","items":[],"status":"inProgress"}}`)
	notify(t, turn, "item/started", `{"threadId":"thread","turnId":"turn-a","item":{"type":"commandExecution","id":"cmd","command":"serve","cwd":"/workspace","processId":"5","status":"inProgress","commandActions":[]}}`)
	notify(t, turn, "turn/completed", `{"threadId":"thread","turn":{"id":"turn-a","items":[],"status":"completed"}}`)
	turn.settleBackgroundCommands()
	notify(t, turn, "item/commandExecution/outputDelta", `{"threadId":"thread","turnId":"turn-a","itemId":"cmd","delta":"late"}`)
	notify(t, turn, "item/completed", `{"threadId":"thread","turnId":"turn-a","item":{"type":"commandExecution","id":"cmd","command":"serve","cwd":"/workspace","processId":"5","status":"completed","exitCode":0,"commandActions":[]}}`)
	finishReplay(turn)

	ends := endsFor(sink, "cmd")
	if len(ends) != 1 || ends[0].Result.(map[string]any)["status"] != backgroundCommandStatus {
		t.Fatalf("ends = %#v, want only the turn-end record", ends)
	}
	for _, ev := range sink.events {
		if ev.Type == event.ToolCallProgress && ev.ToolCallID == "cmd" {
			t.Fatalf("progress after the end: %#v", ev)
		}
	}
}

// Every model request ends with a usage report, which tells the idle
// watchdog the agent is working.
func TestUsageReportIsActivity(t *testing.T) {
	turn, sink := newReplayTurn(t, external.PromptInput{})
	replay(t, turn, "testdata/background_same_turn.jsonl", nil)
	finishReplay(turn)

	usage, activity := 0, 0
	f, _ := os.ReadFile("testdata/background_same_turn.jsonl")
	usage = strings.Count(string(f), `"thread/tokenUsage/updated"`)
	for _, ev := range sink.events {
		if ev.Type == event.Progress && ev.ProgressStatus == event.ProgressRuntimeActivity {
			activity++
		}
	}
	if usage == 0 || activity != usage {
		t.Fatalf("activity = %d, usage reports = %d", activity, usage)
	}
}

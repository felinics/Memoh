package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/memohai/memoh/internal/agent/application"
	"github.com/memohai/memoh/internal/agent/runtime/native"
)

const (
	runtimeContractBotID     = "11111111-1111-1111-1111-111111111111"
	runtimeContractSessionID = "22222222-2222-2222-2222-222222222222"
	runtimeContractRunID     = "run-runtime-contract"
)

func rawRuntimeContractEvent(t *testing.T, ev native.StreamEvent) application.WSStreamEvent {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal runtime event: %v", err)
	}
	return data
}

func richActiveRunWSContractScript(t *testing.T) []application.WSStreamEvent {
	t.Helper()
	return []application.WSStreamEvent{
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventAgentStart}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventReasoningDelta, Delta: "I need to inspect the workspace."}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventTextDelta, Delta: "I will check the current state."}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:       native.EventToolCallStart,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Input:      map[string]any{"command": "pwd"},
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:       native.EventToolCallProgress,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   "queued",
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:       native.EventToolCallProgress,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   map[string]any{"stdout": "/workspace\n"},
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:       native.EventToolCallEnd,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Result:     map[string]any{"structuredContent": map[string]any{"stdout": "/workspace\n"}},
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:       native.EventToolApprovalRequest,
			ToolName:   "exec",
			ToolCallID: "call-approval",
			Input:      map[string]any{"command": "rm -rf build"},
			ApprovalID: "approval-1",
			ShortID:    7,
			Status:     "pending",
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:        native.EventUserInputRequest,
			ToolName:    "ask_user",
			ToolCallID:  "call-ask",
			Input:       map[string]any{"questions": []any{map[string]any{"text": "Continue?", "kind": "single_select"}}},
			UserInputID: "input-1",
			ShortID:     8,
			Status:      "pending",
			Metadata: map[string]any{
				"ui_payload": map[string]any{
					"version": 2,
					"questions": []any{map[string]any{
						"id":   "q1",
						"text": "Continue?",
						"kind": "single_select",
						"options": []any{
							map[string]any{"id": "yes", "label": "Yes"},
							map[string]any{"id": "no", "label": "No"},
						},
					}},
				},
			},
		}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventAgentEnd}),
	}
}

func interruptedRunWSContractScript(t *testing.T) []application.WSStreamEvent {
	t.Helper()
	return []application.WSStreamEvent{
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventAgentStart}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventTextDelta, Delta: "partial output"}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventError, Error: "runtime interrupted"}),
		rawRuntimeContractEvent(t, native.StreamEvent{Type: native.EventAgentAbort}),
	}
}

func collectRuntimeContractWSEvents(t *testing.T, script []application.WSStreamEvent, stopAt string) []map[string]any {
	t.Helper()

	closeWriter := make(chan struct{})
	var closeWriterOnce sync.Once
	handlerDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			handlerDone <- err
			return
		}
		defer func() { _ = conn.Close() }()

		writer := newWSWriter(conn)
		eventCh := make(chan application.WSStreamEvent, len(script))
		for _, event := range script {
			eventCh <- event
		}
		close(eventCh)

		(&LocalChannelHandler{logger: slog.Default()}).forwardWSStreamEvents(
			r.Context(),
			r.Context(),
			writer,
			runtimeContractBotID,
			wsTurnRef{RunID: runtimeContractRunID, SessionID: runtimeContractSessionID},
			eventCh,
		)

		<-closeWriter
		writer.Close()
		handlerDone <- nil
	}))
	defer server.Close()
	defer closeWriterOnce.Do(func() { close(closeWriter) })

	client, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = client.Close() }()

	var events []map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := client.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		var event map[string]any
		if err := client.ReadJSON(&event); err != nil {
			t.Fatalf("read ws event: %v; events=%#v", err, events)
		}
		events = append(events, event)
		if event["type"] == stopAt {
			break
		}
	}

	closeWriterOnce.Do(func() { close(closeWriter) })
	select {
	case err := <-handlerDone:
		if err != nil {
			t.Fatalf("handler error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler")
	}

	return events
}

func TestLocalChannelRuntimeContractForwardsRichActiveRunUIState(t *testing.T) {
	t.Parallel()

	events := collectRuntimeContractWSEvents(t, richActiveRunWSContractScript(t), "end")
	if len(events) < 8 {
		t.Fatalf("got %d events, want rich stream events: %#v", len(events), events)
	}
	if events[0]["type"] != "start" {
		t.Fatalf("first event = %#v, want start", events[0])
	}
	// Everything a run emits names the run, and names it the same way, so a
	// subscriber that never sent the submission can still follow it.
	for _, event := range events {
		if _, present := event["stream_id"]; present {
			t.Fatalf("event still carries stream_id: %#v", event)
		}
		if event["run_id"] != runtimeContractRunID {
			t.Fatalf("event run_id = %#v, want %q", event["run_id"], runtimeContractRunID)
		}
	}

	var reasoning, text, execTool, approvalTool, askUserTool map[string]any
	for _, event := range events {
		data, _ := event["data"].(map[string]any)
		switch {
		case data["type"] == "reasoning":
			reasoning = data
		case data["type"] == "text":
			text = data
		case data["type"] == "tool" && data["tool_call_id"] == "call-exec":
			execTool = data
		case data["type"] == "tool" && data["tool_call_id"] == "call-approval":
			approvalTool = data
		case data["type"] == "tool" && data["tool_call_id"] == "call-ask":
			askUserTool = data
		}
	}

	if reasoning["content"] != "I need to inspect the workspace." {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if text["content"] != "I will check the current state." {
		t.Fatalf("text = %#v", text)
	}
	if execTool["running"] != false {
		t.Fatalf("exec tool = %#v, want completed running=false", execTool)
	}
	if progress, _ := execTool["progress"].([]any); len(progress) != 2 {
		t.Fatalf("exec progress = %#v, want two entries", execTool["progress"])
	}
	approval, _ := approvalTool["approval"].(map[string]any)
	if approval["approval_id"] != "approval-1" || approval["can_approve"] != true {
		t.Fatalf("approval tool = %#v", approvalTool)
	}
	userInput, _ := askUserTool["user_input"].(map[string]any)
	if userInput["user_input_id"] != "input-1" || userInput["can_respond"] != true {
		t.Fatalf("ask_user tool = %#v", askUserTool)
	}
	if events[len(events)-1]["type"] != "end" {
		t.Fatalf("last event = %#v, want end", events[len(events)-1])
	}
}

func TestLocalChannelRuntimeContractForwardsInterruptedRunError(t *testing.T) {
	t.Parallel()

	events := collectRuntimeContractWSEvents(t, interruptedRunWSContractScript(t), "end")
	if len(events) != 4 {
		t.Fatalf("events = %#v, want start, partial message, error, end", events)
	}
	if events[0]["type"] != "start" || events[1]["type"] != "message" || events[2]["type"] != "error" || events[3]["type"] != "end" {
		t.Fatalf("unexpected interrupted event sequence: %#v", events)
	}
	if events[2]["message"] != "runtime interrupted" {
		t.Fatalf("error event = %#v", events[2])
	}
}

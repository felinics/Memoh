package chat

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

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/application"
	"github.com/memohai/memoh/internal/apperror"
)

const (
	runtimeContractBotID     = "11111111-1111-1111-1111-111111111111"
	runtimeContractSessionID = "22222222-2222-2222-2222-222222222222"
	runtimeContractStreamID  = "stream-runtime-contract"
)

func rawRuntimeContractEvent(t *testing.T, ev agentdomain.StreamEvent) application.WSStreamEvent {
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
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.AgentStart}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.ReasoningDelta, Delta: "I need to inspect the workspace."}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.TextDelta, Delta: "I will check the current state."}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallStart,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Input:      map[string]any{"command": "pwd"},
		}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallProgress,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   "queued",
		}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallProgress,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Progress:   map[string]any{"stdout": "/workspace\n"},
		}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallEnd,
			ToolName:   "exec",
			ToolCallID: "call-exec",
			Result:     map[string]any{"structuredContent": map[string]any{"stdout": "/workspace\n"}},
		}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:       agentdomain.ToolApprovalRequest,
			ToolName:   "exec",
			ToolCallID: "call-approval",
			Input:      map[string]any{"command": "rm -rf build"},
			ApprovalID: "approval-1",
			ShortID:    7,
			Status:     "pending",
		}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{
			Type:        agentdomain.UserInputRequest,
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
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.AgentEnd}),
	}
}

func interruptedRunWSContractScript(t *testing.T) []application.WSStreamEvent {
	t.Helper()
	return []application.WSStreamEvent{
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.AgentStart}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.TextDelta, Delta: "partial output"}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.Error, Error: "runtime interrupted"}),
		rawRuntimeContractEvent(t, agentdomain.StreamEvent{Type: agentdomain.AgentAbort}),
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
			runtimeContractSessionID,
			runtimeContractStreamID,
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
	// The runtime reports stream failures as free text we cannot classify, so
	// the kind stays internal — but the text itself reaches the client as an
	// upstream quotation, because for provider calls this service is a proxy
	// and that sentence is the only actionable thing in the response.
	if events[2]["kind"] != apperror.KindInternal.String() {
		t.Fatalf("error event = %#v", events[2])
	}
	upstream, ok := events[2]["upstream"].(map[string]any)
	if !ok || upstream["message"] != "runtime interrupted" {
		t.Fatalf("upstream quote lost: %#v", events[2])
	}
}

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

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
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
			// A zero handle: this asserts the contract the *socket* sees, which
			// must hold on its own. Publication to the session runtime is a
			// separate obligation and must not be what makes these frames appear.
			sessionruntime.RunHandle{},
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

// A run's output belongs to its session, not to the socket that started it, so
// the initiating connection is fed nothing here that a second subscriber would
// not also receive — it reads the run through the same snapshot and deltas.
//
// The script is a full rich run followed by an error. Reading exactly one frame
// is what makes the negative deterministic: every other event was queued ahead
// of the error, so anything that still rendered to the socket would have
// arrived first.
func TestLocalChannelRuntimeContractSendsOnlyErrorsToTheInitiatingSocket(t *testing.T) {
	t.Parallel()

	script := append(
		richActiveRunWSContractScript(t),
		rawRuntimeContractEvent(t, native.StreamEvent{
			Type:  native.EventError,
			Code:  string(apperror.CodeContextBudgetUnsatisfied),
			Error: "The model context window is too small for this request.",
		}),
	)
	events := collectRuntimeContractWSEvents(t, script, "error")
	if len(events) != 1 {
		t.Fatalf("events = %#v, want the error alone", events)
	}
	if events[0]["message"] != "The model context window is too small for this request." {
		t.Fatalf("error event = %#v", events[0])
	}
	if events[0]["code"] != string(apperror.CodeContextBudgetUnsatisfied) {
		t.Fatalf("error code = %#v, want %q", events[0]["code"], apperror.CodeContextBudgetUnsatisfied)
	}
	// The frame names the run, and only the run: a subscriber that never sent
	// the submission still has to recognise which turn failed.
	if events[0]["run_id"] != runtimeContractRunID {
		t.Fatalf("error run_id = %#v, want %q", events[0]["run_id"], runtimeContractRunID)
	}
	if _, present := events[0]["invocation_id"]; present {
		t.Fatalf("error event names two ids: %#v", events[0])
	}
}

// An error still reaches the caller when the run produced output first: the
// published state names the run as failed, but only this frame tells the
// connection that made the send what went wrong.
func TestLocalChannelRuntimeContractForwardsInterruptedRunError(t *testing.T) {
	t.Parallel()

	events := collectRuntimeContractWSEvents(t, interruptedRunWSContractScript(t), "error")
	if len(events) != 1 || events[0]["message"] != "runtime interrupted" {
		t.Fatalf("events = %#v, want the interruption reported once", events)
	}
}

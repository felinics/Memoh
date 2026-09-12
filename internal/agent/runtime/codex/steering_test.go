package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type testSteering struct {
	mu       sync.Mutex
	inputs   []external.SteerInput
	accepted []string
	enabled  int
	closed   int
}

func (s *testSteering) Enable(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled++
	return nil
}

func (*testSteering) Wake() <-chan struct{} { return nil }
func (s *testSteering) Next(context.Context) (external.SteerInput, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputs) == 0 {
		return external.SteerInput{}, false, nil
	}
	return s.inputs[0], true, nil
}

func (s *testSteering) Accepted(_ context.Context, id string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accepted = append(s.accepted, id)
	if len(s.inputs) > 0 && s.inputs[0].ID == id {
		s.inputs = s.inputs[1:]
	}
	return nil
}

func (s *testSteering) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func TestCodexSteerConsumerProcessesTwoInputsInOrder(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	source := &testSteering{inputs: []external.SteerInput{{ID: "one", Text: "same"}, {ID: "two", Text: "same"}}}
	turn := newTurnState(t.Context(), external.PromptInput{Steering: source, Sink: external.EventSinkFunc(func(event.StreamEvent) {})}, "thread", nil, nil, nil, nil, slog.Default())
	defer turn.close()
	turn.setTurnID("current")
	conn := newConn(client, &appServer{logger: slog.Default(), turns: map[string]*turnState{"thread": turn}}, slog.Default())
	defer func() { _ = conn.Close() }()
	stop := startSteering(t.Context(), conn, turn)
	defer stop()
	turn.emit(event.StreamEvent{Type: event.TextDelta, Delta: "before"})
	scanner := bufio.NewScanner(server)
	for _, id := range []string{"one", "two"} {
		if err := server.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing steer %s: %v", id, scanner.Err())
		}
		var request struct {
			ID     json.RawMessage
			Method string
			Params protocol.TurnSteerParams
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if request.Method != protocol.MethodTurnSteer || request.Params.ThreadID != "thread" || request.Params.ExpectedTurnID != "current" || request.Params.ClientUserMessageID == nil || *request.Params.ClientUserMessageID != id {
			t.Fatalf("steer order: %+v", request.Params)
		}
		if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":{\"turnId\":\"current\"}}\n", request.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(server, `{"method":"item/started","params":{"threadId":"thread","turnId":"current","item":{"type":"userMessage","id":%q,"clientId":%q,"content":[]}}}`+"\n", id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(server, `{"method":"item/agentMessage/delta","params":{"threadId":"thread","turnId":"current","itemId":%q,"delta":"after"}}`+"\n", "assistant-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"current","status":"completed","items":[],"error":null}}}`); err != nil {
		t.Fatal(err)
	}
	<-turn.done
	stop()
	if source.enabled != 1 || source.closed != 1 || len(source.accepted) != 2 || source.accepted[0] != "one" || source.accepted[1] != "two" {
		t.Fatalf("consumer lifecycle: %+v", source)
	}
	result, err := turn.result("")
	if err != nil || len(result.Output) != 5 || len(result.SteerInputIDs) != 2 {
		t.Fatalf("two identical inputs must stay distinct: %+v, %v", result, err)
	}
	roles := make([]sdk.MessageRole, len(result.Output))
	for i, message := range result.Output {
		roles[i] = message.Role
	}
	if !reflect.DeepEqual(roles, []sdk.MessageRole{sdk.MessageRoleAssistant, sdk.MessageRoleUser, sdk.MessageRoleAssistant, sdk.MessageRoleUser, sdk.MessageRoleAssistant}) {
		t.Fatalf("transcript order: %v", roles)
	}
}

func TestCodexSteerAcknowledgmentAloneDoesNotApplyInput(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	source := &testSteering{}
	turn := newTurnState(t.Context(), external.PromptInput{Steering: source, Sink: external.EventSinkFunc(func(event.StreamEvent) {})}, "thread", nil, nil, nil, nil, slog.Default())
	defer turn.close()
	turn.setTurnID("current")
	conn := newConn(client, &appServer{logger: slog.Default(), turns: map[string]*turnState{"thread": turn}}, slog.Default())
	defer func() { _ = conn.Close() }()
	finished := make(chan error, 1)
	go func() {
		finished <- submitSteer(t.Context(), conn, turn, external.SteerInput{ID: "input", Text: "adjust"})
	}()
	scanner := bufio.NewScanner(server)
	if !scanner.Scan() {
		t.Fatal("missing request")
	}
	var request struct{ ID json.RawMessage }
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	// Same connection ordering: accepted into the buffer, but the turn ends
	// without consuming the input. It must never appear as a delivered message.
	if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":{\"turnId\":\"current\"}}\n", request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"current","status":"completed","items":[],"error":null}}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("buffered input reported delivered")
		}
	case <-time.After(time.Second):
		t.Fatal("steer leaked past turn end")
	}
	result, err := turn.result("")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.accepted) != 0 || len(result.SteerInputIDs) != 0 || len(result.Output) != 0 {
		t.Fatalf("unconsumed input entered history: %+v", result)
	}
}

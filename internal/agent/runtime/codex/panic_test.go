package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func requireTurnFailedInternal(t *testing.T, turn *turnState) {
	t.Helper()
	select {
	case <-turn.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end after the panic")
	}
	if _, err := turn.result(); !external.IsPanic(err) {
		t.Fatalf("turn error = %v, want a recovered panic", err)
	}
}

// panickingHandler panics where the read loop hands an inbound message to the
// owning turn.
type panickingHandler struct{}

func (panickingHandler) HandleServerRequest(context.Context, *protocol.Inbound) {
	panic("request boom")
}
func (panickingHandler) HandleNotification(context.Context, *protocol.Inbound) { panic("note boom") }

// The read loop serves every thread on one app-server process. A panic in it
// closes the connection, so calls waiting on the process fail instead of
// waiting on a loop that is gone, and the failure carries the internal code.
func TestReadLoopPanicClosesTheConnectionWithAnInternalFailure(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	conn := newConn(client, panickingHandler{}, slog.New(slog.DiscardHandler))
	defer func() { _ = conn.Close() }()

	result := make(chan error, 1)
	go func() { result <- conn.Call(t.Context(), "thread/read", nil, nil) }()
	scanner := bufio.NewScanner(server)
	if !scanner.Scan() {
		t.Fatal("missing request")
	}
	if _, err := server.Write([]byte(`{"method":"thread/closed","params":{"threadId":"x"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrConnClosed) || !external.IsPanic(err) {
			t.Fatalf("Call() error = %v, want a closed connection from a recovered panic", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a pending call was left waiting after the read loop died")
	}
}

// A sink that panics ends only the turn whose events it receives.
func TestEventDeliveryPanicEndsTheTurnWithAnInternalFailure(t *testing.T) {
	sink := external.EventSinkFunc(func(event.StreamEvent) { panic("sink boom") })
	turn := newTurnState(t.Context(), external.PromptInput{Sink: sink}, "thread", nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	defer turn.close()
	turn.emit(event.StreamEvent{Type: event.TextDelta, Delta: "x"})
	requireTurnFailedInternal(t, turn)
}

// panickingApproval panics on first use, as a failing approval flow would.
type panickingApproval struct{ approval.FlowService }

// A panic while deciding an approval ends the turn and rejects the request, so
// the app-server is not left waiting on an answer.
func TestServerRequestPanicEndsTheTurnWithAnInternalFailure(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	turn := newTurnState(t.Context(), external.PromptInput{BotID: "bot"}, "thread", panickingApproval{}, nil, nil, nil, slog.New(slog.DiscardHandler))
	defer turn.close()
	conn := newConn(client, &appServer{logger: slog.New(slog.DiscardHandler), turns: map[string]*turnState{"thread": turn}}, slog.New(slog.DiscardHandler))
	defer func() { _ = conn.Close() }()

	command := "ls"
	go turn.handleServerRequest(conn, &protocol.Inbound{ID: protocol.NewRequestID(7)}, &protocol.CommandExecutionRequestApprovalParams{Command: &command, ItemID: "item"})

	scanner := bufio.NewScanner(server)
	if err := server.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() {
		t.Fatalf("the request was never answered: %v", scanner.Err())
	}
	requireTurnFailedInternal(t, turn)
}

// panickingSteering panics where the steering worker calls the run's input
// source.
type panickingSteering struct{ testSteering }

func (*panickingSteering) Enable(context.Context) error { panic("steer boom") }

func TestSteeringPanicEndsTheTurnWithAnInternalFailure(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	source := &panickingSteering{}
	turn := newTurnState(t.Context(), external.PromptInput{Steering: source, Sink: external.EventSinkFunc(func(event.StreamEvent) {})}, "thread", nil, nil, nil, nil, slog.New(slog.DiscardHandler))
	defer turn.close()
	conn := newConn(client, &appServer{logger: slog.New(slog.DiscardHandler), turns: map[string]*turnState{"thread": turn}}, slog.New(slog.DiscardHandler))
	defer func() { _ = conn.Close() }()
	stop := startSteering(t.Context(), conn, turn)
	defer stop()
	requireTurnFailedInternal(t, turn)
}

type panickingUserInput struct{ UserInputService }

func TestElicitationPanicEndsTheTurnWithAnInternalFailure(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	turn := newTurnState(t.Context(), external.PromptInput{BotID: "bot", CanRequestUserInput: true}, "thread", nil, nil, panickingUserInput{}, nil, slog.New(slog.DiscardHandler))
	defer turn.close()
	conn := newConn(client, &appServer{logger: slog.New(slog.DiscardHandler)}, slog.New(slog.DiscardHandler))
	defer func() { _ = conn.Close() }()

	var params protocol.McpServerElicitationRequestParams
	if err := json.Unmarshal([]byte(`{"mode":"form","message":"ok?","requestedSchema":{"type":"object","properties":{"a":{"type":"string"}}}}`), &params); err != nil {
		t.Fatal(err)
	}
	go turn.handleElicitation(conn, &protocol.Inbound{ID: protocol.NewRequestID(9)}, &params)

	scanner := bufio.NewScanner(server)
	if err := server.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() {
		t.Fatalf("the request was never answered: %v", scanner.Err())
	}
	requireTurnFailedInternal(t, turn)
}

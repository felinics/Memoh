package grpctransport

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

// terminalHandle is a scripted handle that sends run_terminal.
type terminalHandle struct{ scriptedHandle }

func (*terminalHandle) ReportsRunTerminal() bool { return true }

// The started frame carries whether the run ends with run_terminal, and the
// event reaches the client ahead of the run's error.
func TestRunTerminalCrossesTransport(t *testing.T) {
	payload, err := turn.NewRunTerminalEvent(turn.RunStateFailed, string(apperror.CodeAgentProviderOverloaded))
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan turn.Event, 1)
	errs := make(chan error, 1)
	events <- turn.Event{RunID: "run-scripted", Seq: 1, Kind: turn.EventRunTerminal, Payload: payload}
	errs <- errors.New("SECRET provider body")
	close(events)
	close(errs)
	client, cleanup := newTestClient(t, &scriptedService{handle: &terminalHandle{scriptedHandle{events: events, errs: errs}}}, "secret")
	defer cleanup()

	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if !turn.ReportsRunTerminal(handle) {
		t.Fatal("client handle does not report run_terminal")
	}
	var terminals []turn.RunTerminal
	var runErr error
	for events, errs := handle.Events(), handle.Errs(); events != nil || errs != nil; {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if terminal, ok := turn.RunTerminalFrom(event); ok {
				terminals = append(terminals, terminal)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			runErr = err
		}
	}
	want := turn.RunTerminal{Type: turn.EventRunTerminal, State: turn.RunStateFailed, ErrorCode: "agent.provider_overloaded"}
	if len(terminals) != 1 || terminals[0] != want {
		t.Fatalf("terminals = %+v, want %+v", terminals, want)
	}
	if runErr == nil {
		t.Fatal("run error was not delivered")
	}
}

// A handle that does not send run_terminal, like a server that predates it,
// leaves the flag false.
func TestRunTerminalFlagDefaultsToFalse(t *testing.T) {
	client, cleanup := newTestClient(t, &fakeService{}, "secret")
	defer cleanup()
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "team-1"})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	for event := range handle.Events() {
		_ = event
	}
	if turn.ReportsRunTerminal(handle) {
		t.Fatal("client handle reports run_terminal for a server that does not send it")
	}
}

package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
)

// turn/interrupt alone leaves unified-exec processes running, so a stop must
// also terminate the processes this turn's commands started — and only those.
func TestInterruptTurnTerminatesTheTurnsProcesses(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	turn := newTurnState(t.Context(), external.PromptInput{Sink: &captureSink{}}, "thread", nil, nil, nil, nil, slog.Default())
	defer turn.close()
	for _, raw := range []string{
		`{"type":"commandExecution","id":"call_a","command":"sleep 8","cwd":"/data","commandActions":[],"processId":"101","status":"inProgress"}`,
		`{"type":"commandExecution","id":"call_b","command":"true","cwd":"/data","commandActions":[],"processId":"102","status":"inProgress"}`,
		`{"type":"commandExecution","id":"call_a","command":"sleep 8","cwd":"/data","commandActions":[],"processId":"101","status":"inProgress"}`,
		`{"type":"commandExecution","id":"call_c","command":"echo","cwd":"/data","commandActions":[],"status":"inProgress"}`,
	} {
		var item protocol.ThreadItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			t.Fatal(err)
		}
		turn.handleItemStarted(&item)
	}
	owner := &appServer{logger: slog.Default(), turns: map[string]*turnState{"thread": turn}}
	owner.conn = newConn(client, owner, slog.Default())
	defer func() { _ = owner.conn.Close() }()

	done := make(chan struct{})
	go func() {
		(&Driver{logger: slog.Default()}).interruptTurn(owner, turn, "turn-1")
		close(done)
	}()

	type request struct {
		ID     json.RawMessage
		Method string
		Params map[string]any
	}
	var got []request
	scanner := bufio.NewScanner(server)
	for range 3 {
		if !scanner.Scan() {
			t.Fatalf("missing request after %+v", got)
		}
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatal(err)
		}
		got = append(got, req)
		result := `{}`
		if req.Method == protocol.MethodThreadBackgroundTerminalsTerminate {
			result = `{"terminated":true}`
		}
		if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":%s}\n", req.ID, result); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interruptTurn did not return")
	}

	if got[0].Method != protocol.MethodTurnInterrupt || got[0].Params["turnId"] != "turn-1" {
		t.Fatalf("first request = %+v, want turn/interrupt", got[0])
	}
	for i, want := range []string{"101", "102"} {
		req := got[i+1]
		if req.Method != protocol.MethodThreadBackgroundTerminalsTerminate ||
			req.Params["threadId"] != "thread" || req.Params["processId"] != want {
			t.Fatalf("request %d = %+v, want terminate %s", i+1, req, want)
		}
	}
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

// collectTurn drains a turn handle and returns its event payloads, the
// run_terminal events among them, and the turn errors.
func collectTurn(t *testing.T, handle turn.RunHandle) ([]string, []turn.RunTerminal, []error) {
	t.Helper()
	var (
		payloads  []string
		terminals []turn.RunTerminal
		errs      []error
	)
	timeout := time.After(5 * time.Second)
	for events, errCh := handle.Events(), handle.Errs(); events != nil || errCh != nil; {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			payloads = append(payloads, string(event.Payload))
			if terminal, ok := turn.RunTerminalFrom(event); ok {
				terminals = append(terminals, terminal)
				if i := len(payloads); i > 0 && event.Seq != int64(i) {
					t.Fatalf("run_terminal seq = %d, want %d", event.Seq, i)
				}
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			errs = append(errs, err)
		case <-timeout:
			t.Fatal("turn did not end")
		}
	}
	return payloads, terminals, errs
}

// A chat turn against a real runtime manager ends with run_terminal naming
// the state and code the run's record carries.
func TestChatTurnEndsWithRunTerminalFromLedger(t *testing.T) {
	tests := []struct {
		name     string
		streamer testChatStreamer
		cancel   bool
		want     turn.RunTerminal
		ledger   [2]string
	}{
		{
			name:     "failed with a coded error",
			streamer: &errRunner{chunks: []string{`{"type":"text_delta","delta":"partial"}`}, err: apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("SECRET"), nil)},
			want:     turn.RunTerminal{Type: turn.EventRunTerminal, State: turn.RunStateFailed, ErrorCode: "agent.provider_overloaded"},
			ledger:   [2]string{"failed", "agent.provider_overloaded"},
		},
		{
			name:     "failed with an uncoded error",
			streamer: &errRunner{err: errors.New("SECRET provider exploded")},
			want:     turn.RunTerminal{Type: turn.EventRunTerminal, State: turn.RunStateFailed, ErrorCode: "runtime_run_failed"},
			ledger:   [2]string{"failed", "runtime_run_failed"},
		},
		{
			name:     "completed",
			streamer: &fakeRunner{chunks: []string{`{"type":"agent_start"}`, `{"type":"agent_end","messages":[]}`}},
			want:     turn.RunTerminal{Type: turn.EventRunTerminal, State: turn.RunStateCompleted},
			ledger:   [2]string{"completed", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := sessionledger.New()
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
				OwnerID: "owner-run-terminal", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
			})
			t.Cleanup(func() { _ = manager.Close() })
			service := newTurnTestService(tt.streamer)
			service.SetSessionRuntime(manager)

			cmd := turn.StartTurnCommand{TeamID: "team-1", Mode: turn.ModeChat, BotID: "bot-1", ThreadID: "thread-1", IdempotencyKey: "msg-1"}
			handle, err := service.StartTurn(context.Background(), cmd)
			if err != nil {
				t.Fatalf("start turn: %v", err)
			}
			if !turn.ReportsRunTerminal(handle) {
				t.Fatal("handle does not report run_terminal")
			}
			payloads, terminals, _ := collectTurn(t, handle)
			if len(terminals) != 1 || terminals[0] != tt.want {
				t.Fatalf("run_terminal = %+v, want %+v (events %q)", terminals, tt.want, payloads)
			}
			if last := payloads[len(payloads)-1]; !json.Valid([]byte(last)) || turn.EventRunTerminal != parseKind(json.RawMessage(last)) {
				t.Fatalf("last event = %s, want run_terminal", last)
			}
			run, err := runs.Get(context.Background(), handle.RunID())
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if got := [2]string{string(run.State), run.ErrorCode}; got != tt.ledger {
				t.Fatalf("session_runs = %q, want %q", got, tt.ledger)
			}
		})
	}
}

// A run whose terminal write was refused, because its owner lost the run or
// the write failed, ends without run_terminal.
func TestChatTurnWithoutTerminalRecordSendsNoRunTerminal(t *testing.T) {
	for name, finishErr := range map[string]error{
		"ownership lost": sessionruntime.ErrRunOwnershipLost,
		"write failed":   errors.New("ledger down"),
	} {
		t.Run(name, func(t *testing.T) {
			service, admitter := newAdmittedTurnTestService(&errRunner{err: errors.New("provider exploded")})
			admitter.finishErr = finishErr
			handle, err := service.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "m"})
			if err != nil {
				t.Fatal(err)
			}
			_, terminals, errs := collectTurn(t, handle)
			if len(terminals) != 0 {
				t.Fatalf("run_terminal = %+v, want none", terminals)
			}
			if len(errs) != 1 {
				t.Fatalf("turn errors = %v, want the run error", errs)
			}
		})
	}
}

// The terminal record the finish write returns is what run_terminal carries.
func TestChatTurnRunTerminalCarriesFinishRecord(t *testing.T) {
	service, admitter := newAdmittedTurnTestService(&fakeRunner{chunks: []string{`{"type":"agent_end","messages":[]}`}})
	admitter.terminal = sessionruntime.TerminalRun{RunID: "run-1", State: "lost", ErrorCode: "runtime_owner_lease_expired"}
	handle, err := service.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "m"})
	if err != nil {
		t.Fatal(err)
	}
	_, terminals, _ := collectTurn(t, handle)
	want := turn.RunTerminal{Type: turn.EventRunTerminal, State: "lost", ErrorCode: "runtime_owner_lease_expired"}
	if len(terminals) != 1 || terminals[0] != want {
		t.Fatalf("run_terminal = %+v, want %+v", terminals, want)
	}
}

// A consumer that stopped reading does not hold the pump open forever.
func TestRunTerminalSendGivesUpOnStalledConsumer(t *testing.T) {
	h := &runHandle{id: "run-1", events: make(chan turn.Event)}
	h.terminal = sessionruntime.TerminalRun{RunID: "run-1", State: "completed"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sendTerminal("t", "s", 1)
	}()
	select {
	case <-done:
	case <-time.After(terminalEventTimeout + 5*time.Second):
		t.Fatal("sendTerminal blocked past its timeout")
	}
}

// A discuss turn ends with run_terminal too.
func TestDiscussTurnEndsWithRunTerminal(t *testing.T) {
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventAgentEnd, Messages: json.RawMessage(`[{"role":"assistant","content":"done"}]`)},
	)
	if last := got.events[len(got.events)-1]; last != `{"type":"run_terminal","state":"completed"}` {
		t.Fatalf("last discuss event = %s", last)
	}
}

// A consumer that canceled the run does not get run_terminal, and the pump
// does not wait for it to read one.
func TestCanceledChatTurnSendsNoRunTerminal(t *testing.T) {
	service, admitter := newAdmittedTurnTestService(&fakeRunner{chunks: []string{`{"type":"done"}`}, block: make(chan struct{})})
	admitter.terminal = sessionruntime.TerminalRun{RunID: "run-1", State: "aborted"}
	handle, err := service.StartTurn(context.Background(), turn.StartTurnCommand{TeamID: "t", Mode: turn.ModeChat, BotID: "b", ThreadID: "s", IdempotencyKey: "m"})
	if err != nil {
		t.Fatal(err)
	}
	handle.Cancel()
	start := time.Now()
	_, terminals, _ := collectTurn(t, handle)
	if len(terminals) != 0 {
		t.Fatalf("run_terminal = %+v, want none after the consumer canceled", terminals)
	}
	if elapsed := time.Since(start); elapsed >= terminalEventTimeout {
		t.Fatalf("pump took %s to close", elapsed)
	}
}

// A discuss turn whose consumer canceled it ends without run_terminal.
func TestCanceledDiscussTurnSendsNoRunTerminal(t *testing.T) {
	runs := sessionledger.New()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID: "owner-discuss-cancel", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	service := newDiscussTestService(&fakeRunner{}, blockingNativeStreamer{}, &fakeDiscussService{})
	service.SetSessionRuntime(manager)
	handle, err := service.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	handle.Cancel()
	start := time.Now()
	_, terminals, _ := collectTurn(t, handle)
	if len(terminals) != 0 {
		t.Fatalf("run_terminal = %+v, want none after the consumer canceled", terminals)
	}
	if elapsed := time.Since(start); elapsed >= terminalEventTimeout {
		t.Fatalf("pump took %s to close", elapsed)
	}
}

// blockingNativeStreamer streams nothing until the run is canceled.
type blockingNativeStreamer struct{}

func (blockingNativeStreamer) Stream(ctx context.Context, _ native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

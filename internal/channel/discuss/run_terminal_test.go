package discuss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/logger"
)

type recordingBroadcaster struct{ events []channel.StreamEvent }

func (b *recordingBroadcaster) PublishEvent(_ string, event channel.StreamEvent) {
	b.events = append(b.events, event)
}

func (b *recordingBroadcaster) errors() []channel.StreamEvent {
	var out []channel.StreamEvent
	for _, event := range b.events {
		if event.Type == channel.StreamEventError {
			out = append(out, event)
		}
	}
	return out
}

// scriptedDiscussService replays events on a handle; reportsTerminal decides
// whether the handle says it sends run_terminal.
type scriptedDiscussService struct {
	*fakeTurnService
	reportsTerminal bool
	events          []turn.Event
	tailErr         error
}

type scriptedDiscussHandle struct {
	fakeRunHandle
	reportsTerminal bool
}

func (h *scriptedDiscussHandle) ReportsRunTerminal() bool { return h.reportsTerminal }

func (s scriptedDiscussService) StartTurn(context.Context, turn.StartTurnCommand) (turn.RunHandle, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	h := &scriptedDiscussHandle{
		fakeRunHandle:   fakeRunHandle{events: make(chan turn.Event, len(s.events)), errs: make(chan error, 1)},
		reportsTerminal: s.reportsTerminal,
	}
	for _, event := range s.events {
		h.events <- event
	}
	if s.tailErr != nil {
		h.errs <- s.tailErr
	}
	close(h.events)
	close(h.errs)
	return h, nil
}

func discussEvents(t *testing.T, items ...any) []turn.Event {
	t.Helper()
	resolved, _ := json.Marshal(turn.DiscussRunResolvedPayload{RuntimeType: "native"})
	out := []turn.Event{{Seq: 1, Kind: turn.DiscussEventRunResolved, Payload: resolved}}
	for _, item := range items {
		var event turn.Event
		switch v := item.(type) {
		case agentevent.StreamEvent:
			payload, _ := json.Marshal(v)
			event = turn.Event{Kind: string(v.Type), Payload: payload}
		case turn.RunTerminal:
			payload, err := turn.NewRunTerminalEvent(v.State, v.ErrorCode)
			if err != nil {
				t.Fatal(err)
			}
			event = turn.Event{Kind: turn.EventRunTerminal, Payload: payload}
		default:
			t.Fatalf("unsupported item %T", item)
		}
		event.Seq = int64(len(out) + 1)
		out = append(out, event)
	}
	return out
}

func runScriptedDiscuss(t *testing.T, service turn.Service) (discussRunOutcome, *recordingBroadcaster) {
	t.Helper()
	outcome, broadcaster, _ := runScriptedDiscussLogged(t, service)
	return outcome, broadcaster
}

// runScriptedDiscussLogged also returns the turn's "discuss turn" result line.
func runScriptedDiscussLogged(t *testing.T, service turn.Service) (discussRunOutcome, *recordingBroadcaster, map[string]any) {
	t.Helper()
	broadcaster := &recordingBroadcaster{}
	runner := discussTurnRunner{projector: newDiscussEventProjector(broadcaster)}
	var logs bytes.Buffer
	outcome, _ := runner.Run(context.Background(), service, turn.StartTurnCommand{BotID: "bot-1"}, logger.New(&logs, "debug", "json"))
	var line map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(raw, &record) == nil && record["msg"] == "discuss turn" {
			line = record
		}
	}
	if line == nil {
		t.Fatalf("no discuss turn line in %s", logs.String())
	}
	return outcome, broadcaster, line
}

func assertOneFailure(t *testing.T, b *recordingBroadcaster, wantCode, wantText string) {
	t.Helper()
	failures := b.errors()
	if len(failures) != 1 || failures[0].ErrorCode != wantCode || failures[0].Error != wantText {
		t.Fatalf("error broadcasts = %+v, want one %s %q", failures, wantCode, wantText)
	}
}

const (
	discussOverloadedCopy = "The model provider is overloaded right now. Please try again in a moment."
	discussRunFailedCopy  = "The response could not be completed. Please try again."
)

// With run_terminal, a failed run is broadcast once with the copy for its
// code, and none of the error events the run streamed are broadcast.
func TestDiscussFailureBroadcastComesFromRunTerminal(t *testing.T) {
	outcome, b := runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.Error, Code: "agent.provider_rate_limited", Error: "SECRET first"},
			agentevent.StreamEvent{Type: agentevent.Error, Code: "agent.provider_overloaded", Error: "SECRET second"},
			agentevent.StreamEvent{Type: agentevent.AgentAbort},
			turn.RunTerminal{State: turn.RunStateFailed, ErrorCode: "agent.provider_overloaded"},
		),
	})
	assertOneFailure(t, b, "agent.provider_overloaded", discussOverloadedCopy)
	if !outcome.failed || outcome.endedClean {
		t.Fatalf("outcome = %+v, want a failed run", outcome)
	}
}

// The result line of a run that failed names the run's code and the fault
// the catalog declares for it, as the IM result line does, not the text of
// the error events it streamed.
func TestDiscussResultLineNamesTheRunCode(t *testing.T) {
	_, _, line := runScriptedDiscussLogged(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.Error, Code: "agent.provider_overloaded", Error: discussOverloadedCopy},
			agentevent.StreamEvent{Type: agentevent.AgentAbort},
			turn.RunTerminal{State: turn.RunStateFailed, ErrorCode: "agent.provider_overloaded"},
		),
	})
	want := errs.Analyze(context.Background(), apperror.New(apperror.CodeAgentProviderOverloaded, nil)).Fault
	if line["reason"] != "agent.provider_overloaded" || line["fault"] != string(want) {
		t.Fatalf("discuss turn line = %v, want reason agent.provider_overloaded fault %s", line, want)
	}
	if line["error"] == discussOverloadedCopy {
		t.Fatalf("discuss turn line error = %q, want the run's code, not the event text", line["error"])
	}
}

// A failure the turn port reports on Errs is broadcast once, by the run's
// code, never with the error's text.
func TestDiscussTurnErrorBroadcastOnce(t *testing.T) {
	_, b := runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events:  discussEvents(t, turn.RunTerminal{State: turn.RunStateFailed, ErrorCode: "agent.response_interrupted"}),
		tailErr: errors.New("SECRET persist failed"),
	})
	assertOneFailure(t, b, "agent.response_interrupted", "The model response was interrupted. Please try again.")

	// A run that completed but still reported an error is answered by the
	// error's code, which an uncoded error does not have.
	_, b = runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.AgentEnd},
			turn.RunTerminal{State: turn.RunStateCompleted},
		),
		tailErr: errors.New("SECRET commit failed"),
	})
	assertOneFailure(t, b, "runtime_run_failed", discussRunFailedCopy)
}

// A recovered retry is not broadcast as an error.
func TestDiscussRecoveredRetryIsNotBroadcast(t *testing.T) {
	outcome, b := runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.Error, Code: "agent.provider_overloaded", Error: "overloaded"},
			agentevent.StreamEvent{Type: agentevent.AgentEnd},
			turn.RunTerminal{State: turn.RunStateCompleted},
		),
	})
	if failures := b.errors(); len(failures) != 0 {
		t.Fatalf("error broadcasts = %+v, want none", failures)
	}
	if !outcome.endedClean {
		t.Fatalf("outcome = %+v, want a clean end", outcome)
	}
}

// When the handle reports run_terminal but the run ends without one (its owner
// lost the run), the held error events are broadcast as they arrived.
func TestDiscussHeldErrorsReleasedWithoutRunTerminal(t *testing.T) {
	_, b := runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{}, reportsTerminal: true,
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.Error, Code: "agent.provider_overloaded", Error: discussOverloadedCopy},
			agentevent.StreamEvent{Type: agentevent.AgentAbort},
		),
	})
	assertOneFailure(t, b, "", discussOverloadedCopy)
	if last := b.events[len(b.events)-1]; last.Type != channel.StreamEventError {
		t.Fatalf("last broadcast = %s, want the released error after agent_end", last.Type)
	}
}

// A handle without run_terminal keeps broadcasting error events as they
// arrive, even when a run_terminal event appears.
func TestDiscussLegacyHandleBroadcastsStreamErrors(t *testing.T) {
	_, b := runScriptedDiscuss(t, scriptedDiscussService{
		fakeTurnService: &fakeTurnService{},
		events: discussEvents(t,
			agentevent.StreamEvent{Type: agentevent.Error, Error: "provider rejected the request"},
			agentevent.StreamEvent{Type: agentevent.AgentAbort},
			turn.RunTerminal{State: turn.RunStateFailed, ErrorCode: "agent.provider_overloaded"},
		),
	})
	assertOneFailure(t, b, "", "provider rejected the request")
}

// A start failure is broadcast once by its code; an uncoded one gets the
// runtime_run_failed copy. Admission answers are not failures.
func TestDiscussStartFailureBroadcast(t *testing.T) {
	_, b := runScriptedDiscuss(t, scriptedDiscussService{fakeTurnService: &fakeTurnService{startErr: apperror.Wrap(apperror.CodeAgentProviderOverloaded, errors.New("SECRET"), nil)}})
	assertOneFailure(t, b, "agent.provider_overloaded", discussOverloadedCopy)

	_, b = runScriptedDiscuss(t, scriptedDiscussService{fakeTurnService: &fakeTurnService{startErr: errors.New("SECRET resolve failed")}})
	assertOneFailure(t, b, "runtime_run_failed", discussRunFailedCopy)

	for _, answer := range []error{turn.ErrSessionBusy, turn.ErrDuplicateTurn, turn.ErrTurnDeferred} {
		_, b = runScriptedDiscuss(t, scriptedDiscussService{fakeTurnService: &fakeTurnService{startErr: fmt.Errorf("%w: x", answer)}})
		if failures := b.errors(); len(failures) != 0 {
			t.Fatalf("%v: error broadcasts = %+v, want none", answer, failures)
		}
	}
}

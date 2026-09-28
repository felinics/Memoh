package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
)

// terminalTurnRun is a handle that ends its runs with run_terminal.
type terminalTurnRun struct{ fakeTurnRun }

func (*terminalTurnRun) ReportsRunTerminal() bool { return true }

// terminalFailureGateway replays payloads on a handle that reports
// run_terminal. A payload is a run_terminal event when its type says so.
type terminalFailureGateway struct {
	scriptedFailureGateway
	// legacy returns a handle that does not report run_terminal.
	legacy bool
}

func (f *terminalFailureGateway) StartTurn(ctx context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	handle, err := f.scriptedFailureGateway.StartTurn(ctx, cmd)
	if err != nil {
		return nil, err
	}
	run := handle.(*fakeTurnRun)
	events := make(chan turn.Event, len(f.payloads))
	for event := range run.events {
		event.Kind = parseTestKind(event.Payload)
		events <- event
	}
	close(events)
	if f.legacy {
		return &fakeTurnRun{events: events, errs: run.errs}, nil
	}
	return &terminalTurnRun{fakeTurnRun{events: events, errs: run.errs}}, nil
}

func parseTestKind(payload json.RawMessage) string {
	var env struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &env)
	return env.Type
}

func runIMTerminal(t *testing.T, gateway *terminalFailureGateway) (imFailureResult, []channel.StreamEvent) {
	t.Helper()
	processor, sender, cfg, msg := newIMFailureProcessor(gateway)
	result := imFailureResult{err: processor.HandleInbound(context.Background(), cfg, msg, sender)}
	var failures []channel.StreamEvent
	for _, event := range sender.events {
		if event.Type == channel.StreamEventError {
			result.errorTexts = append(result.errorTexts, event.Error)
			failures = append(failures, event)
		}
	}
	for _, sent := range sender.sent {
		result.sent = append(result.sent, sent.Message.PlainText())
	}
	return result, failures
}

const (
	overloadedCopy  = "The model provider is overloaded right now. Please try again in a moment."
	runFailedCopy   = "The response could not be completed. Please try again."
	interruptedCopy = "The model response was interrupted. Please try again."
)

// The run's code decides the one failure reply. The error events the run
// streamed are not shown, and neither is the event's own text.
func TestIMFailureReplyComesFromRunTerminal(t *testing.T) {
	t.Parallel()
	got, failures := runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{payloads: []string{
		`{"type":"agent_start"}`,
		`{"type":"error","code":"agent.provider_rate_limited","error":"The model provider rate limit was reached. Please wait a moment before sending again."}`,
		`{"type":"retry","attempt":1,"maxAttempt":3,"retryError":"api error 503"}`,
		`{"type":"error","code":"agent.provider_overloaded","error":"SECRET upstream text"}`,
		`{"type":"agent_abort","messages":[]}`,
		`{"type":"run_terminal","state":"failed","error_code":"agent.provider_overloaded"}`,
	}}})
	assertIMFailure(t, got, "", false, overloadedCopy)
	if failures[0].ErrorCode != "agent.provider_overloaded" {
		t.Fatalf("failure reply code = %q", failures[0].ErrorCode)
	}
}

// A retry the runtime recovered from ends completed; its error event is not
// shown.
func TestIMRecoveredRetryShowsNoError(t *testing.T) {
	t.Parallel()
	got, _ := runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{payloads: []string{
		`{"type":"error","code":"agent.provider_overloaded","error":"The model provider is overloaded right now."}`,
		`{"type":"text_delta","delta":"answer"}`,
		`{"type":"agent_end","messages":[]}`,
		`{"type":"run_terminal","state":"completed"}`,
	}}})
	assertIMFailure(t, got, "", false)
}

// A turn error does not reach the reply text: the reply is the copy for the
// run's code, and the turn error is still returned.
func TestIMTurnErrorRepliesWithRunCode(t *testing.T) {
	t.Parallel()
	got, _ := runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{
		payloads: []string{
			`{"type":"text_delta","delta":"partial"}`,
			`{"type":"run_terminal","state":"failed","error_code":"runtime_run_failed"}`,
		},
		tailErr: errors.New("SECRET provider exploded"),
	}})
	assertIMFailure(t, got, "", true, runFailedCopy)

	got, _ = runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{
		payloads: []string{`{"type":"run_terminal","state":"failed","error_code":"agent.response_interrupted"}`},
		tailErr:  apperror.Wrap(apperror.CodeAgentResponseInterrupted, errors.New("SECRET cause"), nil),
	}})
	assertIMFailure(t, got, "agent.response_interrupted", true, interruptedCopy)
}

// A code the channel has no copy for falls back to runtime_run_failed copy
// rather than the bare code or any error text.
func TestIMUnknownRunCodeUsesRunFailedCopy(t *testing.T) {
	t.Parallel()
	got, _ := runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{payloads: []string{
		`{"type":"error","error":"SECRET raw"}`,
		`{"type":"run_terminal","state":"lost","error_code":"not.in_catalog"}`,
	}}})
	assertIMFailure(t, got, "", false, runFailedCopy)
}

// An External Agent failure is answered once, in the stream, with the run's
// copy; no separate reply repeats it. A handle that does not report
// run_terminal answers it once too.
func TestIMExternalAgentFailureWithRunTerminal(t *testing.T) {
	t.Parallel()
	cause := apperror.New(apperror.CodeACPAgentNotConfigured, nil)
	for _, legacy := range []bool{false, true} {
		got, failures := runIMTerminal(t, &terminalFailureGateway{legacy: legacy, scriptedFailureGateway: scriptedFailureGateway{
			payloads: []string{`{"type":"run_terminal","state":"failed","error_code":"acp_agent_not_configured"}`},
			tailErr:  cause,
		}})
		if got.err == nil || len(failures) != 1 || failures[0].ErrorCode != "acp_agent_not_configured" {
			t.Fatalf("legacy=%v: err = %v, failures = %+v", legacy, got.err, failures)
		}
		if len(got.sent) != 0 {
			t.Fatalf("legacy=%v: sent = %q, want no reply besides the stream copy %q", legacy, got.sent, failures[0].Error)
		}
	}
}

// Without run_terminal, a handle that reports it releases the held error
// events unchanged.
func TestIMHeldErrorsReleasedWithoutRunTerminal(t *testing.T) {
	t.Parallel()
	got, failures := runIMTerminal(t, &terminalFailureGateway{scriptedFailureGateway: scriptedFailureGateway{payloads: []string{
		`{"type":"error","code":"agent.provider_overloaded","error":"` + overloadedCopy + `"}`,
		`{"type":"agent_abort","messages":[]}`,
	}}})
	assertIMFailure(t, got, "", false, overloadedCopy)
	if failures[0].ErrorCode != "" {
		t.Fatalf("released event code = %q, want the event unchanged", failures[0].ErrorCode)
	}
}

// A handle that does not report run_terminal keeps today's replies even when
// the event appears.
func TestIMLegacyHandleIgnoresRunTerminal(t *testing.T) {
	t.Parallel()
	got, _ := runIMTerminal(t, &terminalFailureGateway{legacy: true, scriptedFailureGateway: scriptedFailureGateway{payloads: []string{
		`{"type":"error","error":"SECRET raw"}`,
		`{"type":"agent_abort","messages":[]}`,
		`{"type":"run_terminal","state":"failed","error_code":"agent.provider_overloaded"}`,
	}}})
	assertIMFailure(t, got, "", false, "SECRET raw")
}

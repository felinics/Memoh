package handlers

// Characterization tests for what the local WebSocket channel reports when an
// Agent run fails. They pin CURRENT behavior so the failure-classification
// refactor can show exactly which outward values it changes. A value that looks
// wrong is still asserted as it is today and marked "current behavior".
//
// Each case composes the handler's own pieces with a real session runtime
// manager backed by the in-memory ledger: forwardWSStreamEvents publishes the
// run's events and writes the initiating socket's error frame, finishWSRun
// writes the terminal state, and sendWSRunFailure is the frame a runner error
// produces after the run has finished.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

const (
	failureCharBotID     = "11111111-1111-4111-8111-111111111111"
	failureCharSessionID = "22222222-2222-4222-8222-222222222222"
)

type failureCharFence struct{}

func (failureCharFence) Activate(context.Context, string, string, int64) error { return nil }

type failureCharRun struct {
	manager   *sessionruntime.Manager
	runs      *sessionledger.Store
	handler   *LocalChannelHandler
	admission sessionruntime.Admission
	writer    *wsWriter
	ref       wsTurnRef
}

func newFailureCharRun(t *testing.T) failureCharRun {
	t.Helper()
	runs := sessionledger.New()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-ws-failure",
		OwnerLeaseTTL: time.Minute,
		Ledger:        runs,
		Fence:         failureCharFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	admission, err := manager.Admit(context.Background(), sessionruntime.AdmitInput{
		BotID:        failureCharBotID,
		SessionID:    failureCharSessionID,
		InvocationID: "invocation-ws-failure",
		Payload:      []byte(`{"text":"hi"}`),
		Execution: sessionruntime.Execution{
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
		},
	})
	if err != nil || !admission.Started {
		t.Fatalf("admit = %+v, %v", admission, err)
	}
	return failureCharRun{
		manager:   manager,
		runs:      runs,
		handler:   &LocalChannelHandler{logger: slog.New(slog.DiscardHandler), sessionRuntime: manager},
		admission: admission,
		writer:    &wsWriter{ch: make(chan []byte, 16), stop: make(chan struct{}), done: make(chan struct{})},
		ref:       wsTurnRef{RunID: admission.RunID, SessionID: failureCharSessionID},
	}
}

// forward runs the given native events through the handler exactly as the
// runner's event channel would deliver them.
func (r failureCharRun) forward(t *testing.T, events ...native.StreamEvent) {
	t.Helper()
	ch := make(chan application.WSStreamEvent, len(events))
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		ch <- data
	}
	close(ch)
	ctx := context.Background()
	r.handler.forwardWSStreamEvents(ctx, ctx, r.writer, failureCharBotID, r.ref, r.admission.Handle, ch)
}

func (r failureCharRun) finish(runErr error) {
	r.handler.finishWSRun(context.Background(), wsRunAdmission{RunID: r.admission.RunID, Handle: r.admission.Handle}, wsRunOutcome(application.RunOutcome{}, runErr))
}

// sendRunFailure finishes the run with the error its runner returned and
// sends the frame startWSStream sends for it.
func (r failureCharRun) sendRunFailure(runErr error) {
	outcome := wsRunOutcome(application.RunOutcome{}, runErr)
	runCode := r.handler.finishWSRun(context.Background(), wsRunAdmission{RunID: r.admission.RunID, Handle: r.admission.Handle}, outcome)
	sendWSRunFailure(context.Background(), r.writer, r.ref, runErr, outcome, runCode)
}

// frames drains every frame written to the initiating socket.
func (r failureCharRun) frames(t *testing.T) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for {
		select {
		case data := <-r.writer.ch:
			var frame map[string]any
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			frames = append(frames, frame)
		default:
			return frames
		}
	}
}

func (r failureCharRun) ledgerColumns(t *testing.T) [3]string {
	t.Helper()
	run, err := r.runs.Get(context.Background(), r.admission.RunID)
	if err != nil {
		t.Fatalf("load ledger run: %v", err)
	}
	return [3]string{string(run.State), run.ErrorCode, run.ErrorMessage}
}

func (r failureCharRun) runView(t *testing.T) [2]string {
	t.Helper()
	snapshot, err := r.manager.Snapshot(context.Background(), failureCharBotID, failureCharSessionID)
	if err != nil || snapshot.CurrentRunView == nil {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	view := snapshot.CurrentRunView
	return [2]string{view.Status, view.ErrorCode}
}

func assertFrames(t *testing.T, got []map[string]any, want []map[string]any) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("socket frames = %s\nwant %s", gotJSON, wantJSON)
	}
}

const (
	providerOverloadedDetail = "The model provider is unavailable or overloaded right now. Please try again in a moment."
	responseInterruptedCopy  = "The model response was interrupted. Please try again."
	runFailedDetail          = "The response could not be completed. Please try again."
)

// Scenarios 2 and 3 on the WebSocket path: the application layer forwards the
// public EventError (the failure already translated to its code), the native
// runtime ends with agent_abort, and the WS loop returns nil for a mid-stream
// failure, so finishWSRun is called without an error.
func TestCharacterizeWSMidStreamProviderFailure(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.forward(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3},
		native.StreamEvent{Type: native.EventError, Code: "agent.provider_overloaded", Error: providerOverloadedDetail},
		native.StreamEvent{Type: native.EventAgentAbort},
	)
	r.finish(nil)

	assertFrames(t, r.frames(t), []map[string]any{{
		"type": "error", "run_id": r.admission.RunID, "session_id": failureCharSessionID,
		"code": "agent.provider_overloaded", "message": providerOverloadedDetail,
	}})
	if got, want := r.ledgerColumns(t), [3]string{"failed", "agent.provider_overloaded", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "agent.provider_overloaded"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 3: a failed attempt that recovers leaves no error anywhere.
func TestCharacterizeWSRetryRecoveredRun(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.forward(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3},
		native.StreamEvent{Type: native.EventTextDelta, Delta: "done"},
		native.StreamEvent{Type: native.EventAgentEnd},
	)
	r.finish(nil)

	assertFrames(t, r.frames(t), nil)
	if got, want := r.ledgerColumns(t), [3]string{"completed", "", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"completed", ""}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 1 and 10 on the WebSocket path: the runner fails before the agent
// stream starts with an error that carries no apperror code.
//
// The socket frame carries runtime_run_failed, the code session_runs records,
// with its catalog detail instead of the error text.
func TestCharacterizeWSPlainRunnerError_CurrentBehavior(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	runErr := errors.New("resolve: model not found")
	r.sendRunFailure(runErr)

	assertFrames(t, r.frames(t), []map[string]any{{
		"type": "error", "run_id": r.admission.RunID, "session_id": failureCharSessionID,
		"code": "runtime_run_failed", "message": runFailedDetail,
	}})
	if got, want := r.ledgerColumns(t), [3]string{"failed", "runtime_run_failed", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "runtime_run_failed"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 1 and 10 with a catalogued runner error (X1).
//
// The socket frame carries the code at the top level with the catalog detail.
// session_runs records the runner's code in error_code and no message; the run
// view carries the same code and no error text.
func TestCharacterizeWSCodedRunnerError(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	runErr := apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("dial unix: no such file"), nil)
	r.sendRunFailure(runErr)

	assertFrames(t, r.frames(t), []map[string]any{{
		"type": "error", "run_id": r.admission.RunID, "session_id": failureCharSessionID,
		"code": "workspace.unreachable", "message": "The workspace could not be reached.",
	}})
	if got, want := r.ledgerColumns(t), [3]string{"failed", "workspace.unreachable", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "workspace.unreachable"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 6: an External Agent configuration failure (X6). The runner returns
// the External Agent error the application translated the driver's error into
// before any round ran.
//
// Current behavior: session_runs and the run view record its code.
func TestCharacterizeWSExternalAgentRunnerError(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.finish(apperror.New(apperror.CodeACPAgentNotConfigured, nil))

	if got, want := r.ledgerColumns(t), [3]string{"failed", "acp_agent_not_configured", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "acp_agent_not_configured"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 10: an uncoded native error text that reached the socket without
// classification (the handler does not classify; the application layer does).
//
// The frame, session_runs and the run view all name it runtime_run_failed, and
// the text goes nowhere.
func TestCharacterizeWSUncodedStreamError_CurrentBehavior(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.forward(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventError, Error: "runtime interrupted"},
		native.StreamEvent{Type: native.EventAgentAbort},
	)
	r.finish(nil)

	assertFrames(t, r.frames(t), []map[string]any{{
		"type": "error", "run_id": r.admission.RunID, "session_id": failureCharSessionID,
		"code": "runtime_run_failed", "message": runFailedDetail,
	}})
	if got, want := r.ledgerColumns(t), [3]string{"failed", "runtime_run_failed", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "runtime_run_failed"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 8 on the WebSocket path: the runner returns a bare cancellation,
// which finishWSRun leaves unnamed; with an abort recorded on the live run the
// outcome is aborted and nothing is sent to the socket.
func TestCharacterizeWSCanceledRunnerAfterAbort(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.forward(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventAgentAbort},
	)
	r.finish(context.Canceled)

	assertFrames(t, r.frames(t), nil)
	if got, want := r.ledgerColumns(t), [3]string{"aborted", "", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"aborted", ""}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

// Scenario 5 on the WebSocket path: the admission sentinels become run_rejected
// with a code and its catalog detail; no run is named. Any other admission
// failure is an error frame with the code for its fault.
func TestCharacterizeWSAdmissionRejectionFrames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err  error
		want map[string]any
	}{
		{
			err: sessionruntime.ErrSessionBusy,
			want: map[string]any{
				"type": "run_rejected", "invocation_id": "invocation-1", "session_id": failureCharSessionID,
				"code": "session_runtime.session_busy", "message": "This conversation is still working on the previous message. Please try again shortly.",
			},
		},
		{
			// Ownership loss during admission is not a rejection sentinel. It is
			// this process's failure, so the client gets internal.
			err: sessionruntime.ErrRunOwnershipLost,
			want: map[string]any{
				"type": "error", "invocation_id": "invocation-1", "session_id": failureCharSessionID,
				"code": "internal", "message": "Something went wrong on the server. Please try again.",
			},
		},
	} {
		writer := &wsWriter{ch: make(chan []byte, 1), stop: make(chan struct{}), done: make(chan struct{})}
		_ = sendWSErrorFromError(context.Background(), writer, wsTurn("invocation-1", failureCharSessionID), tc.err)
		var frame map[string]any
		if err := json.Unmarshal(<-writer.ch, &frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		assertFrames(t, []map[string]any{frame}, []map[string]any{tc.want})
	}
}

// SSE exit: the local channel's SSE stream carries the IM processor's error
// event (see the inbound characterization tests for its text).
//
// Current behavior: the SSE error event has only the text, no code.
func TestCharacterizeSSEErrorEventHasNoCode_CurrentBehavior(t *testing.T) {
	t.Parallel()
	data, err := formatLocalStreamEvent(channel.StreamEvent{Type: channel.StreamEventError, Error: providerOverloadedDetail})
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if want := `{"type":"error","error":"` + providerOverloadedDetail + `"}`; string(data) != want {
		t.Fatalf("sse event = %s, want %s", data, want)
	}
}

// A runner that delivered its failure in the stream returns no error; the
// outcome it reports is what the terminal write records. An error the runner
// returns still takes precedence, and a bare cancellation is left unnamed.
func TestWSRunOutcomeCarriesDeliveredFailure(t *testing.T) {
	t.Parallel()
	delivered := application.RunOutcome{
		Status: sessionruntime.RunStatusErrored,
		Cause:  apperror.New(apperror.CodeAgentProviderRateLimited, nil),
	}
	if got := wsRunOutcome(delivered, nil); got != delivered {
		t.Fatalf("wsRunOutcome(delivered, nil) = %+v, want the delivered outcome", got)
	}
	// A failure the stream delivered names the run even when the runner then
	// returns an error of its own; that error is only a diagnostic.
	runErr := apperror.New(apperror.CodeWorkspaceUnreachable, nil)
	if got := wsRunOutcome(delivered, runErr); got != delivered {
		t.Fatalf("wsRunOutcome(delivered, err) = %+v, want the delivered outcome", got)
	}
	if got := wsRunOutcome(application.RunOutcome{}, runErr); got.ErrorCode() != "workspace.unreachable" {
		t.Fatalf("wsRunOutcome(none, err) code = %q, want the returned error's", got.ErrorCode())
	}
	if got := wsRunOutcome(delivered, context.Canceled); got != (application.RunOutcome{}) {
		t.Fatalf("wsRunOutcome(delivered, canceled) = %+v, want unnamed", got)
	}

	// Without a proposal (the terminal event never reached the manager), the
	// delivered outcome alone names the failure.
	r := newFailureCharRun(t)
	r.handler.finishWSRun(context.Background(), wsRunAdmission{RunID: r.admission.RunID, Handle: r.admission.Handle}, wsRunOutcome(delivered, nil))
	if got, want := r.ledgerColumns(t), [3]string{"failed", "agent.provider_rate_limited", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
}

// Path A on the WebSocket path: the stream delivered a coded failure, and the
// runner then returned an error of its own, for example because replacing the
// retried turn in history failed. The run records the delivered failure's code;
// the later error does not rename it.
func TestCharacterizeWSPersistFailureAfterDeliveredFailure(t *testing.T) {
	t.Parallel()
	r := newFailureCharRun(t)
	r.forward(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventError, Code: "agent.provider_overloaded", Error: providerOverloadedDetail},
	)
	delivered := application.RunOutcome{
		Status: sessionruntime.RunStatusErrored,
		Cause:  apperror.New(apperror.CodeAgentProviderOverloaded, nil),
	}
	r.handler.finishWSRun(context.Background(), wsRunAdmission{RunID: r.admission.RunID, Handle: r.admission.Handle},
		wsRunOutcome(delivered, errors.New("replace history turn: boom")))

	if got, want := r.ledgerColumns(t), [3]string{"failed", "agent.provider_overloaded", ""}; got != want {
		t.Fatalf("session_runs = %q, want %q", got, want)
	}
	if got, want := r.runView(t), [2]string{"errored", "agent.provider_overloaded"}; got != want {
		t.Fatalf("run view = %q, want %q", got, want)
	}
}

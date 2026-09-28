package application

// Characterization tests for the outward results of a failed native Agent run
// as the application layer produces them: the public stream event, the history
// failure marker, and the terminal state a real session runtime manager records
// for the run. They pin CURRENT behavior so that the failure-classification
// refactor can show exactly which outward values it changes. A value that looks
// wrong is still asserted as it is today and marked "current behavior".
//
// Codes and texts are string literals on purpose: a renamed constant or edited
// catalog entry changes what clients receive, and must fail here.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

const (
	charOverloadedDetail  = "The model provider is overloaded right now. Please try again in a moment."
	charInterruptedDetail = "The model response was interrupted. Please try again."
	charExhaustedText     = "mid-stream retry: all 3 attempts failed (last: api error 503: Service Unavailable)"
)

// The classification every native exit shares: an EventError from the runtime
// becomes a lifecycle cause (its code feeds the run's terminal write and the
// history marker) and a public event (what WS, SSE and discuss subscribers see).
func TestCharacterizeNativeStreamErrorClassification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		event       native.StreamEvent
		code        string
		historyCode string
		public      string
	}{
		{
			name:        "retries exhausted on 503",
			event:       native.StreamEvent{Type: native.EventError, Error: charExhaustedText},
			code:        "agent.provider_overloaded",
			historyCode: "agent.provider_overloaded",
			public:      `{"type":"error","code":"agent.provider_overloaded","error":"` + charOverloadedDetail + `"}`,
		},
		{
			name:        "rate limited",
			event:       native.StreamEvent{Type: native.EventError, Error: "api error 429: Too Many Requests"},
			code:        "agent.provider_rate_limited",
			historyCode: "agent.provider_rate_limited",
		},
		{
			name:        "stream start failure",
			event:       native.StreamEvent{Type: native.EventError, Error: "stream start: dial tcp: connection refused"},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
			public:      `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		},
		{
			name:        "unrecognized text",
			event:       native.StreamEvent{Type: native.EventError, Error: "boom"},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
			public:      `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		},
		{
			name:        "empty text",
			event:       native.StreamEvent{Type: native.EventError},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
		},
		{
			// A catalogued code on the event wins over its text.
			name:        "coded event",
			event:       native.StreamEvent{Type: native.EventError, Code: "context.budget_unsatisfied", Error: "api error 503"},
			code:        "context.budget_unsatisfied",
			historyCode: "",
		},
		{
			// Current behavior: an unknown code is ignored and the text decides.
			name:        "uncatalogued code",
			event:       native.StreamEvent{Type: native.EventError, Code: "not.a.code", Error: "overloaded"},
			code:        "agent.provider_overloaded",
			historyCode: "agent.provider_overloaded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cause := agentStreamLifecycleError(tc.event)
			if got := string(apperror.CodeOf(cause)); got != tc.code {
				t.Fatalf("lifecycle code = %q, want %q", got, tc.code)
			}
			if got := string(snapshotFailureCode(false, cause)); got != tc.historyCode {
				t.Fatalf("history failure code = %q, want %q", got, tc.historyCode)
			}
			public := publicAgentStreamEvent(tc.event)
			if public.Code != tc.code {
				t.Fatalf("public event code = %q, want %q", public.Code, tc.code)
			}
			if tc.public != "" {
				data, err := json.Marshal(public)
				if err != nil {
					t.Fatalf("marshal public event: %v", err)
				}
				if string(data) != tc.public {
					t.Fatalf("public event = %s, want %s", data, tc.public)
				}
			}
		})
	}
}

// Scenario 10: a failure cause without an apperror code gets a public event, but
// only because agentFailureStreamEvent wraps it as an interruption.
func TestCharacterizeFailureEventForNonAppErrorCause(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{errors.New("SECRET provider response"), context.Canceled} {
		data, err := json.Marshal(agentFailureStreamEvent(cause))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if want := `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`; string(data) != want {
			t.Fatalf("failure event for %v = %s, want %s", cause, data, want)
		}
	}
	// The history marker is not written for such a cause: snapshotFailureCode
	// only accepts the codes it lists.
	if got := snapshotFailureCode(false, errors.New("SECRET provider response")); got != "" {
		t.Fatalf("history failure code = %q, want none", got)
	}
	if got := snapshotFailureCode(true, errors.New("idle")); got != "agent.response_timeout" {
		t.Fatalf("history failure code after idle timeout = %q", got)
	}
}

// History exit: a failure without assistant output stores the user message and
// an empty assistant message carrying the failure marker.
func TestCharacterizeHistoryFailureMarker(t *testing.T) {
	t.Parallel()
	for _, code := range []apperror.Code{apperror.CodeAgentProviderOverloaded, apperror.CodeAgentResponseInterrupted} {
		messages := &recordingMessageService{}
		service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
		if _, err := service.persistTurnFailure(context.Background(),
			ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "hello"},
			resolvedContext{}, code); err != nil {
			t.Fatalf("persistTurnFailure: %v", err)
		}
		if len(messages.persisted) != 2 || messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
			t.Fatalf("persisted = %#v, want user + assistant", messages.persisted)
		}
		metadata := messages.persisted[1].Metadata
		if metadata["agent_step_interrupted"] != true || metadata["error_code"] != string(code) {
			t.Fatalf("assistant metadata = %#v, want interrupted with error_code %q", metadata, code)
		}
		if _, hasError := metadata["error"]; hasError {
			t.Fatalf("assistant metadata = %#v, native failure has no error text key", metadata)
		}
	}
}

// History exit with visible partial output: the partial answer is stored and
// the failure code is not.
//
// Current behavior: a turn that failed after printing text looks like a normal
// assistant message in history.
func TestCharacterizeHistoryPartialOutputDropsFailureCode_CurrentBehavior(t *testing.T) {
	t.Parallel()
	messages := &recordingMessageService{}
	service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	service.persistPartialResult(context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "hello"},
		resolvedContext{},
		[]sdk.Message{sdk.AssistantMessage("partial answer")},
		nil, 0, false, true,
		apperror.CodeAgentProviderOverloaded, nil)

	if len(messages.persisted) != 2 {
		t.Fatalf("persisted = %#v, want user + assistant", messages.persisted)
	}
	metadata := messages.persisted[1].Metadata
	if _, has := metadata[messagepkg.HistoryErrorCodeMetadataKey]; has {
		t.Fatalf("assistant metadata = %#v, current behavior stores no error_code", metadata)
	}

	// Without visible output the same partial snapshot becomes the failure marker.
	hidden := &recordingMessageService{}
	service = &Service{messageService: hidden, logger: slog.New(slog.DiscardHandler)}
	service.persistPartialResult(context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "hello"},
		resolvedContext{},
		[]sdk.Message{sdk.AssistantMessage("partial answer")},
		nil, 0, false, false,
		apperror.CodeAgentProviderOverloaded, nil)
	if len(hidden.persisted) != 2 || hidden.persisted[1].Metadata["error_code"] != "agent.provider_overloaded" {
		t.Fatalf("persisted without visible output = %#v, want failure marker", hidden.persisted)
	}
}

// discussCharRun drives the native discuss pump against a real session runtime
// manager, so the run's published events, the turn port's errors, the live run
// view and the durable row all come from production code.
type discussCharRun struct {
	events  []string
	errs    []string
	errCode []string
	view    [3]string
	ledger  [3]string
	stores  int
}

type scriptedNativeStreamer struct{ events []native.StreamEvent }

func (s *scriptedNativeStreamer) Stream(context.Context, native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent, len(s.events))
	for _, event := range s.events {
		ch <- event
	}
	close(ch)
	return ch
}

func runDiscussCharacterization(t *testing.T, events ...native.StreamEvent) discussCharRun {
	t.Helper()
	runs := sessionledger.New()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID: "owner-discuss-failure", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	resolver := &fakeDiscussService{}
	service := newDiscussTestService(&fakeRunner{}, &scriptedNativeStreamer{events: events}, resolver)
	service.SetSessionRuntime(manager)

	cmd := discussCommand()
	handle, err := service.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatalf("start discuss turn: %v", err)
	}
	var out discussCharRun
	for event := range handle.Events() {
		out.events = append(out.events, string(event.Payload))
	}
	for err := range handle.Errs() {
		out.errs = append(out.errs, err.Error())
		out.errCode = append(out.errCode, string(apperror.CodeOf(err)))
	}
	out.stores = resolver.storeCalls

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := manager.Snapshot(context.Background(), cmd.BotID, cmd.ThreadID)
		if err != nil || snapshot.CurrentRunView == nil {
			t.Fatalf("snapshot = %+v, %v", snapshot, err)
		}
		view := snapshot.CurrentRunView
		run, err := runs.Get(context.Background(), view.RunID)
		if err != nil {
			t.Fatalf("load ledger run: %v", err)
		}
		if run.State.Terminal() || time.Now().After(deadline) {
			out.view = [3]string{view.Status, view.ErrorCode, view.Error}
			out.ledger = [3]string{string(run.State), run.ErrorCode, run.ErrorMessage}
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("%s = %s\nwant %s", what, gotJSON, wantJSON)
	}
}

// Scenario 2 on the discuss path: the native runtime gives up after retries,
// publishes its giving-up EventError and ends with agent_abort.
//
// Current behavior: the turn port reports no error (the IM discuss runner only
// learns of the failure from the error event), and no history is written. The
// terminal event carries the run's failure code.
func TestCharacterizeDiscussRetriesExhausted_CurrentBehavior(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3, RetryError: "api error 503"},
		native.StreamEvent{Type: native.EventError, Error: charExhaustedText},
		native.StreamEvent{Type: native.EventAgentAbort, Messages: json.RawMessage(`[]`)},
	)
	assertStrings(t, "turn events", got.events, []string{
		`{"runtime_type":""}`,
		`{"type":"agent_start"}`,
		`{"type":"retry","attempt":1,"maxAttempt":3,"retryError":"api error 503"}`,
		`{"type":"error","code":"agent.provider_overloaded","error":"` + charOverloadedDetail + `"}`,
		`{"type":"agent_abort","messages":[],"code":"agent.provider_overloaded"}`,
	})
	assertStrings(t, "turn errors", got.errs, nil)
	if want := [3]string{"failed", "agent.provider_overloaded", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [3]string{"errored", "agent.provider_overloaded", charOverloadedDetail}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
	if got.stores != 0 {
		t.Fatalf("history rounds stored = %d, current behavior stores none", got.stores)
	}
}

// X3 on the discuss path: the attempts fail with different classes. The run
// records the first failure, the same one the lifecycle and history record,
// not the last stream error the live view saw.
func TestCharacterizeDiscussMixedFailureClassesRecordFirst(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventError, Error: "api error 429: rate limited"},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3, RetryError: "api error 429"},
		native.StreamEvent{Type: native.EventError, Error: charExhaustedText},
		native.StreamEvent{Type: native.EventAgentAbort, Messages: json.RawMessage(`[]`)},
	)
	if want := [3]string{"failed", "agent.provider_rate_limited", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [3]string{"errored", "agent.provider_rate_limited", charOverloadedDetail}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
}

// Scenario 3 on the discuss path: a retried attempt that recovers.
func TestCharacterizeDiscussRetryRecovered(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3, RetryError: "api error 503"},
		native.StreamEvent{Type: native.EventAgentEnd, Messages: json.RawMessage(`[{"role":"assistant","content":"done"}]`)},
	)
	assertStrings(t, "turn errors", got.errs, nil)
	if want := [3]string{"completed", "", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [3]string{"completed", "", ""}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
	if got.stores != 1 {
		t.Fatalf("history rounds stored = %d, want 1", got.stores)
	}
}

// Scenario 1 on the discuss path: the runtime fails before any output (for
// example "stream start: ...") and closes the stream without a terminal event.
// This is the one discuss failure the turn port reports as an error.
func TestCharacterizeDiscussFailureWithoutTerminalEvent(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventError, Error: "stream start: dial tcp: connection refused"},
	)
	assertStrings(t, "turn events", got.events, []string{
		`{"runtime_type":""}`,
		`{"type":"agent_start"}`,
		`{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
	})
	assertStrings(t, "turn errors", got.errs, []string{"agent.response_interrupted"})
	assertStrings(t, "turn error codes", got.errCode, []string{"agent.response_interrupted"})
	if want := [3]string{"failed", "agent.response_interrupted", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [3]string{"errored", "agent.response_interrupted", charInterruptedDetail}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
}

// Scenario 10 through turnRunFinisher: whatever the cause, only its catalogued
// code reaches the runtime.
//
// Current behavior: a cause without a code finishes as runtime_run_failed; an
// External Agent cause finishes with its code.
func TestCharacterizeTurnRunFinisherCodes_CurrentBehavior(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status string
		cause  error
		want   [3]string
	}{
		{"plain cause", sessionruntime.RunStatusErrored, errors.New("SECRET adapter crashed"), [3]string{"failed", "runtime_run_failed", ""}},
		{"coded cause", sessionruntime.RunStatusErrored, apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("dial"), nil), [3]string{"failed", "workspace.unreachable", ""}},
		{"external agent cause", sessionruntime.RunStatusErrored, apperror.New(apperror.CodeACPAgentNotConfigured, nil), [3]string{"failed", "acp_agent_not_configured", ""}},
		{"errored without cause", sessionruntime.RunStatusErrored, nil, [3]string{"failed", "runtime_run_failed", ""}},
		{"aborted", sessionruntime.RunStatusAborted, context.Canceled, [3]string{"aborted", "", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runs := sessionledger.New()
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
				OwnerID: "owner-finisher", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
			})
			t.Cleanup(func() { _ = manager.Close() })
			service := &Service{logger: slog.New(slog.DiscardHandler)}
			service.SetSessionRuntime(manager)
			admission, err := manager.Admit(context.Background(), sessionruntime.AdmitInput{
				BotID: "bot-1", SessionID: "session-1", InvocationID: "invocation-finisher", Payload: []byte(`{}`),
				Execution: sessionruntime.Execution{
					Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
						return sessionruntime.RunAdmissionView{}, nil
					},
				},
			})
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			service.turnRunFinisher(context.Background(), admission)(RunOutcome{Status: tc.status, Cause: tc.cause})
			run, err := runs.Get(context.Background(), admission.RunID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if got := [3]string{string(run.State), run.ErrorCode, run.ErrorMessage}; got != tc.want {
				t.Fatalf("session_runs = %q, want %q", got, tc.want)
			}
		})
	}
}

// Scenario 7 (X7): a decision continuation that fails after the run resumed.
//
// Current behavior: session_runs records the cause's catalogued code in
// error_code and no message; a cause without one finishes as
// runtime_run_failed.
func TestCharacterizeDecisionContinuationFailureColumns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cause error
		want  [3]string
	}{
		{"coded cause", apperror.Wrap(apperror.CodeSessionHistoryInconsistent, errors.New("SECRET"), nil), [3]string{"failed", "session_runtime.history_inconsistent", ""}},
		{"plain cause", errors.New("SECRET continuation failed"), [3]string{"failed", "runtime_run_failed", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runs := sessionledger.New()
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
				OwnerID: "owner-continuation", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
			})
			t.Cleanup(func() { _ = manager.Close() })
			admission, err := manager.Admit(context.Background(), sessionruntime.AdmitInput{
				BotID: "bot-1", SessionID: "session-1", InvocationID: "invocation-continuation", Payload: []byte(`{}`),
				Execution: sessionruntime.Execution{
					Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
						return sessionruntime.RunAdmissionView{}, nil
					},
				},
			})
			if err != nil {
				t.Fatalf("admit: %v", err)
			}
			service := &Service{decisionRuntime: manager, logger: slog.New(slog.DiscardHandler)}
			service.finishRuntimeDecision(context.Background(), admission.Handle, tc.cause)
			run, err := runs.Get(context.Background(), admission.RunID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if got := [3]string{string(run.State), run.ErrorCode, run.ErrorMessage}; got != tc.want {
				t.Fatalf("session_runs = %q, want %q", got, tc.want)
			}
		})
	}
}

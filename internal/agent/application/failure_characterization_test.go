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
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/models/modelretry"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

const (
	charOverloadedDetail  = "The model provider is unavailable or overloaded right now. Please try again in a moment."
	charInterruptedDetail = "The model response was interrupted. Please try again."
	charRejectedDetail    = "The model provider rejected the request. Check the model settings, or try another model."
	charUnreachableDetail = "Memoh could not reach the model provider. Check the provider address and that the service is running."
)

// charProviderErr is a provider's answer as the SDK reports it; its message
// and request id stay in the process.
func charProviderErr(status int, kind sdk.ErrorKind) error {
	return &sdk.APIError{Provider: "openai-completions", StatusCode: status, Kind: kind, Message: "SECRET provider message", RequestID: "req_SECRET"}
}

// charExhausted is the cause the native runtime ends a run with when every
// retry of a model call failed.
func charExhausted(last error) error {
	return errs.Wrap(errs.WrapDependency(last, "model stream"), "model call retries exhausted")
}

// charFailingProvider fails every model call with err.
type charFailingProvider struct {
	abortAlignmentProvider
	err error
}

func (p charFailingProvider) DoStream(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
	return nil, p.err
}

// charModelCallFailure is the cause a native run ends with when its model call
// fails with err, retried once when err is worth another call.
func charModelCallFailure(t *testing.T, err error) error {
	t.Helper()
	var cause error
	for event := range native.New(native.Deps{}).Stream(context.Background(), native.RunConfig{
		Model:            &sdk.Model{ID: "char-model", Provider: charFailingProvider{err: err}},
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		Identity:         native.SessionContext{BotID: "char-bot"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            modelretry.Config{MaxAttempts: 1, FastAttempts: 1},
	}) {
		if event.Type == native.EventError {
			cause = event.Cause
		}
	}
	if cause == nil {
		t.Fatalf("native run with a model call failing on %v reported no error", err)
	}
	return cause
}

// The classification every native exit shares: an EventError from the runtime
// becomes a lifecycle cause (its code feeds the run's terminal write and the
// history marker) and a public event (what WS, SSE and discuss subscribers see).
func TestCharacterizeNativeStreamErrorClassification(t *testing.T) {
	t.Parallel()
	refused := &url.Error{Op: "Post", URL: "http://127.0.0.1:1/v1/chat/completions", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	for _, tc := range []struct {
		name        string
		event       native.StreamEvent
		code        string
		historyCode string
		public      string
	}{
		{
			name:        "retries exhausted on 503",
			event:       native.StreamEvent{Type: native.EventError, Cause: charExhausted(charProviderErr(503, sdk.KindServerError))},
			code:        "agent.provider_overloaded",
			historyCode: "agent.provider_overloaded",
			public:      `{"type":"error","code":"agent.provider_overloaded","error":"` + charOverloadedDetail + `"}`,
		},
		{
			name:        "retries exhausted on a rate limit",
			event:       native.StreamEvent{Type: native.EventError, Cause: charExhausted(charProviderErr(429, sdk.KindRateLimited))},
			code:        "agent.provider_rate_limited",
			historyCode: "agent.provider_rate_limited",
		},
		{
			name:        "request the provider rejected",
			event:       native.StreamEvent{Type: native.EventError, Cause: errs.WrapDependency(charProviderErr(400, sdk.KindUnknown), "model stream")},
			code:        "agent.provider_request_rejected",
			historyCode: "agent.provider_request_rejected",
			public:      `{"type":"error","code":"agent.provider_request_rejected","error":"` + charRejectedDetail + `"}`,
		},
		{
			name:        "key without access to the model",
			event:       native.StreamEvent{Type: native.EventError, Cause: errs.WrapDependency(charProviderErr(403, sdk.KindPermissionDenied), "model stream")},
			code:        "agent.provider_permission_denied",
			historyCode: "agent.provider_permission_denied",
		},
		{
			name:        "provider not listening",
			event:       native.StreamEvent{Type: native.EventError, Cause: charModelCallFailure(t, refused)},
			code:        "agent.provider_unreachable",
			historyCode: "agent.provider_unreachable",
			public:      `{"type":"error","code":"agent.provider_unreachable","error":"` + charUnreachableDetail + `"}`,
		},
		{
			// The request may have gone anywhere, so it names no provider.
			name:        "refused connection in the runtime's own work",
			event:       native.StreamEvent{Type: native.EventError, Cause: errs.Wrap(refused, "approval handler")},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
			public:      `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		},
		{
			name:        "stream start failure",
			event:       native.StreamEvent{Type: native.EventError, Cause: errors.New("stream start: SECRET dispatch failure")},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
			public:      `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		},
		{
			name:        "status text without an APIError",
			event:       native.StreamEvent{Type: native.EventError, Cause: errors.New("api error 429: Too Many Requests")},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
		},
		{
			name:        "no cause",
			event:       native.StreamEvent{Type: native.EventError},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
		},
		{
			name:        "context the budget cannot fit",
			event:       native.StreamEvent{Type: native.EventError, Cause: fmt.Errorf("prepare context view: %w: SECRET math", contextfrag.ErrBudgetUnsatisfied)},
			code:        "context.budget_unsatisfied",
			historyCode: "",
		},
		{
			name:        "protected context overflow",
			event:       native.StreamEvent{Type: native.EventError, Cause: fmt.Errorf("prepare context view: %w: SECRET cost", contextfrag.ErrProtectedContextOverflow)},
			code:        "context.protected_overflow",
			historyCode: "",
		},
		{
			name:        "failed steer checkpoint",
			event:       native.StreamEvent{Type: native.EventError, Cause: errs.Wrap(errors.New("SECRET ledger"), "checkpoint steered model call")},
			code:        "agent.response_interrupted",
			historyCode: "agent.response_interrupted",
			public:      `{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		},
		{
			// A catalogued code on the event wins over its cause.
			name:        "coded event",
			event:       native.StreamEvent{Type: native.EventError, Code: "context.budget_unsatisfied", Cause: charProviderErr(503, sdk.KindServerError)},
			code:        "context.budget_unsatisfied",
			historyCode: "",
		},
		{
			// An unknown code is ignored and the cause decides.
			name:        "uncatalogued code",
			event:       native.StreamEvent{Type: native.EventError, Code: "not.a.code", Cause: charProviderErr(529, sdk.KindServerError)},
			code:        "agent.provider_overloaded",
			historyCode: "agent.provider_overloaded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cause := agentStreamFailure(tc.event)
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
			data, err := json.Marshal(public)
			if err != nil {
				t.Fatalf("marshal public event: %v", err)
			}
			if tc.public != "" && string(data) != tc.public {
				t.Fatalf("public event = %s, want %s", data, tc.public)
			}
			if strings.Contains(string(data), "SECRET") {
				t.Fatalf("public event leaked the cause: %s", data)
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
	view    [2]string
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
			out.view = [2]string{view.Status, view.ErrorCode}
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
// Current behavior: the turn port reports no error, and no history is written.
// The retry reaches subscribers with its counters alone. The terminal event
// carries the run's failure code, and the stream ends with run_terminal naming
// the recorded state and code.
func TestCharacterizeDiscussRetriesExhausted_CurrentBehavior(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3},
		native.StreamEvent{Type: native.EventError, Cause: charExhausted(charProviderErr(503, sdk.KindServerError))},
		native.StreamEvent{Type: native.EventAgentAbort, Messages: json.RawMessage(`[]`)},
	)
	assertStrings(t, "turn events", got.events, []string{
		`{"runtime_type":""}`,
		`{"type":"agent_start"}`,
		`{"type":"retry","attempt":1,"maxAttempt":3}`,
		`{"type":"error","code":"agent.provider_overloaded","error":"` + charOverloadedDetail + `"}`,
		`{"type":"agent_abort","messages":[],"code":"agent.provider_overloaded"}`,
		`{"type":"run_terminal","state":"failed","error_code":"agent.provider_overloaded"}`,
	})
	assertStrings(t, "turn errors", got.errs, nil)
	if want := [3]string{"failed", "agent.provider_overloaded", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [2]string{"errored", "agent.provider_overloaded"}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
	if got.stores != 0 {
		t.Fatalf("history rounds stored = %d, current behavior stores none", got.stores)
	}
}

// X3 on the discuss path: the attempts fail with different classes. The run
// records the class of the last attempt, the one the giving-up error carries:
// the retried attempts publish no error of their own.
func TestCharacterizeDiscussMixedFailureClassesRecordLast(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3},
		native.StreamEvent{Type: native.EventError, Cause: charExhausted(charProviderErr(503, sdk.KindServerError))},
		native.StreamEvent{Type: native.EventAgentAbort, Messages: json.RawMessage(`[]`)},
	)
	if want := [3]string{"failed", "agent.provider_overloaded", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [2]string{"errored", "agent.provider_overloaded"}; got.view != want {
		t.Fatalf("run view = %q, want %q", got.view, want)
	}
}

// Scenario 3 on the discuss path: a retried attempt that recovers.
func TestCharacterizeDiscussRetryRecovered(t *testing.T) {
	t.Parallel()
	got := runDiscussCharacterization(t,
		native.StreamEvent{Type: native.EventAgentStart},
		native.StreamEvent{Type: native.EventRetry, Attempt: 1, MaxAttempt: 3},
		native.StreamEvent{Type: native.EventAgentEnd, Messages: json.RawMessage(`[{"role":"assistant","content":"done"}]`)},
	)
	assertStrings(t, "turn errors", got.errs, nil)
	if want := [3]string{"completed", "", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [2]string{"completed", ""}; got.view != want {
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
		native.StreamEvent{Type: native.EventError, Cause: errors.New("stream start: SECRET dispatch failure")},
	)
	assertStrings(t, "turn events", got.events, []string{
		`{"runtime_type":""}`,
		`{"type":"agent_start"}`,
		`{"type":"error","code":"agent.response_interrupted","error":"` + charInterruptedDetail + `"}`,
		`{"type":"run_terminal","state":"failed","error_code":"agent.response_interrupted"}`,
	})
	assertStrings(t, "turn errors", got.errs, []string{"agent.response_interrupted"})
	assertStrings(t, "turn error codes", got.errCode, []string{"agent.response_interrupted"})
	if want := [3]string{"failed", "agent.response_interrupted", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
	if want := [2]string{"errored", "agent.response_interrupted"}; got.view != want {
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
			service.turnRunFinisher(context.Background(), admission, sessionmode.Chat)(RunOutcome{Status: tc.status, Cause: tc.cause})
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

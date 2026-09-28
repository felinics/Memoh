package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

// scriptedFailureTransport answers the first streaming model requests with
// the scripted HTTP statuses and passes every later request to the real test
// provider. A 503 is retryable in the native runtime and a 400 is not, so the
// event order comes from production code: a single 503 gives
// EventError -> EventRetry -> AgentEnd, and 503 then 400 gives
// EventError -> EventRetry -> EventError -> AgentAbort.
type scriptedFailureTransport struct {
	base     http.RoundTripper
	statuses []int
	calls    atomic.Int32
}

func (t *scriptedFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	call := int(t.calls.Add(1))
	if call > len(t.statuses) {
		return t.base.RoundTrip(r)
	}
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	status := t.statuses[call-1]
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + http.StatusText(status) + `"}}`)),
		Request:    r,
	}, nil
}

// noDecisionQueries reports that the run has no pending decisions.
type noDecisionQueries struct{ *directLifecycleQueries }

func (noDecisionQueries) ListToolApprovalsByRun(context.Context, pgtype.UUID) ([]sqlc.ToolApprovalRequest, error) {
	return nil, nil
}

func (noDecisionQueries) ListUserInputsByRun(context.Context, pgtype.UUID) ([]sqlc.UserInputRequest, error) {
	return nil, nil
}

// proposalRecordingLedger records every finish proposal the manager makes, in
// order, so the test can show which outcome reached the ledger first.
type proposalRecordingLedger struct {
	*sessionledger.Store
	mu        sync.Mutex
	proposals [][2]string
	// failFirst makes the first proposal write fail, as a transient database
	// error would, so the terminal event defers the outcome to finish.
	failFirst bool
}

func (l *proposalRecordingLedger) PrepareFinish(ctx context.Context, params ledger.PrepareFinishParams) (ledger.Run, bool, error) {
	l.mu.Lock()
	l.proposals = append(l.proposals, [2]string{string(params.State), params.ErrorCode})
	failFirst := l.failFirst && len(l.proposals) == 1
	l.mu.Unlock()
	if failFirst {
		return ledger.Run{}, false, errors.New("proposal write temporarily unavailable")
	}
	return l.Store.PrepareFinish(ctx, params)
}

func (l *proposalRecordingLedger) snapshot() [][2]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][2]string(nil), l.proposals...)
}

func newScriptedFailureFixture(t *testing.T, statuses ...int) (directLifecycleFixture, *scriptedFailureTransport) {
	t.Helper()
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	transport := &scriptedFailureTransport{base: fixture.service.streamHTTPClient.Transport, statuses: statuses}
	fixture.service.streamHTTPClient = &http.Client{Transport: transport}
	return fixture, transport
}

func eventTypes(t *testing.T, payloads []json.RawMessage) []string {
	t.Helper()
	types := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		var env struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			t.Fatalf("decode event %s: %v", payload, err)
		}
		types = append(types, env.Type)
	}
	return types
}

// turnRun is what one native turn through the turn port produced.
type turnRun struct {
	types     []string
	errCodes  []string
	proposals [][2]string
	ledger    [3]string
}

// runNativeTurn drives StartTurn -> StreamChat against a real session runtime
// manager and an in-memory ledger that records every finish proposal.
func runNativeTurn(t *testing.T, fixture directLifecycleFixture) turnRun {
	t.Helper()
	return runNativeTurnWith(t, fixture, &proposalRecordingLedger{Store: sessionledger.New()})
}

func runNativeTurnWith(t *testing.T, fixture directLifecycleFixture, runs *proposalRecordingLedger) turnRun {
	t.Helper()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID: "owner-retry-turn", OwnerLeaseTTL: time.Minute, Ledger: runs, Fence: abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	fixture.service.SetSessionRuntime(manager)
	// The real manager asks for the run's pending decisions when it finishes.
	fixture.service.queries = noDecisionQueries{fixture.service.queries.(*directLifecycleQueries)}

	handle, err := fixture.service.StartTurn(context.Background(), turn.StartTurnCommand{
		SchemaVersion:        1,
		TeamID:               "retry-team",
		Mode:                 turn.ModeChat,
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	var payloads []json.RawMessage
	var out turnRun
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range handle.Events() {
			payloads = append(payloads, event.Payload)
		}
		for err := range handle.Errs() {
			out.errCodes = append(out.errCodes, string(apperror.CodeOf(err)))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	out.types = eventTypes(t, payloads)
	out.proposals = runs.snapshot()
	run, err := runs.Get(context.Background(), handle.RunID())
	if err != nil {
		t.Fatalf("load ledger run: %v", err)
	}
	out.ledger = [3]string{string(run.State), run.ErrorCode, run.ErrorMessage}
	return out
}

// X4, turn RPC (IM) native path: a stream error that the runtime retried and
// recovered from is not reported. The caller's Errs() stays empty, both
// finish proposals say completed, and session_runs ends completed.
func TestStreamChatRetrySuccessReportsNoError(t *testing.T) {
	fixture, transport := newScriptedFailureFixture(t, http.StatusServiceUnavailable)
	got := runNativeTurn(t, fixture)

	if calls := transport.calls.Load(); calls < 2 {
		t.Fatalf("model requests = %d, want the failed attempt and its retry", calls)
	}
	assertOrdered(t, got.types, "error", "retry", "agent_end")
	if len(fixture.messages.persisted) == 0 {
		t.Fatal("the recovered answer was not stored")
	}
	assertStrings(t, "turn error codes", got.errCodes, nil)
	// The AgentEnd event proposes completed through HandleAgentEvent, and
	// runHandle.finish proposes the same outcome afterwards.
	wantProposals := [][2]string{{"completed", ""}, {"completed", ""}}
	if len(got.proposals) != len(wantProposals) || got.proposals[0] != wantProposals[0] || got.proposals[1] != wantProposals[1] {
		t.Fatalf("finish proposals = %q, want %q", got.proposals, wantProposals)
	}
	if want := [3]string{"completed", "", ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
}

// A retry that does not recover still fails the turn: the retried 503 is
// followed by a non-retryable 400, the run ends with agent_abort, and the
// caller's Errs() reports it.
func TestStreamChatRetryFailureReportsError(t *testing.T) {
	fixture, transport := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	got := runNativeTurn(t, fixture)

	if calls := transport.calls.Load(); calls != 2 {
		t.Fatalf("model requests = %d, want the retried attempt and the final one", calls)
	}
	assertOrdered(t, got.types, "error", "retry", "error", "agent_abort")
	if len(got.errCodes) != 1 || got.errCodes[0] == "" {
		t.Fatalf("turn error codes = %q, want exactly one coded error", got.errCodes)
	}
	// X3: the run records the same first failure the turn port reports, not
	// the last stream error.
	if want := [3]string{"failed", got.errCodes[0], ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
}

// A WebSocket stream whose retry does not recover delivers the failure in the
// stream and returns no error; its outcome still names the failure for the
// terminal write, with the code of the first error.
func TestStreamChatWSRetryFailureReportsOutcome(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	eventCh := make(chan WSStreamEvent)
	var payloads []json.RawMessage
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range eventCh {
			payloads = append(payloads, event)
		}
	}()
	_, outcome, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, nil)
	close(eventCh)
	<-drained

	if err != nil {
		t.Fatalf("WS turn error = %v, want the failure delivered in the stream", err)
	}
	assertOrdered(t, eventTypes(t, payloads), "error", "retry", "error", "agent_abort")
	var first, terminal struct{ Type, Code string }
	for _, payload := range payloads {
		var event struct{ Type, Code string }
		_ = json.Unmarshal(payload, &event)
		if event.Type == "error" && first.Type == "" {
			first = event
		}
		if event.Type == "agent_abort" {
			terminal = event
		}
	}
	if outcome.Status != sessionruntime.RunStatusErrored || outcome.ErrorCode() != first.Code || first.Code == "" {
		t.Fatalf("outcome = %q / %q, want errored with the first error's code %q", outcome.Status, outcome.ErrorCode(), first.Code)
	}
	if terminal.Code != first.Code {
		t.Fatalf("agent_abort code = %q, want %q", terminal.Code, first.Code)
	}
}

// X4, WebSocket path: the same error -> retry -> AgentEnd sequence. AgentEnd
// clears lifecycleCause and the function returns nil.
func TestStreamChatWSRetrySuccessReportsNoError_X4(t *testing.T) {
	fixture, transport := newScriptedFailureFixture(t, http.StatusServiceUnavailable)
	eventCh := make(chan WSStreamEvent)
	var payloads []json.RawMessage
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range eventCh {
			payloads = append(payloads, event)
		}
	}()
	_, _, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, nil)
	close(eventCh)
	<-drained

	if got := transport.calls.Load(); got < 2 {
		t.Fatalf("model requests = %d, want the failed attempt and its retry", got)
	}
	assertOrdered(t, eventTypes(t, payloads), "error", "retry", "agent_end")
	if err != nil {
		t.Fatalf("WS turn error = %v, want nil after a recovered retry", err)
	}
	if len(fixture.messages.persisted) == 0 {
		t.Fatal("the recovered answer was not stored")
	}
}

// assertOrdered checks that want appears in got as a subsequence.
func assertOrdered(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, value := range got {
		if i < len(want) && value == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event types = %q, want %q in order", got, want)
	}
}

// A WebSocket stream that delivered a failure and then failed to persist the
// turn returns the persistence error for the caller to report, but its outcome
// still names the delivered failure: the run's code is named once, when the
// failure is first declared.
func TestStreamChatWSPersistFailureAfterDeliveredFailureKeepsItsCode(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	eventCh := make(chan WSStreamEvent)
	var payloads []json.RawMessage
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range eventCh {
			payloads = append(payloads, event)
		}
	}()
	persistErr := errors.New("replace history turn: boom")
	_, outcome, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, func(context.Context, []messagepkg.Message) error {
		return persistErr
	})
	close(eventCh)
	<-drained

	if !errors.Is(err, persistErr) {
		t.Fatalf("WS turn error = %v, want the persistence failure", err)
	}
	var first struct{ Type, Code string }
	for _, payload := range payloads {
		if err := json.Unmarshal(payload, &first); err == nil && first.Type == "error" {
			break
		}
		first.Type, first.Code = "", ""
	}
	if first.Code == "" {
		t.Fatalf("no coded error event in %q", eventTypes(t, payloads))
	}
	if outcome.Status != sessionruntime.RunStatusErrored || outcome.ErrorCode() != first.Code {
		t.Fatalf("outcome = %q / %q, want errored with the delivered code %q", outcome.Status, outcome.ErrorCode(), first.Code)
	}
}

// A WebSocket stream whose persistence fails without an earlier stream failure
// has no delivered outcome; the persistence error names the run.
func TestStreamChatWSPersistFailureWithoutDeliveredFailureHasNoOutcome(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	eventCh := make(chan WSStreamEvent)
	go func() {
		for range eventCh {
		}
	}()
	persistErr := errors.New("replace history turn: boom")
	_, outcome, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, func(context.Context, []messagepkg.Message) error {
		return persistErr
	})
	close(eventCh)

	if !errors.Is(err, persistErr) {
		t.Fatalf("WS turn error = %v, want the persistence failure", err)
	}
	if outcome != (RunOutcome{}) {
		t.Fatalf("outcome = %+v, want none", outcome)
	}
}

// failedRunOutcome prefers a failure the stream delivered before the error it
// is given, and the wrapped error still reads and unwraps as that error.
func TestFailedRunOutcomePrefersDeliveredFailure(t *testing.T) {
	delivered := RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: apperror.New(apperror.CodeAgentProviderOverloaded, nil)}
	commitErr := errors.New("commit agent step: boom")
	wrapped := withDeliveredOutcome(commitErr, delivered)
	if wrapped.Error() != commitErr.Error() || !errors.Is(wrapped, commitErr) || apperror.CodeOf(wrapped) != "" {
		t.Fatalf("wrapped error = %v (code %q), want it to read as %v", wrapped, apperror.CodeOf(wrapped), commitErr)
	}
	if got := failedRunOutcome(wrapped); got.ErrorCode() != string(apperror.CodeAgentProviderOverloaded) {
		t.Fatalf("failedRunOutcome(wrapped) code = %q", got.ErrorCode())
	}
	if got := failedRunOutcome(commitErr); got.Status != sessionruntime.RunStatusErrored || !errors.Is(got.Cause, commitErr) {
		t.Fatalf("failedRunOutcome(plain) = %+v", got)
	}
	var wrappedAgain *deliveredFailureError
	if plain := withDeliveredOutcome(commitErr, RunOutcome{}); errors.As(plain, &wrappedAgain) || !errors.Is(plain, commitErr) {
		t.Fatal("an error without a delivered failure must be returned unchanged")
	}
}

// Path A on the turn port: StreamChat reports a step commit failure after the
// stream delivered a coded failure. The run's terminal write records the
// delivered failure's code, while the caller is still told about the commit
// failure.
func TestTurnFinishAfterDeliveredFailureKeepsItsCode(t *testing.T) {
	delivered := RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: apperror.New(apperror.CodeAgentProviderOverloaded, nil)}
	var got RunOutcome
	h := &runHandle{
		streamErr: withDeliveredOutcome(errors.New("commit agent step: boom"), delivered),
		finishRun: func(outcome RunOutcome) sessionruntime.TerminalRun {
			got = outcome
			return sessionruntime.TerminalRun{}
		},
	}
	h.finish()
	if got.Status != sessionruntime.RunStatusErrored || got.ErrorCode() != string(apperror.CodeAgentProviderOverloaded) {
		t.Fatalf("terminal outcome = %q / %q, want errored with the delivered code", got.Status, got.ErrorCode())
	}
}

// A stream error that a retry recovered from is not a delivered failure: when
// persisting the recovered turn then fails, the persistence error names the
// run.
func TestStreamChatWSPersistFailureAfterRecoveredRetryHasNoOutcome(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable)
	eventCh := make(chan WSStreamEvent)
	var payloads []json.RawMessage
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range eventCh {
			payloads = append(payloads, event)
		}
	}()
	persistErr := errors.New("replace history turn: boom")
	_, outcome, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	}, eventCh, make(chan struct{}), nil, func(context.Context, []messagepkg.Message) error {
		return persistErr
	})
	close(eventCh)
	<-drained

	assertOrdered(t, eventTypes(t, payloads), "error", "retry")
	if !errors.Is(err, persistErr) {
		t.Fatalf("WS turn error = %v, want the persistence failure", err)
	}
	if outcome != (RunOutcome{}) {
		t.Fatalf("outcome = %+v, want none after a recovered retry", outcome)
	}
}

// When the terminal event's proposal write fails, the run's outcome is deferred
// to finish. The turn port's finish already holds the delivered failure, so the
// row still records the code the caller was told about.
func TestStreamChatDeferredProposalFinishKeepsDeliveredCode(t *testing.T) {
	fixture, _ := newScriptedFailureFixture(t, http.StatusServiceUnavailable, http.StatusBadRequest)
	runs := &proposalRecordingLedger{Store: sessionledger.New(), failFirst: true}
	got := runNativeTurnWith(t, fixture, runs)

	assertOrdered(t, got.types, "error", "retry", "error", "agent_abort")
	if len(got.errCodes) != 1 || got.errCodes[0] == "" {
		t.Fatalf("turn error codes = %q, want exactly one coded error", got.errCodes)
	}
	if len(got.proposals) != 2 || got.proposals[1] != [2]string{"failed", got.errCodes[0]} {
		t.Fatalf("finish proposals = %q, want the deferred one from finish with %q", got.proposals, got.errCodes[0])
	}
	if want := [3]string{"failed", got.errCodes[0], ""}; got.ledger != want {
		t.Fatalf("session_runs = %q, want %q", got.ledger, want)
	}
}

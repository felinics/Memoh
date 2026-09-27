package application

import (
	"context"
	"encoding/json"
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
}

func (l *proposalRecordingLedger) PrepareFinish(ctx context.Context, params ledger.PrepareFinishParams) (ledger.Run, bool, error) {
	l.mu.Lock()
	l.proposals = append(l.proposals, [2]string{string(params.State), params.ErrorCode})
	l.mu.Unlock()
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
	runs := &proposalRecordingLedger{Store: sessionledger.New()}
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
	if got.ledger[0] != "failed" {
		t.Fatalf("session_runs = %q, want failed", got.ledger)
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
	_, err := fixture.service.streamChatWSResultWithHooks(context.Background(), ChatRequest{
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

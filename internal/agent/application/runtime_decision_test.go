package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	sessiontest "github.com/felinics/memoh/internal/testutil/sessionruntime"
)

// A continuation that reports its failure as an event and then returns it
// fails once: one failure event in the decision output, a close at the next
// sequence, and one result record that carries the private cause.
func TestRuntimeDecisionContinuationFailureIsReportedOnce(t *testing.T) {
	backend := &decisionOutputBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
	manager, handle := newWaitingDecisionRuntime(t, backend)
	logger, logs := captureLogs()
	service := &Service{decisionRuntime: manager, logger: logger}
	manager.SetTerminalObserver(service.logRunResult)
	failure := apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("private idle timeout detail"), nil)
	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
		StreamOutput: true, ID: "answer-timeout", TargetID: "decision-timeout", Type: sessionruntime.CommandUserInputResponse,
		BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, Generation: handle.Generation,
	}, func(_ context.Context, _ *continuationLifecycleResult, ch chan<- WSStreamEvent) error {
		ch <- runtimeDecisionEvent(t, agentFailureStreamEvent(failure))
		return failure
	})

	failures := 0
	for _, entry := range backend.output {
		var event native.StreamEvent
		if entry.Output != nil && json.Unmarshal(entry.Output, &event) == nil && event.Type == native.EventError {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("decision output failure events = %d, want 1", failures)
	}
	if n := len(backend.output); n == 0 || backend.output[n-1].Type != "decision_output_end" {
		t.Fatal("decision output did not close")
	}
	records := waitRunResults(t, logs, 1)
	if got := recordAttrs(records[0])["error"]; !strings.Contains(got, "private idle timeout detail") {
		t.Fatalf("result record error = %q, want the private cause", got)
	}
	for _, record := range logs.atLevel(slog.LevelError) {
		if record.Message != "agent run" {
			t.Fatalf("unexpected error record %q besides the run result", record.Message)
		}
	}
}

type failFirstDecisionOutputBackend struct {
	*decisionOutputBackend
	failed atomic.Bool
}

func (b *failFirstDecisionOutputBackend) AppendDecisionOutput(ctx context.Context, ref sessionruntime.DecisionOutputRef, seq int64, payload json.RawMessage, limits sessionruntime.DecisionOutputLimits) (sessionruntime.DecisionOutputState, error) {
	if payload != nil && b.failed.CompareAndSwap(false, true) {
		return sessionruntime.DecisionOutputState{}, errors.New("decision output write unavailable")
	}
	return b.decisionOutputBackend.AppendDecisionOutput(ctx, ref, seq, payload, limits)
}

// A failure event that cannot be appended does not take a sequence number, so
// the close still lands at the next one.
func TestRuntimeDecisionUnappendedFailureKeepsOutputSequence(t *testing.T) {
	backend := &failFirstDecisionOutputBackend{decisionOutputBackend: &decisionOutputBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}}
	manager, handle := newWaitingDecisionRuntime(t, backend)
	service := &Service{decisionRuntime: manager}
	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
		StreamOutput: true, ID: "answer-unappended", BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, Generation: handle.Generation,
	}, func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error {
		return apperror.New(apperror.CodeAgentResponseTimeout, nil)
	})
	if len(backend.output) != 1 || backend.output[0].Type != "decision_output_end" {
		t.Fatalf("decision output = %+v, want only its close", backend.output)
	}
}

func newWaitingDecisionRuntime(t *testing.T, backends ...sessionruntime.Backend) (*sessionruntime.Manager, sessionruntime.RunHandle) {
	t.Helper()
	var backend sessionruntime.Backend = sessionruntime.NewMemoryBackend()
	if len(backends) > 0 {
		backend = backends[0]
	}
	manager := sessiontest.New(backend, sessionruntime.Options{
		OwnerID:       "runtime-lifecycle-owner",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := sessiontest.Start(context.Background(), manager,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		lifecycleTestRunID,
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start runtime run: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:        native.EventUserInputRequest,
		UserInputID: "44444444-4444-4444-8444-444444444444",
		Status:      "pending",
	}); err != nil {
		t.Fatalf("park runtime decision: %v", err)
	}
	if _, err := manager.FinishRun(context.Background(), handle, ""); err != nil {
		t.Fatalf("mark deferred producer ready: %v", err)
	}
	return manager, handle
}

func runtimeDecisionEvent(t *testing.T, event native.StreamEvent) WSStreamEvent {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type failNextRuntimeDecisionBackend struct {
	*sessionruntime.MemoryBackend
	failNext atomic.Bool
	err      error
}

func (b *failNextRuntimeDecisionBackend) Update(
	ctx context.Context,
	key sessionruntime.Key,
	update sessionruntime.SnapshotUpdate,
) (sessionruntime.Snapshot, bool, error) {
	if b.failNext.CompareAndSwap(true, false) {
		return sessionruntime.Snapshot{}, false, b.err
	}
	return b.MemoryBackend.Update(ctx, key, update)
}

func TestRuntimeDecisionTerminalDoesNotExposePrivateErrors(t *testing.T) {
	tests := []struct {
		name         string
		contextCause error
		cause        error
		status       string
		code         string
	}{
		{name: "success"},
		{
			name:         "explicit cancellation",
			contextCause: context.Canceled,
			cause:        context.Canceled,
		},
		{
			name:   "provider cancellation with active context",
			cause:  context.Canceled,
			status: sessionruntime.RunStatusErrored,
		},
		{
			name:         "ownership loss",
			contextCause: sessionruntime.ErrRunOwnershipLost,
			cause:        context.Canceled,
			status:       sessionruntime.RunStatusErrored,
		},
		{
			name:   "private provider error",
			cause:  errors.New("private provider detail"),
			status: sessionruntime.RunStatusErrored,
		},
		{
			name:   "stable application error",
			cause:  apperror.New(apperror.CodeSessionHistoryInconsistent, nil),
			status: sessionruntime.RunStatusErrored,
			code:   string(apperror.CodeSessionHistoryInconsistent),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.contextCause != nil {
				var cancel context.CancelCauseFunc
				ctx, cancel = context.WithCancelCause(ctx)
				cancel(tt.contextCause)
			}
			outcome := runtimeDecisionTerminal(ctx, tt.cause)
			if outcome.Status != tt.status || outcome.ErrorCode() != tt.code {
				t.Fatalf("runtimeDecisionTerminal() = (%q, %q), want (%q, %q)", outcome.Status, outcome.ErrorCode(), tt.status, tt.code)
			}
		})
	}
}

func TestContinueRuntimeDecisionDoesNotParkProviderCancellation(t *testing.T) {
	const (
		botID     = "bot-provider-cancel"
		sessionID = "session-provider-cancel"
		runID     = "run-provider-cancel"
	)
	manager := sessiontest.New(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-provider-cancel",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := sessiontest.Start(context.Background(), manager,
		botID,
		sessionID,
		runID,
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start runtime run: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:        native.EventUserInputRequest,
		UserInputID: "input-provider-cancel",
		Status:      "pending",
	}); err != nil {
		t.Fatalf("park runtime decision: %v", err)
	}
	if _, err := manager.FinishRun(context.Background(), handle, ""); err != nil {
		t.Fatalf("mark deferred producer ready: %v", err)
	}

	service := &Service{decisionRuntime: manager}
	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
		BotID:      botID,
		SessionID:  sessionID,
		RunID:      runID,
		Generation: handle.Generation,
	}, func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error {
		return context.Canceled
	})

	snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil {
		t.Fatalf("load terminal snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusErrored {
		t.Fatalf("terminal run = %#v, want errored instead of parked", snapshot.CurrentRunView)
	}
}

func TestContinueRuntimeDecisionCancelsContinuationAfterPublicationFailure(t *testing.T) {
	const (
		botID     = "bot-publish-failure"
		sessionID = "session-publish-failure"
		runID     = "run-publish-failure"
	)
	publishErr := errors.New("private runtime publication failure")
	backend := &failNextRuntimeDecisionBackend{
		MemoryBackend: sessionruntime.NewMemoryBackend(),
		err:           publishErr,
	}
	manager := sessiontest.New(backend, sessionruntime.Options{
		OwnerID:       "owner-publish-failure",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := sessiontest.Start(context.Background(), manager,
		botID,
		sessionID,
		runID,
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start runtime run: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:        native.EventUserInputRequest,
		UserInputID: "input-publish-failure",
		Status:      "pending",
	}); err != nil {
		t.Fatalf("park runtime decision: %v", err)
	}
	if _, err := manager.FinishRun(context.Background(), handle, ""); err != nil {
		t.Fatalf("mark deferred producer ready: %v", err)
	}

	command := sessionruntime.Command{
		BotID:      botID,
		SessionID:  sessionID,
		RunID:      runID,
		Generation: handle.Generation,
	}
	service := &Service{decisionRuntime: manager}
	continuationCause := make(chan error, 1)
	continuationDone := make(chan struct{})
	go func() {
		defer close(continuationDone)
		service.continueRuntimeDecision(context.Background(), command, func(
			continuationCtx context.Context,
			_ *continuationLifecycleResult,
			eventCh chan<- WSStreamEvent,
		) error {
			backend.failNext.Store(true)
			select {
			case eventCh <- WSStreamEvent(`{"type":"text_delta","delta":"late"}`):
			case <-continuationCtx.Done():
				continuationCause <- context.Cause(continuationCtx)
				return context.Cause(continuationCtx)
			}
			<-continuationCtx.Done()
			continuationCause <- context.Cause(continuationCtx)
			return context.Cause(continuationCtx)
		})
	}()

	select {
	case <-continuationDone:
	case <-time.After(time.Second):
		t.Fatal("runtime decision continuation hung after publication failure")
	}
	select {
	case cause := <-continuationCause:
		if !errors.Is(cause, context.Canceled) {
			t.Fatalf("continuation cause = %v, want context canceled", cause)
		}
	default:
		t.Fatal("continuation did not observe cancellation")
	}

	snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil {
		t.Fatalf("load terminal snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusErrored {
		t.Fatalf("terminal run = %#v, want errored", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.ErrorCode != "runtime_run_failed" {
		t.Fatalf("terminal run error code = %q, want runtime_run_failed", snapshot.CurrentRunView.ErrorCode)
	}
}

func TestContinueRuntimeDecisionPersistsCurrentStackTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name              string
		events            []native.StreamEvent
		wantLifecycle     string
		wantRuntimeStatus string
	}{
		{
			name:              "completed",
			events:            []native.StreamEvent{{Type: native.EventAgentEnd}},
			wantLifecycle:     contextLifecycleStatusCompleted,
			wantRuntimeStatus: sessionruntime.RunStatusCompleted,
		},
		{
			name: "provider failure",
			events: []native.StreamEvent{
				{Type: native.EventError, Error: "private provider detail"},
				{Type: native.EventAgentAbort},
			},
			wantLifecycle:     contextLifecycleStatusFailedProvider,
			wantRuntimeStatus: sessionruntime.RunStatusErrored,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, handle := newWaitingDecisionRuntime(t)
			lifecycles := &recordingContextLifecycleStore{}
			service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}
			snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
			if !ok {
				t.Fatal("test lifecycle snapshot is unavailable")
			}

			service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
				BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
				RunID: lifecycleTestRunID, Generation: handle.Generation,
			}, func(_ context.Context, result *continuationLifecycleResult, events chan<- WSStreamEvent) error {
				result.snapshot = &snapshot
				events <- runtimeDecisionEvent(t, native.StreamEvent{Type: native.EventAgentStart})
				for _, event := range tt.events {
					events <- runtimeDecisionEvent(t, event)
				}
				return nil
			})

			if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != tt.wantLifecycle {
				t.Fatalf("lifecycle creates = %#v, want one %s row", lifecycles.creates, tt.wantLifecycle)
			}
			if bytes.Contains(lifecycles.creates[0].Snapshot, []byte("private provider detail")) {
				t.Fatalf("lifecycle snapshot leaked private provider detail: %s", lifecycles.creates[0].Snapshot)
			}
			runtimeSnapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if runtimeSnapshot.CurrentRunView == nil || runtimeSnapshot.CurrentRunView.Status != tt.wantRuntimeStatus {
				t.Fatalf("runtime terminal = %#v, want %s", runtimeSnapshot.CurrentRunView, tt.wantRuntimeStatus)
			}
		})
	}
}

func TestContinueRuntimeDecisionDefersSecondPendingDecisionLifecycle(t *testing.T) {
	tests := []struct {
		name     string
		request  native.StreamEvent
		terminal native.StreamEvent
	}{
		{
			name:     "tool approval",
			request:  native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: "approval-next", Status: "pending"},
			terminal: native.StreamEvent{Type: native.EventAgentEnd, ApprovalID: "approval-next", Status: "pending"},
		},
		{
			name:     "user input",
			request:  native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "input-next", Status: "pending"},
			terminal: native.StreamEvent{Type: native.EventAgentEnd, UserInputID: "input-next", Status: "pending"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, handle := newWaitingDecisionRuntime(t)
			lifecycles := &recordingContextLifecycleStore{}
			service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}
			snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
			if !ok {
				t.Fatal("test lifecycle snapshot is unavailable")
			}

			service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
				BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
				RunID: lifecycleTestRunID, Generation: handle.Generation,
			}, func(_ context.Context, result *continuationLifecycleResult, events chan<- WSStreamEvent) error {
				result.snapshot = &snapshot
				events <- runtimeDecisionEvent(t, native.StreamEvent{Type: native.EventAgentStart})
				events <- runtimeDecisionEvent(t, tt.request)
				events <- runtimeDecisionEvent(t, tt.terminal)
				return nil
			})

			if len(lifecycles.creates) != 0 {
				t.Fatalf("lifecycle creates = %#v, want none while a second decision is pending", lifecycles.creates)
			}
			runtimeSnapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if runtimeSnapshot.CurrentRunView == nil || runtimeSnapshot.CurrentRunView.Status != sessionruntime.RunStatusWaitingDecision {
				t.Fatalf("runtime state = %#v, want waiting_decision", runtimeSnapshot.CurrentRunView)
			}
		})
	}
}

func TestContinueRuntimeDecisionExplicitAbortAlignsLifecycleAndRuntime(t *testing.T) {
	manager, handle := newWaitingDecisionRuntime(t)
	lifecycles := &recordingContextLifecycleStore{}
	service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}
	applied, err := manager.AbortControl(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		lifecycleTestRunID,
		"abort-continuation",
	)
	if err != nil || !applied {
		t.Fatalf("AbortControl() = (%t, %v), want (true, nil)", applied, err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)
	service.finishRuntimeDecision(ctx, handle, context.Canceled)

	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusAborted {
		t.Fatalf("lifecycle creates = %#v, want one aborted row", lifecycles.creates)
	}
	runtimeSnapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeSnapshot.CurrentRunView == nil || runtimeSnapshot.CurrentRunView.Status != sessionruntime.RunStatusAborted {
		t.Fatalf("runtime terminal = %#v, want aborted", runtimeSnapshot.CurrentRunView)
	}
}

func TestContinueRuntimeDecisionOwnershipLossDoesNotPersistLifecycle(t *testing.T) {
	manager, handle := newWaitingDecisionRuntime(t)
	lifecycles := &recordingContextLifecycleStore{}
	service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}
	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
		BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
		RunID: lifecycleTestRunID, Generation: handle.Generation + "-stale",
	}, func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error {
		t.Fatal("stale continuation unexpectedly ran")
		return nil
	})
	if len(lifecycles.creates) != 0 {
		t.Fatalf("ownership-lost continuation created lifecycle rows: %#v", lifecycles.creates)
	}
}

func TestStopTurnAbortsParkedDecision(t *testing.T) {
	manager, _ := newWaitingDecisionRuntime(t)
	service := &Service{decisionRuntime: manager, abortRuntime: manager, allowedTeam: "team-1"}
	cmd := turn.StopCommand{TeamID: "other-team", BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID}
	if _, err := service.StopTurn(context.Background(), cmd); !errors.Is(err, turn.ErrTeamNotServed) {
		t.Fatalf("wrong team: %v", err)
	}
	cmd.TeamID = "team-1"
	stopped, err := service.StopTurn(context.Background(), cmd)
	if err != nil || !stopped {
		t.Fatalf("stop = %t, %v", stopped, err)
	}
	snapshot, err := manager.Snapshot(context.Background(), cmd.BotID, cmd.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status != sessionruntime.RunStatusAborted {
		t.Fatalf("run = %#v", snapshot.CurrentRunView)
	}
}

type interruptReadDecisionBackend struct {
	*sessionruntime.MemoryBackend
	failRead atomic.Bool
}

func (b *interruptReadDecisionBackend) ReadDecisionOutput(ctx context.Context, ref sessionruntime.DecisionOutputRef, from int) (sessionruntime.DecisionOutputPage, error) {
	if b.failRead.Load() {
		return sessionruntime.DecisionOutputPage{}, errors.New("redis connection reset")
	}
	return b.MemoryBackend.ReadDecisionOutput(ctx, ref, from)
}

type staticDecisionStore struct {
	target sessionruntime.DecisionTarget
}

func (s *staticDecisionStore) ResolveRuntimeDecision(context.Context, string, string) (sessionruntime.DecisionTarget, error) {
	return s.target, nil
}

func (*staticDecisionStore) PendingRuntimeDecisions(context.Context, string) ([]sessionruntime.DecisionTarget, error) {
	return nil, nil
}

func (*staticDecisionStore) ParkedRunMessages(context.Context, ledger.Run) ([]chatview.UIMessage, error) {
	return nil, nil
}

func TestRouteRuntimeDecisionErrorMappingAndStatelessFullReplay(t *testing.T) {
	backend := &interruptReadDecisionBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
	manager, handle := newWaitingDecisionRuntime(t, backend)
	snap, _ := manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
	decisionID := "44444444-4444-4444-8444-444444444444"
	manager.SetDecisionStore(&staticDecisionStore{target: sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandToolApprovalResponse, ID: decisionID,
		BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID,
		TurnID: snap.CurrentRunView.TurnID, Status: "pending", FencingToken: handle.FencingToken,
	}})
	manager.SetCommandHandler(func(ctx context.Context, cmd sessionruntime.Command) error {
		_ = manager.PublishDecisionOutput(ctx, cmd, 1, json.RawMessage(`{"type":"text_delta","delta":"one"}`))
		_ = manager.PublishDecisionOutput(ctx, cmd, 2, json.RawMessage(`{"type":"text_delta","delta":"two"}`))
		return manager.PublishDecisionOutput(ctx, cmd, 3, nil)
	})
	svc := &Service{decisionRuntime: manager}
	in := ToolApprovalResponseInput{ControlID: "ctrl-app-interrupt", BotID: handle.BotID, ThreadID: handle.SessionID, ApprovalID: decisionID, Decision: "approve"}

	backend.failRead.Store(true)
	if handled, err := svc.routeToolApprovalResponse(context.Background(), in, make(chan WSStreamEvent, 8)); !handled || !errors.Is(err, turn.ErrDecisionOutputInterrupted) {
		t.Fatalf("interrupted = (%v, %v)", handled, err)
	}
	backend.failRead.Store(false)
	replayCh := make(chan WSStreamEvent, 8)
	if handled, err := svc.routeToolApprovalResponse(context.Background(), in, replayCh); !handled || err != nil || len(replayCh) != 3 {
		t.Fatalf("stateless replay = (%v, %v), len=%d", handled, err, len(replayCh))
	}
	if a, b, c := string(<-replayCh), string(<-replayCh), string(<-replayCh); !strings.Contains(a, "decision_accepted") || !strings.Contains(b, `"one"`) || !strings.Contains(c, `"two"`) {
		t.Fatalf("replayed events = (%s, %s, %s)", a, b, c)
	}

	eofMgr, eofHandle := newWaitingDecisionRuntime(t, sessionruntime.NewMemoryBackend())
	eofSnap, _ := eofMgr.Snapshot(context.Background(), eofHandle.BotID, eofHandle.SessionID)
	eofMgr.SetDecisionStore(&staticDecisionStore{target: sessionruntime.DecisionTarget{
		Type: sessionruntime.CommandUserInputResponse, ID: decisionID, BotID: eofHandle.BotID,
		SessionID: eofHandle.SessionID, RunID: eofHandle.RunID, TurnID: eofSnap.CurrentRunView.TurnID,
		Status: "pending", FencingToken: eofHandle.FencingToken,
	}})
	eofMgr.SetCommandHandler(func(ctx context.Context, cmd sessionruntime.Command) error {
		oversized, _ := json.Marshal(strings.Repeat("x", 9<<20))
		_ = eofMgr.PublishDecisionOutput(ctx, cmd, 1, oversized)
		return nil
	})
	eofCh := make(chan WSStreamEvent, 8)
	if handled, err := (&Service{decisionRuntime: eofMgr}).routeUserInputResponse(context.Background(), UserInputResponseInput{
		ControlID: "ctrl-eof", BotID: eofHandle.BotID, ThreadID: eofHandle.SessionID, UserInputID: decisionID, TextAnswer: "ok",
	}, eofCh); !handled || err != nil {
		t.Fatalf("permanent EOF = (%v, %v)", handled, err)
	}
	var sawError bool
	for len(eofCh) > 0 {
		var ev native.StreamEvent
		if json.Unmarshal(<-eofCh, &ev) == nil && ev.Type == native.EventError {
			sawError = true
		}
	}
	if !sawError || implicitDecisionControlID(sessionruntime.CommandToolApprovalResponse, "dec-1", false) == implicitDecisionControlID(sessionruntime.CommandToolApprovalResponse, "dec-1", true) {
		t.Fatal("expected EventError on permanent EOF and distinct streaming/non-streaming ControlIDs")
	}
}

type nonUUIDDecisionQueries struct {
	dbstore.Queries
	mu        sync.Mutex
	approvals []sqlc.ToolApprovalRequest
	inputs    []sqlc.UserInputRequest
}

func (q *nonUUIDDecisionQueries) GetToolApprovalRequest(_ context.Context, id pgtype.UUID) (sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.approvals {
		if r.ID == id {
			return r, nil
		}
	}
	return sqlc.ToolApprovalRequest{}, pgx.ErrNoRows
}

func (q *nonUUIDDecisionQueries) ListToolApprovalsBySession(_ context.Context, arg sqlc.ListToolApprovalsBySessionParams) ([]sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []sqlc.ToolApprovalRequest
	for _, r := range q.approvals {
		if r.BotID == arg.BotID && r.SessionID == arg.SessionID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (q *nonUUIDDecisionQueries) GetUserInputRequest(_ context.Context, id pgtype.UUID) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.inputs {
		if r.ID == id {
			return r, nil
		}
	}
	return sqlc.UserInputRequest{}, pgx.ErrNoRows
}

func (q *nonUUIDDecisionQueries) ListUserInputsBySession(_ context.Context, arg sqlc.ListUserInputsBySessionParams) ([]sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []sqlc.UserInputRequest
	for _, r := range q.inputs {
		if r.BotID == arg.BotID && r.SessionID == arg.SessionID {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestRouteRuntimeDecisionTargetResolutionAndPinning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit string
		replyMsg string
		promptID string
	}{
		{name: "short_id", explicit: "7", promptID: "ext-prompt-77"},
		{name: "reply_matching_prompt", replyMsg: "ext-prompt-77", promptID: "ext-prompt-77"},
		{name: "reply_empty_prompt_fallback", replyMsg: "feishu-card-1", promptID: ""},
		{name: "bare_latest_session", promptID: "ext-prompt-77"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &interruptReadDecisionBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
			manager, handle := newWaitingDecisionRuntime(t, backend)
			snap, _ := manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
			approvalUUID := "55555555-5555-4555-8555-555555555555"
			q := &nonUUIDDecisionQueries{approvals: []sqlc.ToolApprovalRequest{{
				ID: db.ParseUUIDOrEmpty(approvalUUID), BotID: db.ParseUUIDOrEmpty(handle.BotID),
				SessionID: db.ParseUUIDOrEmpty(handle.SessionID), RunID: db.ParseUUIDOrEmpty(handle.RunID),
				TurnID: db.ParseUUIDOrEmpty(snap.CurrentRunView.TurnID), ToolName: "bash", ShortID: 7,
				Status: "pending", PromptExternalMessageID: tc.promptID,
				RuntimeFencingToken: pgtype.Int8{Int64: handle.FencingToken, Valid: true},
			}}}
			svc := &Service{queries: q, decisionRuntime: manager}
			manager.SetDecisionStore(svc)
			manager.SetCommandHandler(func(ctx context.Context, cmd sessionruntime.Command) error {
				q.mu.Lock()
				q.approvals[0].Status = "approved"
				q.approvals[0].ResponseControlID = pgtype.Text{String: cmd.ID, Valid: true}
				q.approvals[0].ResponsePayloadHash = pgtype.Text{String: cmd.PayloadHash, Valid: true}
				q.mu.Unlock()
				_ = manager.PublishDecisionOutput(ctx, cmd, 1, json.RawMessage(`{"type":"text_delta","delta":"part-1"}`))
				_ = manager.PublishDecisionOutput(ctx, cmd, 2, json.RawMessage(`{"type":"text_delta","delta":"part-2"}`))
				return manager.PublishDecisionOutput(ctx, cmd, 3, nil)
			})
			backend.failRead.Store(true)
			in := ToolApprovalResponseInput{
				ControlID: "ctrl-init", DeferRelease: true, BotID: handle.BotID, ThreadID: handle.SessionID,
				ExplicitID: tc.explicit, ReplyExternalMessageID: tc.replyMsg, Decision: "approve",
			}
			if handled, err := svc.routeToolApprovalResponse(context.Background(), in, make(chan WSStreamEvent, 8)); !handled || !errors.Is(err, turn.ErrDecisionOutputInterrupted) {
				t.Fatalf("initial = (%v, %v)", handled, err)
			}
			backend.failRead.Store(false)
			in.ControlID = "ctrl-retry-new"
			out := make(chan WSStreamEvent, 8)
			if handled, err := svc.routeToolApprovalResponse(context.Background(), in, out); !handled || err != nil || len(out) != 3 {
				t.Fatalf("retry = (%v, %v), len=%d, want 3 full-replay events", handled, err, len(out))
			}
		})
	}

	// Cross-surface UserInput retry (/respond 9 -> Answers), ControlID pinning, reply guard, and stateless plain-text recovery.
	backend := &interruptReadDecisionBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
	manager, handle := newWaitingDecisionRuntime(t, backend)
	snap, _ := manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
	u1UUID, u2UUID := "66666666-6666-4666-8666-666666666666", "66666666-6666-4666-8666-777777777777"
	uiJSON, _ := json.Marshal(userinput.UIPayload{
		Version: 2,
		Questions: []userinput.UIQuestion{{
			ID: "q1", Kind: userinput.QuestionKindSingleSelect, Text: "Pick one",
			Options: []userinput.UIOption{{ID: "q1.o1", Label: "Alpha"}, {ID: "q1.o2", Label: "Beta"}},
		}},
	})
	interactionJSON, _ := json.Marshal(userinput.TextInteractionState{
		Completed: true, Answers: []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	})
	q := &nonUUIDDecisionQueries{inputs: []sqlc.UserInputRequest{{
		ID: db.ParseUUIDOrEmpty(u1UUID), BotID: db.ParseUUIDOrEmpty(handle.BotID),
		SessionID: db.ParseUUIDOrEmpty(handle.SessionID), RunID: db.ParseUUIDOrEmpty(handle.RunID),
		TurnID: db.ParseUUIDOrEmpty(snap.CurrentRunView.TurnID), ToolName: "ask_user", ShortID: 9,
		Status: "pending", UiPayloadJson: uiJSON, PromptExternalMessageID: "ext-input-9",
		RuntimeFencingToken: pgtype.Int8{Int64: handle.FencingToken, Valid: true},
	}}}
	svc := &Service{queries: q, userInput: &fakeUserInputService{}, decisionRuntime: manager}
	manager.SetDecisionStore(svc)
	manager.SetCommandHandler(func(ctx context.Context, cmd sessionruntime.Command) error {
		q.mu.Lock()
		q.inputs[0].Status, q.inputs[0].InteractionJson = userinput.StatusSubmitted, interactionJSON
		q.inputs[0].ResponseControlID = pgtype.Text{String: cmd.ID, Valid: true}
		q.inputs[0].ResponsePayloadHash = pgtype.Text{String: cmd.PayloadHash, Valid: true}
		q.mu.Unlock()
		_ = manager.PublishDecisionOutput(ctx, cmd, 1, json.RawMessage(`{"type":"text_delta","delta":"ans-1"}`))
		return manager.PublishDecisionOutput(ctx, cmd, 2, nil)
	})
	backend.failRead.Store(true)
	if _, err := svc.routeUserInputResponse(context.Background(), UserInputResponseInput{
		ControlID: "ctrl-u1", DeferRelease: true, BotID: handle.BotID, ThreadID: handle.SessionID,
		ExplicitID: "9", TextAnswer: "Alpha", UILanguage: "en",
	}, make(chan WSStreamEvent, 8)); !errors.Is(err, turn.ErrDecisionOutputInterrupted) {
		t.Fatal("expected initial /respond 9 interruption")
	}
	backend.failRead.Store(false)
	outU := make(chan WSStreamEvent, 8)
	if handled, err := svc.routeUserInputResponse(context.Background(), UserInputResponseInput{
		ControlID: "ctrl-u2", DeferRelease: true, BotID: handle.BotID, ThreadID: handle.SessionID,
		ExplicitID: "9", Answers: []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}, outU); !handled || err != nil || len(outU) != 2 {
		t.Fatalf("cross-surface retry = (%v, %v), len=%d", handled, err, len(outU))
	}
	if _, _, okWrong := svc.resolveRuntimeUserInputID(context.Background(), UserInputResponseInput{
		ControlID: "ctrl-wrong", BotID: handle.BotID, ThreadID: handle.SessionID, ReplyExternalMessageID: "other-msg",
	}); okWrong {
		t.Fatal("reply to unrelated message must not resolve when PromptExternalMessageID is set")
	}

	// Stateless plain-text recovery matches submitted option ("1"/"Alpha") only while unreleased.
	if res, err := svc.AdvancePlainTextUserInput(context.Background(), userinput.AdvanceTextInput{BotID: handle.BotID, SessionID: handle.SessionID, Text: "Beta"}); err != nil || res.Handled {
		t.Fatalf("different answer should not match: %+v %v", res, err)
	}
	if res, err := svc.AdvancePlainTextUserInput(context.Background(), userinput.AdvanceTextInput{BotID: handle.BotID, SessionID: handle.SessionID, Text: "1"}); err != nil || !res.Handled || res.Request.ID != u1UUID {
		t.Fatalf("matching unreleased plain-text recovery failed: %+v %v", res, err)
	}

	q.inputs = append(q.inputs, sqlc.UserInputRequest{
		ID: db.ParseUUIDOrEmpty(u2UUID), BotID: db.ParseUUIDOrEmpty(handle.BotID),
		SessionID: db.ParseUUIDOrEmpty(handle.SessionID), Status: userinput.StatusPending, ShortID: 10,
	})
	if gotID, _, ok := svc.resolveRuntimeUserInputID(context.Background(), UserInputResponseInput{
		ControlID: "ctrl-u1", BotID: handle.BotID, ThreadID: handle.SessionID,
	}); !ok || gotID != u1UUID {
		t.Fatalf("ControlID pinning resolved (%q, %v), want (%q, true)", gotID, ok, u1UUID)
	}

	q.inputs = q.inputs[:1]
	_ = backend.ReleaseDecisionOutput(context.Background(), sessionruntime.DecisionOutputRef{BotID: handle.BotID, CommandID: q.inputs[0].ResponseControlID.String}, "")
	if res, err := svc.AdvancePlainTextUserInput(context.Background(), userinput.AdvanceTextInput{BotID: handle.BotID, SessionID: handle.SessionID, Text: "Alpha"}); err != nil || res.Handled {
		t.Fatalf("released output must not intercept subsequent chat: %+v %v", res, err)
	}
}

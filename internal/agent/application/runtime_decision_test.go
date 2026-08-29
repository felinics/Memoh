package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

func newWaitingDecisionRuntime(t *testing.T) (*sessionruntime.Manager, sessionruntime.RunHandle) {
	t.Helper()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "runtime-lifecycle-owner",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := manager.StartRunHandle(
		context.Background(),
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
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
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
	sessionruntime.Backend
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
	return b.Backend.Update(ctx, key, update)
}

func TestRuntimeDecisionTerminalDoesNotExposePrivateErrors(t *testing.T) {
	tests := []struct {
		name         string
		contextCause error
		cause        error
		status       string
		message      string
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
			name:    "stable application error",
			cause:   apperror.New(apperror.CodeSessionHistoryInconsistent, nil),
			status:  sessionruntime.RunStatusErrored,
			message: string(apperror.CodeSessionHistoryInconsistent),
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
			status, message := runtimeDecisionTerminal(ctx, tt.cause)
			if status != tt.status || message != tt.message {
				t.Fatalf("runtimeDecisionTerminal() = (%q, %q), want (%q, %q)", status, message, tt.status, tt.message)
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
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-provider-cancel",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := manager.StartRunHandle(
		context.Background(),
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
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
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
		Backend: sessionruntime.NewMemoryBackend(),
		err:     publishErr,
	}
	manager := sessionruntime.NewManager(backend, sessionruntime.Options{
		OwnerID:       "owner-publish-failure",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	handle, err := manager.StartRunHandle(
		context.Background(),
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
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
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
	if snapshot.CurrentRunView.Error != "" {
		t.Fatalf("terminal run error = %q, want no private publication detail", snapshot.CurrentRunView.Error)
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

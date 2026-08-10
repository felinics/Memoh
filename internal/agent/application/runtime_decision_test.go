package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/apperror"
)

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
	}, func(context.Context, chan<- WSStreamEvent) error {
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

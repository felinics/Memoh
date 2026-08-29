package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

var errInjectedTerminalLoad = errors.New("injected post-terminal load failure")

type terminalLoadFaultBackend struct {
	*sessionruntime.MemoryBackend
	failNextLoad atomic.Bool
}

func (b *terminalLoadFaultBackend) Load(ctx context.Context, key sessionruntime.Key) (sessionruntime.Snapshot, bool, error) {
	if b.failNextLoad.CompareAndSwap(true, false) {
		return sessionruntime.Snapshot{}, false, errInjectedTerminalLoad
	}
	return b.MemoryBackend.Load(ctx, key)
}

func TestSpawnCleanEndRacingAbortControlAlignsAllTerminals(t *testing.T) {
	runs := &abortAlignmentLedger{}
	backend := &terminalLoadFaultBackend{MemoryBackend: sessionruntime.NewMemoryBackend()}
	manager := sessionruntime.NewManager(backend, sessionruntime.Options{
		OwnerID:       "clean-end-abort-owner",
		OwnerLeaseTTL: time.Minute,
		Ledger:        runs,
		Fence:         abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	lifecycles := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: lifecycles}
	service.SetSessionRuntime(manager)

	runCtx, admission, finish, err := service.AdmitSubagentRun(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		"subagent:clean-end-abort-control",
		[]byte(`{"task":"abort a clean end"}`),
	)
	if err != nil {
		t.Fatalf("admit subagent run: %v", err)
	}
	adapter := native.NewSpawnAdapter(native.New(native.Deps{}))
	adapter.SetRunObserverFactory(func(ctx context.Context) native.SpawnRunObserver {
		publish := service.SubagentRunObserver(ctx)
		return func(event native.StreamEvent) native.SpawnRunObservation {
			if event.Type == native.EventAgentEnd {
				applied, abortErr := manager.AbortControl(
					context.WithoutCancel(ctx),
					lifecycleTestBotID,
					lifecycleTestSessionID,
					admission.RunID,
					"abort-clean-end",
				)
				if abortErr != nil || !applied {
					t.Fatalf("AbortControl() = (%t, %v), want (true, nil)", applied, abortErr)
				}
				backend.failNextLoad.Store(true)
			}
			return publish(event)
		}
	})
	terminalOutcome := tools.SpawnAttemptCompleted
	result, runErr := adapter.GenerateWithWatchdog(runCtx, tools.SpawnRunConfig{
		RunID: admission.RunID,
		Model: &sdk.Model{
			ID:       "clean-end-abort-model",
			Provider: abortAlignmentProvider{complete: true},
			Type:     sdk.ModelTypeChat,
		},
		Query: "abort as the run completes",
		Identity: tools.SpawnIdentity{
			BotID:      lifecycleTestBotID,
			SessionID:  lifecycleTestSessionID,
			IsSubagent: true,
		},
		ResolveCompletion: func() tools.SpawnAttemptDisposition {
			return tools.SpawnAttemptCompleted
		},
		ReconcileTerminal: func(outcome tools.SpawnAttemptDisposition) {
			terminalOutcome = outcome
		},
	}, func() {})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("spawn error = %v, want routed cancellation", runErr)
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("spawn result = %#v, want lifecycle snapshot", result)
	}
	if terminalOutcome != tools.SpawnAttemptAbort {
		t.Fatalf("reconciled terminal = %v, want abort", terminalOutcome)
	}
	if _, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID); !errors.Is(err, errInjectedTerminalLoad) {
		t.Fatalf("post-terminal Snapshot error = %v, want injected failure left unread by observer", err)
	}
	finish(tools.SubagentTerminal{
		ContextLifecycle: result.ContextLifecycle,
		OutcomeResolved:  true,
		Outcome:          terminalOutcome,
	})

	snapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusAborted {
		t.Fatalf("live run = %#v, want aborted terminal", snapshot.CurrentRunView)
	}
	durable, err := runs.Get(context.Background(), admission.RunID)
	if err != nil {
		t.Fatalf("durable run: %v", err)
	}
	if durable.State != ledger.StateAborted || durable.ErrorCode != "" {
		t.Fatalf("durable run = %#v, want aborted terminal", durable)
	}
	if len(lifecycles.terminalUpserts) != 1 || lifecycles.terminalUpserts[0].Status != contextLifecycleStatusAborted {
		t.Fatalf("lifecycle terminal = %#v, want aborted", lifecycles.terminalUpserts)
	}
}

package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

type abortAlignmentProvider struct {
	complete bool
}

func (abortAlignmentProvider) Name() string { return "abort-alignment" }

func (abortAlignmentProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (abortAlignmentProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (abortAlignmentProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (abortAlignmentProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	return &sdk.GenerateResult{FinishReason: sdk.FinishReasonStop}, nil
}

func (p abortAlignmentProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	parts := make(chan sdk.StreamPart, 7)
	parts <- &sdk.StartPart{}
	parts <- &sdk.StartStepPart{}
	if p.complete {
		parts <- &sdk.TextStartPart{ID: "completed"}
		parts <- &sdk.TextDeltaPart{ID: "completed", Text: "done"}
		parts <- &sdk.TextEndPart{ID: "completed"}
		parts <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
		parts <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
		close(parts)
		return &sdk.StreamResult{Stream: parts}, nil
	}
	parts <- &sdk.AbortPart{}
	close(parts)
	return &sdk.StreamResult{Stream: parts}, nil
}

type abortAlignmentFence struct{}

func (abortAlignmentFence) Activate(context.Context, string, string, int64) error { return nil }

type abortAlignmentLedger struct {
	mu    sync.Mutex
	run   ledger.Run
	token int64
}

var (
	_ sessionruntime.FenceActivator = abortAlignmentFence{}
	_ ledger.Store                  = (*abortAlignmentLedger)(nil)
)

func (s *abortAlignmentLedger) Admit(_ context.Context, params ledger.AdmitParams) (ledger.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != "" {
		if s.run.SessionID == params.SessionID && s.run.InvocationID == params.InvocationID {
			return s.run, false, nil
		}
		if s.run.SessionID == params.SessionID && s.run.State.Active() {
			return ledger.Run{}, false, ledger.ErrSessionBusy
		}
	}
	s.run = ledger.Run{
		RunID:            params.RunID,
		BotID:            params.BotID,
		SessionID:        params.SessionID,
		InvocationID:     params.InvocationID,
		TurnID:           params.TurnID,
		TurnPosition:     1,
		State:            ledger.StateAccepted,
		Input:            append([]byte(nil), params.Input...),
		InputFingerprint: params.InputFingerprint,
		CreatedAt:        time.Now(),
	}
	return s.run, true, nil
}

func (s *abortAlignmentLedger) Get(_ context.Context, runID string) (ledger.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != runID {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return s.run, nil
}

func (s *abortAlignmentLedger) GetByInvocation(_ context.Context, sessionID, invocationID string) (ledger.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.SessionID != sessionID || s.run.InvocationID != invocationID {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return s.run, nil
}

func (s *abortAlignmentLedger) ActiveRun(_ context.Context, sessionID string) (ledger.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.SessionID != sessionID || !s.run.State.Active() {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return s.run, nil
}

func (s *abortAlignmentLedger) LatestRun(_ context.Context, sessionID string) (ledger.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.SessionID != sessionID {
		return ledger.Run{}, ledger.ErrRunNotFound
	}
	return s.run, nil
}

func (s *abortAlignmentLedger) NextFencingToken(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token++
	return s.token, nil
}

func (s *abortAlignmentLedger) Claim(_ context.Context, params ledger.ClaimParams) (ledger.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != params.RunID || s.run.State != ledger.StateAccepted {
		return ledger.Run{}, false, nil
	}
	s.run.State = ledger.StateRunning
	s.run.OwnerID = params.OwnerID
	s.run.FencingToken = params.FencingToken
	s.run.LiveGeneration = params.LiveGeneration
	s.run.OwnerSince = time.Now()
	return s.run, true, nil
}

func (s *abortAlignmentLedger) SetWaitingDecision(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return s.transition(runID, token, ledger.StateWaitingDecision)
}

func (s *abortAlignmentLedger) Resume(_ context.Context, runID string, token int64) (ledger.Run, bool, error) {
	return s.transition(runID, token, ledger.StateRunning)
}

func (s *abortAlignmentLedger) transition(runID string, token int64, state ledger.State) (ledger.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != runID || s.run.FencingToken != token || s.run.State.Terminal() {
		return ledger.Run{}, false, nil
	}
	s.run.State = state
	return s.run, true, nil
}

func (s *abortAlignmentLedger) Finalize(_ context.Context, params ledger.FinalizeParams) (ledger.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != params.RunID || s.run.FencingToken != params.FencingToken || s.run.State.Terminal() {
		return s.run, false, nil
	}
	s.run.State = params.State
	s.run.ErrorCode = params.ErrorCode
	s.run.ErrorMessage = params.ErrorMessage
	s.run.UpdatedAt = time.Now()
	return s.run, true, nil
}

func (s *abortAlignmentLedger) RequestAbort(_ context.Context, runID string) (ledger.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.RunID != runID || s.run.State.Terminal() {
		return s.run, false, nil
	}
	s.run.AbortRequestedAt = time.Now()
	return s.run, true, nil
}

func (*abortAlignmentLedger) StaleGenerationRuns(context.Context, ledger.StaleGenerationQuery) ([]ledger.Run, error) {
	return nil, nil
}

func (*abortAlignmentLedger) OrphanedRuns(context.Context, ledger.OrphanQuery) ([]ledger.Run, error) {
	return nil, nil
}

func TestSpawnAbortAlignsManagerLedgerAndLifecycle(t *testing.T) {
	runs := &abortAlignmentLedger{}
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "abort-alignment-owner",
		OwnerLeaseTTL: time.Minute,
		Ledger:        runs,
		Fence:         abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	lifecycles := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: lifecycles}
	service.SetSessionRuntime(manager)

	ownerCtx, cancelOwner := context.WithCancelCause(context.Background())
	runCtx, admission, finish, err := service.AdmitSubagentRun(
		ownerCtx,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		"subagent:abort-alignment",
		[]byte(`{"task":"abort alignment"}`),
	)
	if err != nil {
		t.Fatalf("admit subagent run: %v", err)
	}
	adapter := native.NewSpawnAdapter(native.New(native.Deps{}))
	adapter.SetRunObserverFactory(service.SubagentRunObserver)
	result, runErr := adapter.GenerateWithWatchdog(runCtx, tools.SpawnRunConfig{
		RunID: admission.RunID,
		Model: &sdk.Model{
			ID:       "abort-alignment-model",
			Provider: abortAlignmentProvider{},
			Type:     sdk.ModelTypeChat,
		},
		Query: "abort internally",
		Identity: tools.SpawnIdentity{
			BotID:      lifecycleTestBotID,
			SessionID:  lifecycleTestSessionID,
			IsSubagent: true,
		},
	}, func() {})
	if runErr == nil || runErr.Error() != "agent run aborted" {
		t.Fatalf("spawn error = %v, want generic abort failure", runErr)
	}
	if result == nil || result.ContextLifecycle == nil {
		t.Fatalf("spawn result = %#v, want lifecycle snapshot", result)
	}
	cancelOwner(context.Canceled)
	finish(tools.SubagentTerminal{
		Cause:            runErr,
		ContextLifecycle: result.ContextLifecycle,
		OutcomeResolved:  true,
		Outcome:          tools.SpawnAttemptFailure,
	})

	snapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusErrored ||
		snapshot.CurrentRunView.Error != "agent run aborted" {
		t.Fatalf("live run = %#v, want generic errored terminal", snapshot.CurrentRunView)
	}
	durable, err := runs.Get(context.Background(), admission.RunID)
	if err != nil {
		t.Fatalf("durable run: %v", err)
	}
	if durable.State != ledger.StateFailed || durable.ErrorCode != "runtime_run_failed" {
		t.Fatalf("durable run = %#v, want failed terminal", durable)
	}
	if len(lifecycles.terminalUpserts) != 1 || lifecycles.terminalUpserts[0].Status != contextLifecycleStatusFailedProvider {
		t.Fatalf("lifecycle terminal = %#v, want failed_provider", lifecycles.terminalUpserts)
	}
	if errors.Is(runErr, context.Canceled) {
		t.Fatalf("internal abort was misclassified as owning cancellation: %v", runErr)
	}
}

func TestSpawnWatchdogRetryKeepsManagerRunActive(t *testing.T) {
	runs := &abortAlignmentLedger{}
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "watchdog-retry-owner",
		OwnerLeaseTTL: time.Minute,
		Ledger:        runs,
		Fence:         abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	lifecycles := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: lifecycles}
	service.SetSessionRuntime(manager)

	ownerCtx, cancelOwner := context.WithCancelCause(context.Background())
	runCtx, admission, finish, err := service.AdmitSubagentRun(
		ownerCtx,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		"subagent:watchdog-retry",
		[]byte(`{"task":"watchdog retry"}`),
	)
	if err != nil {
		t.Fatalf("admit subagent run: %v", err)
	}
	adapter := native.NewSpawnAdapter(native.New(native.Deps{}))
	adapter.SetRunObserverFactory(service.SubagentRunObserver)
	attemptCtx, cancelAttempt := context.WithCancelCause(runCtx)
	cancelAttempt(tools.ErrWatchdogTimedOut)
	first, firstErr := adapter.GenerateWithWatchdog(attemptCtx, tools.SpawnRunConfig{
		RunID: admission.RunID,
		Model: &sdk.Model{
			ID:       "watchdog-attempt-model",
			Provider: abortAlignmentProvider{},
			Type:     sdk.ModelTypeChat,
		},
		Query: "retry after the watchdog",
		Identity: tools.SpawnIdentity{
			BotID:      lifecycleTestBotID,
			SessionID:  lifecycleTestSessionID,
			IsSubagent: true,
		},
		Attempt:     1,
		MaxAttempts: 2,
		ResolveAttempt: func(attemptErr error) tools.SpawnAttemptDisposition {
			if errors.Is(attemptErr, tools.ErrWatchdogTimedOut) {
				return tools.SpawnAttemptRetry
			}
			return tools.SpawnAttemptFailure
		},
	}, func() {})
	if !errors.Is(firstErr, tools.ErrWatchdogTimedOut) {
		t.Fatalf("first attempt error = %v, want watchdog timeout", firstErr)
	}
	if first == nil || first.ContextLifecycle == nil {
		t.Fatalf("first attempt result = %#v, want lifecycle snapshot", first)
	}
	active, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
	if err != nil {
		t.Fatalf("runtime snapshot after retry: %v", err)
	}
	if active.CurrentRunView == nil || active.CurrentRunView.Status != sessionruntime.RunStatusRunning || active.CurrentRunView.Error != "" {
		t.Fatalf("live run after retry = %#v, want clean running state", active.CurrentRunView)
	}
	intermediate, err := runs.Get(context.Background(), admission.RunID)
	if err != nil {
		t.Fatalf("durable run after retry: %v", err)
	}
	if intermediate.State != ledger.StateRunning {
		t.Fatalf("durable run after retry = %#v, want running", intermediate)
	}

	final, finalErr := adapter.GenerateWithWatchdog(runCtx, tools.SpawnRunConfig{
		RunID: admission.RunID,
		Model: &sdk.Model{
			ID:       "watchdog-attempt-model",
			Provider: abortAlignmentProvider{complete: true},
			Type:     sdk.ModelTypeChat,
		},
		Query: "retry after the watchdog",
		Identity: tools.SpawnIdentity{
			BotID:      lifecycleTestBotID,
			SessionID:  lifecycleTestSessionID,
			IsSubagent: true,
		},
		Attempt:     2,
		MaxAttempts: 2,
	}, func() {})
	if finalErr != nil {
		t.Fatalf("final attempt: %v", finalErr)
	}
	if final == nil || final.ContextLifecycle == nil || final.Text != "done" {
		t.Fatalf("final attempt result = %#v, want completed output and lifecycle", final)
	}
	// The clean End is already the live terminal. A later owner cancellation
	// must not relabel durable or lifecycle state as aborted.
	cancelOwner(context.Canceled)
	finish(tools.SubagentTerminal{
		ContextLifecycle: final.ContextLifecycle,
		OutcomeResolved:  true,
		Outcome:          tools.SpawnAttemptCompleted,
	})

	snapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusCompleted {
		t.Fatalf("live run = %#v, want completed terminal", snapshot.CurrentRunView)
	}
	durable, err := runs.Get(context.Background(), admission.RunID)
	if err != nil {
		t.Fatalf("durable run: %v", err)
	}
	if durable.State != ledger.StateCompleted || durable.ErrorCode != "" {
		t.Fatalf("durable run = %#v, want completed terminal", durable)
	}
	if len(lifecycles.terminalUpserts) != 1 || lifecycles.terminalUpserts[0].Status != contextLifecycleStatusCompleted {
		t.Fatalf("lifecycle terminal = %#v, want completed", lifecycles.terminalUpserts)
	}
}

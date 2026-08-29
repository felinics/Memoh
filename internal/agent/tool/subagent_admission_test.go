package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

// fakeSubagentAdmitter stands in for the durable admission gate, and it
// enforces the one property this provider has to respect: a thread runs one
// turn at a time. Modelling that here rather than accepting everything is what
// makes a missing release visible — every queued-message test in this package
// runs through it, and a run that never frees its thread turns the next
// message into a refusal instead of a run.
type fakeSubagentAdmitter struct {
	mu       sync.Mutex
	reject   error
	active   map[string]bool
	starts   []subagentAdmissionRecord
	finishes []subagentTerminalRecord
}

type subagentAdmissionRecord struct {
	botID        string
	threadID     string
	runID        string
	invocationID string
	submission   string
}

type subagentTerminalRecord struct {
	threadID         string
	cause            string
	contextLifecycle *contextfrag.LifecycleSnapshot
}

func (f *fakeSubagentAdmitter) AdmitSubagentRun(ctx context.Context, botID, threadID, invocationID string, submission []byte) (context.Context, SubagentAdmission, func(SubagentTerminal), error) {
	f.mu.Lock()
	if f.reject != nil {
		err := f.reject
		f.mu.Unlock()
		return nil, SubagentAdmission{}, nil, err
	}
	if f.active[threadID] {
		f.mu.Unlock()
		return nil, SubagentAdmission{}, nil, fmt.Errorf("%w: thread %s", turn.ErrSessionBusy, threadID)
	}
	if f.active == nil {
		f.active = make(map[string]bool)
	}
	runID := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.starts)+1)
	f.active[threadID] = true
	f.starts = append(f.starts, subagentAdmissionRecord{
		botID:        botID,
		threadID:     threadID,
		runID:        runID,
		invocationID: invocationID,
		submission:   string(submission),
	})
	f.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	admission := SubagentAdmission{
		RunID:        runID,
		TurnID:       "turn-" + invocationID,
		TurnPosition: 1,
	}
	return runCtx, admission, func(terminal SubagentTerminal) {
		defer cancel()
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.active, threadID)
		record := subagentTerminalRecord{
			threadID:         threadID,
			contextLifecycle: terminal.ContextLifecycle,
		}
		if terminal.Cause != nil {
			record.cause = terminal.Cause.Error()
		}
		f.finishes = append(f.finishes, record)
	}, nil
}

func (f *fakeSubagentAdmitter) admissions() []subagentAdmissionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subagentAdmissionRecord(nil), f.starts...)
}

func (f *fakeSubagentAdmitter) terminals() []subagentTerminalRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subagentTerminalRecord(nil), f.finishes...)
}

func TestSpawnedTurnIsAdmittedOnTheAgentsOwnThread(t *testing.T) {
	admitter := &fakeSubagentAdmitter{}
	p, _, _, _ := newAgentControlProviderWithAdmitter(t, &fakeSpawnAgent{}, admitter)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	result := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "audit the ledger",
	}))

	childSessionID, _ := result["session_id"].(string)
	taskID, _ := result["task_id"].(string)
	admissions := admitter.admissions()
	if len(admissions) != 1 {
		t.Fatalf("admissions = %d, want 1", len(admissions))
	}
	admitted := admissions[0]
	// The slot belongs to the agent's own thread. Taking the parent's would stop
	// a parent from running several agents at once, which is the point of them.
	if admitted.threadID != childSessionID || admitted.threadID == session.SessionID {
		t.Errorf("admitted thread = %q, want child thread %q", admitted.threadID, childSessionID)
	}
	if admitted.botID != session.BotID {
		t.Errorf("admitted bot = %q, want %q", admitted.botID, session.BotID)
	}
	if want := "subagent:" + taskID; admitted.invocationID != want {
		t.Errorf("invocation id = %q, want %q", admitted.invocationID, want)
	}
	if !strings.Contains(admitted.submission, "audit the ledger") {
		t.Errorf("submission %q does not carry the message", admitted.submission)
	}
	terminals := admitter.terminals()
	if len(terminals) != 1 || terminals[0].threadID != childSessionID || terminals[0].cause != "" {
		t.Errorf("terminals = %#v, want one clean release of %q", terminals, childSessionID)
	}
}

type retryingIdentitySpawnAgent struct {
	mu       sync.Mutex
	calls    []SpawnRunConfig
	first    *contextfrag.LifecycleSnapshot
	terminal *contextfrag.LifecycleSnapshot
}

func (a *retryingIdentitySpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return a.GenerateWithWatchdog(ctx, cfg, func() {})
}

func (a *retryingIdentitySpawnAgent) GenerateWithWatchdog(_ context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, cfg)
	if len(a.calls) == 1 {
		return &SpawnResult{ContextLifecycle: a.first}, errors.New("provider returned 429")
	}
	return &SpawnResult{
		Text: "done",
		Messages: []sdk.Message{{
			Role:    sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.TextPart{Text: "done"}},
		}},
		ContextLifecycle: a.terminal,
	}, nil
}

func (a *retryingIdentitySpawnAgent) configs() []SpawnRunConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]SpawnRunConfig(nil), a.calls...)
}

func TestSpawnedTurnReusesAdmittedRunIDAcrossRetries(t *testing.T) {
	first := &contextfrag.LifecycleSnapshot{
		Version: 1,
		Counts:  contextfrag.ManifestCounts{Fragments: 1},
	}
	final := &contextfrag.LifecycleSnapshot{
		Version: 1,
		Counts:  contextfrag.ManifestCounts{Fragments: 2},
	}
	agent := &retryingIdentitySpawnAgent{first: first, terminal: final}
	admitter := &fakeSubagentAdmitter{}
	p := newSubagentAdmissionTestProvider(t, agent, admitter)

	result := asMap(t, mustExecuteAgentTool(t, p, SessionContext{
		BotID:     "bot1",
		SessionID: "parent1",
	}, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "retry once",
	}))
	if result["status"] != string(background.TaskCompleted) {
		t.Fatalf("status = %v, want %q", result["status"], background.TaskCompleted)
	}

	admissions := admitter.admissions()
	if len(admissions) != 1 {
		t.Fatalf("admissions = %d, want 1", len(admissions))
	}
	calls := agent.configs()
	if len(calls) != 2 {
		t.Fatalf("agent calls = %d, want 2", len(calls))
	}
	for i, call := range calls {
		if call.RunID != admissions[0].runID {
			t.Errorf("call %d RunID = %q, want admitted RunID %q", i, call.RunID, admissions[0].runID)
		}
	}
	terminals := admitter.terminals()
	if len(terminals) != 1 {
		t.Fatalf("terminal calls = %d, want 1", len(terminals))
	}
	if got := terminals[0].contextLifecycle; got == nil || got.Counts.Fragments != 2 || got.AssistantMessageID != "msg_2" {
		t.Fatalf("terminal snapshot = %#v, want final retry snapshot associated with msg_2", got)
	}
}

type failedLifecycleSpawnAgent struct {
	snapshot *contextfrag.LifecycleSnapshot
}

func (a *failedLifecycleSpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return a.GenerateWithWatchdog(ctx, cfg, func() {})
}

func (a *failedLifecycleSpawnAgent) GenerateWithWatchdog(context.Context, SpawnRunConfig, func()) (*SpawnResult, error) {
	return &SpawnResult{ContextLifecycle: a.snapshot}, errors.New("provider unavailable")
}

func TestSpawnedTurnRetainsLifecycleSnapshotOnGenerationFailure(t *testing.T) {
	snapshot := &contextfrag.LifecycleSnapshot{
		Version: 1,
		Counts:  contextfrag.ManifestCounts{Fragments: 3, Messages: 2},
	}
	admitter := &fakeSubagentAdmitter{}
	p := newSubagentAdmissionTestProvider(t, &failedLifecycleSpawnAgent{snapshot: snapshot}, admitter)

	result := asMap(t, mustExecuteAgentTool(t, p, SessionContext{
		BotID:     "bot1",
		SessionID: "parent1",
	}, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "fail after assembly",
	}))
	if result["status"] != string(background.TaskFailed) {
		t.Fatalf("status = %v, want %q", result["status"], background.TaskFailed)
	}
	terminals := admitter.terminals()
	if len(terminals) != 1 {
		t.Fatalf("terminal calls = %d, want 1", len(terminals))
	}
	if !reflect.DeepEqual(terminals[0].contextLifecycle, snapshot) {
		t.Fatalf("terminal snapshot = %#v, want %#v", terminals[0].contextLifecycle, snapshot)
	}
	if terminals[0].cause != "provider unavailable" {
		t.Fatalf("terminal cause = %q, want provider failure", terminals[0].cause)
	}
}

type abortingSpawnAgent struct {
	calls    int
	snapshot *contextfrag.LifecycleSnapshot
}

func (a *abortingSpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return a.GenerateWithWatchdog(ctx, cfg, func() {})
}

func (a *abortingSpawnAgent) GenerateWithWatchdog(context.Context, SpawnRunConfig, func()) (*SpawnResult, error) {
	a.calls++
	return &SpawnResult{ContextLifecycle: a.snapshot}, errors.New("agent run aborted")
}

func TestSpawnedTurnDoesNotCompleteAdmittedRunAfterAgentAbort(t *testing.T) {
	snapshot := &contextfrag.LifecycleSnapshot{
		Version: 1,
		Counts:  contextfrag.ManifestCounts{Fragments: 3, Messages: 2},
	}
	agent := &abortingSpawnAgent{snapshot: snapshot}
	admitter := &fakeSubagentAdmitter{}
	p := newSubagentAdmissionTestProvider(t, agent, admitter)

	result := asMap(t, mustExecuteAgentTool(t, p, SessionContext{
		BotID:     "bot1",
		SessionID: "parent1",
	}, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "abort internally",
	}))
	if result["status"] != string(background.TaskFailed) {
		t.Fatalf("status = %v, want %q", result["status"], background.TaskFailed)
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls = %d, want one non-retryable attempt", agent.calls)
	}
	terminals := admitter.terminals()
	if len(terminals) != 1 || terminals[0].cause != "agent run aborted" {
		t.Fatalf("terminals = %#v, want one aborted-cause terminal", terminals)
	}
	if !reflect.DeepEqual(terminals[0].contextLifecycle, snapshot) {
		t.Fatalf("terminal snapshot = %#v, want %#v", terminals[0].contextLifecycle, snapshot)
	}
}

type canceledSubagentSpawnAgent struct{}

func (*canceledSubagentSpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return (&canceledSubagentSpawnAgent{}).GenerateWithWatchdog(ctx, cfg, func() {})
}

func (*canceledSubagentSpawnAgent) GenerateWithWatchdog(
	ctx context.Context,
	_ SpawnRunConfig,
	_ func(),
) (*SpawnResult, error) {
	return nil, context.Cause(ctx)
}

func TestRunSubagentTaskPreservesOwningCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)
	p := &SpawnProvider{
		agent:  &canceledSubagentSpawnAgent{},
		logger: slog.New(slog.DiscardHandler),
		modelResolver: func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
			return resolvedSubagentModel{}, nil
		},
	}

	result := p.runSubagentTask(ctx, &agentRequest{
		taskID:         "task-aborted",
		agentID:        "worker",
		agentSessionID: "session-aborted",
		message:        "work",
		parentSession:  SessionContext{BotID: "bot-1"},
	})

	if !errors.Is(result.Cause, context.Canceled) {
		t.Fatalf("terminal cause = %v, want owning context cancellation", result.Cause)
	}
}

func TestRunSubagentTaskKeepsResolvedFailureWhenCancellationRaces(t *testing.T) {
	providerErr := errors.New("provider failed before cancellation")
	ctx, cancel := context.WithCancelCause(context.Background())
	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
			if cfg.ResolveAttempt == nil {
				t.Fatal("attempt resolver is nil")
			}
			if got := cfg.ResolveAttempt(providerErr); got != SpawnAttemptFailure {
				t.Fatalf("attempt disposition = %v, want failure", got)
			}
			cancel(context.Canceled)
			return nil, providerErr
		},
	}
	p := &SpawnProvider{
		agent:  agent,
		logger: slog.New(slog.DiscardHandler),
		modelResolver: func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
			return resolvedSubagentModel{}, nil
		},
	}

	result := p.runSubagentTask(ctx, &agentRequest{
		taskID:         "task-failure-race",
		agentID:        "worker",
		agentSessionID: "session-failure-race",
		message:        "work",
		parentSession:  SessionContext{BotID: "bot-1"},
	})

	if !result.AttemptResolved || result.AttemptOutcome != SpawnAttemptFailure {
		t.Fatalf("attempt outcome = resolved %v disposition %v, want failure", result.AttemptResolved, result.AttemptOutcome)
	}
	if !errors.Is(result.Cause, providerErr) || errors.Is(result.Cause, context.Canceled) {
		t.Fatalf("terminal cause = %v, want resolved provider failure", result.Cause)
	}
}

func newSubagentAdmissionTestProvider(t *testing.T, agent SpawnAgent, admitter SubagentAdmitter) *SpawnProvider {
	t.Helper()
	p := NewSpawnProvider(nil, nil, nil, nil, nil, background.New(nil))
	p.sessionService = &fakeAgentSessionService{}
	p.SetAgent(agent)
	p.SetMessageService(newFakeAgentMessageService())
	p.SetSubagentAdmitter(admitter)
	p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
		return resolvedSubagentModel{
			Model:            &sdk.Model{},
			UUID:             "00000000-0000-0000-0000-000000000123",
			ModelID:          "test-model",
			ProviderName:     "test-provider",
			SupportsToolCall: true,
		}, nil
	}
	return p
}

func TestBusyAgentThreadIsReportedToTheParentAndRunsNothing(t *testing.T) {
	agent := &fakeSpawnAgent{}
	admitter := &fakeSubagentAdmitter{reject: fmt.Errorf("%w: thread child_1", turn.ErrSessionBusy)}
	p, mgr, _, _ := newAgentControlProviderWithAdmitter(t, agent, admitter)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	result := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "audit the ledger",
	}))

	// Busy is ordinary traffic, not a stream failure: the parent model is told
	// what to do about it rather than handed a runtime sentinel.
	message, _ := result["error"].(string)
	if !strings.Contains(message, "already running") {
		t.Errorf("error = %q, want the busy remedy", message)
	}
	if result["status"] != string(background.TaskFailed) {
		t.Errorf("status = %v, want %v", result["status"], background.TaskFailed)
	}
	if calls := agent.queries(); len(calls) != 0 {
		t.Errorf("refused turn still ran the agent: %v", calls)
	}
	if len(admitter.terminals()) != 0 {
		t.Errorf("refused turn wrote a terminal state: %#v", admitter.terminals())
	}
	// The task record has to close, or a caller waiting on it waits forever.
	taskID, _ := result["task_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, session.BotID, session.SessionID, taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != background.TaskFailed {
		t.Errorf("task status = %v, want %v", snap.Status, background.TaskFailed)
	}
}

func TestQueuedAgentMessageIsAdmittedAfterTheRunningOneReleasesTheThread(t *testing.T) {
	block := make(chan struct{})
	agent := &fakeSpawnAgent{block: block}
	admitter := &fakeSubagentAdmitter{}
	p, _, _, _ := newAgentControlProviderWithAdmitter(t, agent, admitter)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "first",
		"run_in_background": true,
	})
	mustExecuteAgentTool(t, p, session, "send_message", map[string]any{"id": "worker", "message": "second"})

	close(block)
	waitUntil(t, 2*time.Second, func() bool {
		return len(admitter.terminals()) == 2
	})

	// Both ran, so the first run released the thread. A release that never
	// happens leaves the gate holding the slot and the queued message is
	// refused rather than run — which is the failure this guards.
	if queries := agent.queries(); len(queries) != 2 {
		t.Fatalf("agent ran %v, want both messages", queries)
	}
	admissions := admitter.admissions()
	if len(admissions) != 2 {
		t.Fatalf("admissions = %#v, want two", admissions)
	}
	if admissions[0].invocationID == admissions[1].invocationID {
		t.Errorf("queued message reused the running task's invocation id: %q", admissions[0].invocationID)
	}
	if admissions[0].threadID != admissions[1].threadID {
		t.Errorf("queued message ran on a different thread: %#v", admissions)
	}
}

package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/background"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
)

func newStepPersistProvider(t *testing.T, agent SpawnAgent, admitter SubagentAdmitter) (*SpawnProvider, *fakeAgentMessageService) {
	t.Helper()
	mgr := background.New(nil)
	messageSvc := newFakeAgentMessageService()
	p := NewSpawnProvider(nil, nil, nil, nil, nil, mgr)
	p.sessionService = &fakeAgentSessionService{}
	p.SetAgent(agent)
	p.SetMessageService(messageSvc)
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
	return p, messageSvc
}

// TestSubagentDoesNotRetryAfterPersistedStep: once a step has durably
// committed, a retryable error must surface instead of restarting the turn —
// the committed step may carry real side effects that a replay would repeat.
func TestSubagentDoesNotRetryAfterPersistedStep(t *testing.T) {
	t.Parallel()

	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
			if cfg.OnStepPersisted != nil {
				cfg.OnStepPersisted()
			}
			return nil, errors.New("api error 500: provider fell over mid-run")
		},
	}
	p, _ := newStepPersistProvider(t, agent, &fakeSubagentAdmitter{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	result := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "do the work",
	}))

	if got := agent.generateCount.Load(); got != 1 {
		t.Fatalf("agent ran %d times, want exactly 1 (no retry after a persisted step)", got)
	}
	if result["status"] != string(background.TaskFailed) {
		t.Fatalf("status = %v, want failed", result["status"])
	}
	errText, _ := result["error"].(string)
	if !strings.Contains(errText, "send a follow-up message to continue") {
		t.Fatalf("error does not tell the parent how to continue: %q", errText)
	}
}

// TestSubagentStillRetriesBeforeAnyPersistedStep: the pre-commit retry
// behavior is unchanged — a retryable failure with no durable output restarts
// the attempt.
func TestSubagentStillRetriesBeforeAnyPersistedStep(t *testing.T) {
	t.Parallel()

	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			return nil, errors.New("api error 500: transient")
		},
	}
	agent.generateFunc = func(_ context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
		if agent.generateCount.Load() == 1 {
			return nil, errors.New("api error 500: transient")
		}
		return &SpawnResult{Text: "recovered"}, nil
	}
	p, _ := newStepPersistProvider(t, agent, &fakeSubagentAdmitter{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	result := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "do the work",
	}))

	if got := agent.generateCount.Load(); got != 2 {
		t.Fatalf("agent ran %d times, want 2 (one retry)", got)
	}
	if result["status"] != string(background.TaskCompleted) {
		t.Fatalf("status = %v, want completed", result["status"])
	}
	if result["text"] != "recovered" {
		t.Fatalf("text = %v, want recovered", result["text"])
	}
}

// TestSubagentSkipsTerminalPersistWhenStepsOwnHistory: a result marked
// Persisted means incremental persistence already wrote every step, so the
// terminal snapshot must not write the messages again.
func TestSubagentSkipsTerminalPersistWhenStepsOwnHistory(t *testing.T) {
	t.Parallel()

	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			return &SpawnResult{
				Text: "done",
				Messages: []sdk.Message{
					sdk.AssistantMessage("done"),
				},
				Persisted: true,
			}, nil
		},
	}
	p, messageSvc := newStepPersistProvider(t, agent, &fakeSubagentAdmitter{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	result := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "do the work",
	}))
	if result["status"] != string(background.TaskCompleted) {
		t.Fatalf("status = %v, want completed", result["status"])
	}

	childSessionID, _ := result["session_id"].(string)
	msgs, err := messageSvc.ListBySession(context.Background(), childSessionID)
	if err != nil {
		t.Fatalf("list session messages: %v", err)
	}
	// Only the pre-run user message may be present; the assistant output was
	// declared persisted by the step committer and must not be written twice.
	for _, msg := range msgs {
		if msg.Role != "user" {
			t.Fatalf("terminal persist wrote a %s message despite Persisted=true", msg.Role)
		}
	}
}

// TestSubagentMessagesFileUnderAdmittedTurn: the task's user message becomes
// the admitted turn's request row (TurnID + TurnPosition), and the assistant
// rows bind to it via TurnRequestMessageID — one history turn per task, the
// same turn the runtime view names, so a mid-stream reader sees one loop.
func TestSubagentMessagesFileUnderAdmittedTurn(t *testing.T) {
	t.Parallel()

	agent := &mockSpawnAgent{
		generateFunc: func(_ context.Context, _ SpawnRunConfig, _ func()) (*SpawnResult, error) {
			return &SpawnResult{
				Text:     "done",
				Messages: []sdk.Message{sdk.AssistantMessage("done")},
			}, nil
		},
	}
	p, messageSvc := newStepPersistProvider(t, agent, &fakeSubagentAdmitter{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "do the work",
	})

	inputs := messageSvc.persistInputs()
	var userInput, assistantInput *messagepkg.PersistInput
	for i := range inputs {
		switch inputs[i].Role {
		case "user":
			userInput = &inputs[i]
		case "assistant":
			assistantInput = &inputs[i]
		}
	}
	if userInput == nil || assistantInput == nil {
		t.Fatalf("expected user and assistant rows, got %d inputs", len(inputs))
	}
	if userInput.TurnID == "" || userInput.TurnPosition == nil {
		t.Fatalf("user row lacks the admitted turn identity: %+v", userInput)
	}
	if userInput.RunID == "" {
		t.Fatal("user row lacks the run id")
	}
	if assistantInput.TurnID != "" || assistantInput.TurnPosition != nil {
		t.Fatal("assistant row must not mint a turn of its own")
	}
	if assistantInput.TurnRequestMessageID == "" {
		t.Fatal("assistant row does not bind to the task's request message")
	}
	if assistantInput.RunID != userInput.RunID {
		t.Fatalf("run ids diverge: user %q assistant %q", userInput.RunID, assistantInput.RunID)
	}
}

// abortableSubagentAdmitter hands the test the run context's cancel so it can
// abort the child run the way a WS abort would — cancelling the run context
// while the parent task context stays alive.
type abortableSubagentAdmitter struct {
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	admitted chan string
}

func newAbortableSubagentAdmitter() *abortableSubagentAdmitter {
	return &abortableSubagentAdmitter{
		cancels:  make(map[string]context.CancelFunc),
		admitted: make(chan string, 4),
	}
}

func (f *abortableSubagentAdmitter) AdmitSubagentRun(ctx context.Context, _, threadID, invocationID string, _ []byte) (context.Context, SubagentAdmission, func(error), error) {
	runCtx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	f.cancels[threadID] = cancel
	f.mu.Unlock()
	f.admitted <- threadID
	return runCtx, SubagentAdmission{
		RunID:        "run-" + invocationID,
		TurnID:       "turn-" + invocationID,
		TurnPosition: 1,
	}, func(error) {}, nil
}

func (f *abortableSubagentAdmitter) abortRun(threadID string) bool {
	f.mu.Lock()
	cancel := f.cancels[threadID]
	f.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// TestSubagentAbortedRunReportsKilled: cancelling the child's run context
// while the parent task is still alive (the stop control on the subagent
// session) records the task as killed, not failed.
func TestSubagentAbortedRunReportsKilled(t *testing.T) {
	t.Parallel()

	var touched atomic.Bool
	agent := &mockSpawnAgent{
		generateFunc: func(ctx context.Context, _ SpawnRunConfig, touchFn func()) (*SpawnResult, error) {
			touched.Store(true)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					touchFn()
				case <-ctx.Done():
					return nil, context.Cause(ctx)
				}
			}
		},
	}
	admitter := newAbortableSubagentAdmitter()
	p, _ := newStepPersistProvider(t, agent, admitter)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	resultCh := make(chan map[string]any, 1)
	go func() {
		resultCh <- asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
			"id":   "worker",
			"task": "run until stopped",
		}))
	}()

	var threadID string
	select {
	case threadID = <-admitter.admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("run was never admitted")
	}
	waitUntil(t, 2*time.Second, touched.Load)
	if !admitter.abortRun(threadID) {
		t.Fatal("no cancel recorded for admitted run")
	}

	var result map[string]any
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("spawn did not return after abort")
	}
	if result["status"] != string(background.TaskKilled) {
		t.Fatalf("status = %v, want killed", result["status"])
	}
	if errText, _ := result["error"].(string); errText != "stopped by the user" {
		t.Fatalf("error = %q, want %q", errText, "stopped by the user")
	}
}

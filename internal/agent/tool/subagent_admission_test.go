package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/background"
	"github.com/memohai/memoh/internal/agent/turn"
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
	invocationID string
	submission   string
}

type subagentTerminalRecord struct {
	threadID string
	cause    string
}

func (f *fakeSubagentAdmitter) AdmitSubagentRun(ctx context.Context, botID, threadID, invocationID string, submission []byte) (context.Context, SubagentAdmission, func(error), error) {
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
	f.active[threadID] = true
	f.starts = append(f.starts, subagentAdmissionRecord{
		botID:        botID,
		threadID:     threadID,
		invocationID: invocationID,
		submission:   string(submission),
	})
	f.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	admission := SubagentAdmission{
		RunID:        "run-" + invocationID,
		TurnID:       "turn-" + invocationID,
		TurnPosition: 1,
	}
	return runCtx, admission, func(cause error) {
		defer cancel()
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.active, threadID)
		record := subagentTerminalRecord{threadID: threadID}
		if cause != nil {
			record.cause = cause.Error()
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

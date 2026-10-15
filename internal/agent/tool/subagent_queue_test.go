package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

type busySubagentRuntime struct {
	SubagentAdmitter
	done chan struct{}
}

type rejectingSubagentRuntime struct {
	*subagentRuntimeStub
	attempts atomic.Int32
}

type unconfirmedBeforeAdmission struct{ *subagentRuntimeStub }

func (*unconfirmedBeforeAdmission) WaitSubagentIdle(context.Context, SessionContext, string) error {
	return apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil)
}

func TestUnconfirmedRejectionBeforeAdmissionReleasesReservation(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &externalSubagentStub{}
	p.SetExternalSubagents(external)
	p.SetSubagentAdmitter(&unconfirmedBeforeAdmission{&subagentRuntimeStub{SubagentAdmitter: p.admitter, state: SubagentRuntimeState{Status: "idle"}}})
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	value := mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"bot_agent_id": "external-1", "task": "work"})
	if asMap(t, value)["status"] != "failed" || len(external.runs) != 0 {
		t.Fatalf("unexpected execution: %v", value)
	}
	p.coord.mu.Lock()
	reserved := len(p.coord.externalRunning[parent.BotID])
	p.coord.mu.Unlock()
	if reserved != 0 {
		t.Fatalf("unexecuted request retained %d execution reservations", reserved)
	}
}

func (r *rejectingSubagentRuntime) AdmitSubagentRun(context.Context, string, string, string, []byte) (context.Context, SubagentAdmission, func(SubagentTerminal), error) {
	r.attempts.Add(1)
	return nil, SubagentAdmission{}, nil, turn.ErrSessionBusy
}

func TestSubagentAdmissionRetriesOnlyBusyAndRemainBounded(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, _, _ := newAgentControlProvider(t, agent)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first"})
	runtime := &rejectingSubagentRuntime{subagentRuntimeStub: &subagentRuntimeStub{SubagentAdmitter: p.admitter, state: SubagentRuntimeState{Status: "idle"}}}
	p.SetSubagentAdmitter(runtime)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := p.execSendMessage(ctx, parent, sendMessageArgs{ID: "worker", Message: "must not execute"})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(t, value)["status"] != "failed" || runtime.attempts.Load() != 3 || len(agent.queries()) != 1 {
		t.Fatalf("result=%v attempts=%d queries=%v", value, runtime.attempts.Load(), agent.queries())
	}
}

func TestSubagentBudgetDefaultsAndPreservesParentDeadline(t *testing.T) {
	start := time.Now()
	ctx, cancel := boundedAgentTaskContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(start.Add(time.Duration(background.BackgroundExecTimeout)*time.Second)) || deadline.After(time.Now().Add(time.Duration(background.BackgroundExecTimeout)*time.Second)) {
		t.Fatalf("default deadline=%v", deadline)
	}
	parent, parentCancel := context.WithDeadline(context.Background(), start.Add(time.Minute))
	defer parentCancel()
	child, childCancel := boundedAgentTaskContext(parent)
	defer childCancel()
	deadline, _ = child.Deadline()
	if !deadline.Equal(start.Add(time.Minute)) {
		t.Fatalf("parent deadline was reset: %v", deadline)
	}
}

type blockingExternalSubagent struct {
	externalSubagentStub
	release chan struct{}
	active  atomic.Int32
}

func (s *blockingExternalSubagent) RunExternalSubagent(ctx context.Context, _ ExternalSubagentRun) (*SpawnResult, error) {
	s.active.Add(1)
	defer s.active.Add(-1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return &SpawnResult{Text: "done", Persisted: true}, nil
	}
}

func TestExternalSubagentsBoundConcurrencyAndPendingMessages(t *testing.T) {
	p, manager, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &blockingExternalSubagent{release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-external.release:
		default:
			close(external.release)
		}
	})
	p.SetExternalSubagents(external)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	var running []string
	for i := 0; i < 3; i++ {
		spawnParent := parent
		if i == 2 {
			spawnParent.SessionID = "parent2"
		}
		result := asMap(t, mustExecuteAgentTool(t, p, spawnParent, "spawn_agent", map[string]any{"id": fmt.Sprintf("worker%d", i), "bot_agent_id": "external-1", "task": "work", "run_in_background": true}))
		running = append(running, result["task_id"].(string))
	}
	waitUntil(t, time.Second, func() bool { return external.active.Load() == 3 })
	if _, err := executeAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "overflow", "bot_agent_id": "external-1", "task": "work", "run_in_background": true}); err == nil {
		t.Fatal("fourth concurrent external child accepted")
	}
	var queued []string
	for i := 0; i < 16; i++ {
		result := asMap(t, mustExecuteAgentTool(t, p, parent, "send_message", map[string]any{"id": "worker0", "message": fmt.Sprintf("queued %d", i), "run_in_background": true}))
		if result["status"] != "queued" {
			t.Fatalf("queue=%v", result)
		}
		queued = append(queued, result["task_id"].(string))
	}
	if _, err := executeAgentTool(t, p, parent, "send_message", map[string]any{"id": "worker0", "message": "overflow", "run_in_background": true}); err == nil {
		t.Fatal("seventeenth pending message accepted")
	}
	for _, id := range queued {
		if err := manager.Kill(id); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, time.Second, func() bool {
		return len(p.coord.snapshot(parent.BotID, parent.SessionID, "worker0").QueuedTaskIDs) == 0
	})
	close(external.release)
	for _, id := range running {
		waitUntil(t, time.Second, func() bool { return manager.Get(id).Snapshot().Status == background.TaskCompleted })
	}
	p.coord.mu.Lock()
	slots := len(p.coord.externalRunning[parent.BotID])
	p.coord.mu.Unlock()
	if slots != 0 {
		t.Fatalf("external execution slots leaked: %d", slots)
	}
}

func TestUnknownExternalTerminationKeepsExecutionQuota(t *testing.T) {
	p, manager, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &externalSubagentStub{err: apperror.New(apperror.CodeExternalRuntimeTerminationUnconfirmed, nil), executionUnconfirmed: true}
	p.SetExternalSubagents(external)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	for i := range 3 {
		caller := parent
		if i == 2 {
			caller.SessionID = "parent2"
		}
		result := asMap(t, mustExecuteAgentTool(t, p, caller, "spawn_agent", map[string]any{"id": fmt.Sprintf("worker%d", i), "bot_agent_id": "external-1", "task": "work"}))
		if result["status"] != "failed" || result["code"] != string(apperror.CodeExternalRuntimeTerminationUnconfirmed) || result["text"] != "external result" {
			t.Fatalf("uncertain termination hid the terminal result: %v", result)
		}
		if task := manager.Get(result["task_id"].(string)); task == nil || task.Snapshot().Status != background.TaskFailed {
			t.Fatalf("uncertain task has no terminal background record: %v", result)
		}
		status, err := NewBackgroundProvider(nil, manager).execGetBackgroundStatus(t.Context(), caller, taskArgs{TaskID: result["task_id"].(string)})
		if err != nil || asMap(t, status)["code"] != string(apperror.CodeExternalRuntimeTerminationUnconfirmed) || asMap(t, status)["result"] != "external result" {
			t.Fatalf("uncertain task cannot be inspected: status=%v err=%v", status, err)
		}
	}
	if _, err := executeAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "overflow", "bot_agent_id": "external-1", "task": "work"}); err == nil {
		t.Fatal("unknown process exits released all three occupied execution slots")
	}
	if len(external.runs) != 3 {
		t.Fatalf("quota overflow executed another child: %d", len(external.runs))
	}
	restarted, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	restarted.SetExternalSubagents(external)
	if _, err := executeAgentTool(t, restarted, parent, "spawn_agent", map[string]any{"id": "after_restart", "bot_agent_id": "external-1", "task": "work"}); err == nil {
		t.Fatal("a fresh coordinator forgot three durable unconfirmed executions")
	}
	external.resolveHeld()
	external.executionUnconfirmed, external.err = false, nil
	result := asMap(t, mustExecuteAgentTool(t, restarted, parent, "spawn_agent", map[string]any{"id": "after_resolution", "bot_agent_id": "external-1", "task": "work"}))
	if result["status"] != "completed" || len(external.runs) != 4 {
		t.Fatalf("resolved executions still hold capacity: %v runs=%d", result, len(external.runs))
	}
}

func TestSlowSettlementDoesNotHoldBackCompletions(t *testing.T) {
	p, manager, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &externalSubagentStub{running: make(chan struct{})}
	p.SetExternalSubagents(external)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	first := asMap(t, mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "bot_agent_id": "external-1", "task": "work", "run_in_background": true}))
	waitUntil(t, time.Second, func() bool {
		external.mu.Lock()
		defer external.mu.Unlock()
		return len(external.runs) == 1
	})
	external.settling = make(chan struct{})
	defer close(external.settling)
	go func() {
		_, _ = executeAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "second", "bot_agent_id": "external-1", "task": "work", "run_in_background": true})
	}()
	time.Sleep(50 * time.Millisecond)
	close(external.running)
	waitUntil(t, time.Second, func() bool {
		return manager.Get(first["task_id"].(string)).Snapshot().Status == background.TaskCompleted
	})
}

func TestExternalSlotsCountEveryRequestOfAChild(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	first := &agentRequest{runtimeType: "codex", agentSessionID: "child", parentSession: parent}
	second := &agentRequest{runtimeType: "codex", agentSessionID: "child", parentSession: parent}
	for _, req := range []*agentRequest{first, second} {
		if err := p.reserveExternalSlotLocked(req, nil); err != nil {
			t.Fatal(err)
		}
	}
	p.releaseExternalSlotLocked(first)
	for i := range background.MaxRunningSpawnTasks - 1 {
		if err := p.reserveExternalSlotLocked(&agentRequest{runtimeType: "codex", agentSessionID: fmt.Sprintf("other%d", i), parentSession: parent}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.reserveExternalSlotLocked(&agentRequest{runtimeType: "codex", agentSessionID: "overflow", parentSession: parent}, nil); err == nil {
		t.Fatal("releasing one request of a child freed the slot its other request still uses")
	}
}

func TestLocalSubagentQueueKeepsSubmissionDeadline(t *testing.T) {
	agent := &fakeSpawnAgent{block: make(chan struct{})}
	p, manager, _, _ := newAgentControlProvider(t, agent)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	first := asMap(t, mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first", "run_in_background": true}))
	waitUntil(t, time.Second, func() bool { return len(agent.queries()) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	value, err := p.execSendMessage(ctx, parent, sendMessageArgs{ID: "worker", Message: "expired", RunInBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	queued := asMap(t, value)
	id := queued["task_id"].(string)
	waitUntil(t, time.Second, func() bool { return manager.Get(id).Snapshot().Status == background.TaskFailed })
	close(agent.block)
	waitUntil(t, time.Second, func() bool {
		return manager.Get(first["task_id"].(string)).Snapshot().Status == background.TaskCompleted
	})
	if len(agent.queries()) != 1 {
		t.Fatalf("expired message executed after promotion: %v", agent.queries())
	}
}

func (s *busySubagentRuntime) SnapshotSubagent(context.Context, SessionContext, string) (SubagentRuntimeState, error) {
	select {
	case <-s.done:
		return SubagentRuntimeState{RunID: "prior-run", Status: "completed"}, nil
	default:
		return SubagentRuntimeState{RunID: "prior-run", Status: "running", Active: true}, nil
	}
}

func (s *busySubagentRuntime) WaitSubagentIdle(ctx context.Context, _ SessionContext, _ string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return nil
	}
}

func (*busySubagentRuntime) ControlSubagent(context.Context, SubagentControlInput) (SubagentControlResult, error) {
	return SubagentControlResult{}, nil
}

func TestFreshCoordinatorQueuesUntilDurableRunFinishes(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, manager, _, _ := newAgentControlProvider(t, agent)
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first"})
	runtime := &busySubagentRuntime{SubagentAdmitter: p.admitter, done: make(chan struct{})}
	p.SetSubagentAdmitter(runtime)
	p.coord = newAgentCoordinator()
	value, err := p.execSendMessage(context.Background(), parent, sendMessageArgs{ID: "worker", Message: "after durable run", RunInBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	queued := asMap(t, value)
	if queued["status"] != "queued" {
		t.Fatalf("busy durable child = %v", queued)
	}
	taskID := queued["task_id"].(string)
	if manager.Get(taskID).Snapshot().Status != background.TaskQueued || len(agent.queries()) != 1 {
		t.Fatal("queued message ran before durable terminal")
	}
	close(runtime.done)
	waitUntil(t, time.Second, func() bool { return manager.Get(taskID).Snapshot().Status == background.TaskCompleted })
	if got := agent.queries(); len(got) != 2 || got[1] != "after durable run" {
		t.Fatalf("queries = %v", got)
	}
}

func TestDurableQueueDeadlineAndCancellationRemovePendingWork(t *testing.T) {
	for _, stop := range []string{"deadline", "kill"} {
		t.Run(stop, func(t *testing.T) {
			agent := &fakeSpawnAgent{}
			p, manager, _, _ := newAgentControlProvider(t, agent)
			parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
			mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first"})
			runtime := &busySubagentRuntime{SubagentAdmitter: p.admitter, done: make(chan struct{})}
			p.SetSubagentAdmitter(runtime)
			p.coord = newAgentCoordinator()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			value, err := p.execSendMessage(ctx, parent, sendMessageArgs{ID: "worker", Message: "must not execute", RunInBackground: true})
			if err != nil {
				t.Fatal(err)
			}
			queued := asMap(t, value)
			if queued["status"] != "queued" {
				t.Fatalf("queued=%v", queued)
			}
			taskID := queued["task_id"].(string)
			if stop == "kill" {
				if err := manager.Kill(taskID); err != nil {
					t.Fatal(err)
				}
			}
			waitUntil(t, time.Second, func() bool {
				state := manager.Get(taskID).Snapshot().Status
				return state == background.TaskFailed || state == background.TaskKilled
			})
			close(runtime.done)
			waitUntil(t, time.Second, func() bool {
				snap := p.coord.snapshot(parent.BotID, parent.SessionID, "worker")
				return snap.RunningTaskID == "" && len(snap.QueuedTaskIDs) == 0
			})
			if len(agent.queries()) != 1 {
				t.Fatalf("expired queued work ran: %v", agent.queries())
			}
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type failingIdleRuntime struct{ *subagentRuntimeStub }

func (*failingIdleRuntime) WaitSubagentIdle(context.Context, SessionContext, string) error {
	return errors.New("PRIVATE_DIAGNOSTIC_CANARY")
}

func TestQueuedSubagentRequestLogsWhyItNeverStarted(t *testing.T) {
	p, manager, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	var logs lockedBuffer
	p.logger = slog.New(slog.NewTextHandler(&logs, nil))
	parent := SessionContext{BotID: "bot1", SessionID: "parent1"}
	mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first"})
	p.SetSubagentAdmitter(&failingIdleRuntime{&subagentRuntimeStub{SubagentAdmitter: p.admitter, state: SubagentRuntimeState{RunID: "active-run", Status: "running", Active: true}}})
	queued := asMap(t, mustExecuteAgentTool(t, p, parent, "send_message", map[string]any{"id": "worker", "message": "second"}))
	waitUntil(t, time.Second, func() bool {
		task := manager.Get(queued["task_id"].(string))
		return task != nil && task.Snapshot().Status == background.TaskFailed
	})
	if !strings.Contains(logs.String(), "PRIVATE_DIAGNOSTIC_CANARY") {
		t.Fatalf("queued request failed without a log: %s", logs.String())
	}
}

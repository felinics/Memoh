package background

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompleteAgentTaskStoresResultAndWakesWaiter(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-1", "do work", "worker: do work", false)
	if err != nil {
		t.Fatalf("StartAgentTask returned error: %v", err)
	}

	mgr.CompleteAgentTask(taskID, AgentTaskResult{
		AgentID:        "worker",
		AgentSessionID: "child-1",
		Message:        "do work",
		ModelID:        "worker-model",
		Provider:       "provider-a",
		Fork:           true,
		Status:         TaskCompleted,
		Report:         "finished report",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != TaskCompleted || snap.AgentReport != "finished report" || snap.AgentModelID != "worker-model" || snap.AgentProvider != "provider-a" || !snap.AgentFork {
		t.Fatalf("snapshot = %+v, want completed agent report", snap)
	}
}

func TestRunningAgentKillWaitsForRuntimeTerminal(t *testing.T) {
	mgr := New(nil)
	var events []TaskEvent
	mgr.SetEventFunc(func(event TaskEvent) { events = append(events, event) })
	taskID, taskCtx, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-1", "do work", "worker: do work", false)
	if err != nil {
		t.Fatalf("StartAgentTask returned error: %v", err)
	}
	if err := mgr.Kill(taskID); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	if !mgr.AgentTaskStopRequested(taskID) {
		t.Fatal("agent stop request was not recorded")
	}
	if !errors.Is(taskCtx.Err(), context.Canceled) {
		t.Fatalf("task context error = %v, want cancellation", taskCtx.Err())
	}
	if snap := mgr.Get(taskID).Snapshot(); snap.Status != TaskRunning {
		t.Fatalf("snapshot = %+v, want nonterminal running status", snap)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := mgr.WaitForSessionTask(waitCtx, "bot1", "sess1", taskID, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForSessionTask error = %v, want no terminal before runtime completion", err)
	}
	if len(events) != 1 || events[0].Event != TaskEventStarted {
		t.Fatalf("events before runtime terminal = %+v, want started only", events)
	}

	mgr.CompleteAgentTask(taskID, AgentTaskResult{
		Status: TaskKilled,
		Error:  "stopped by the user",
	})
	snap, outcome, err := mgr.WaitForSessionTask(context.Background(), "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != TaskKilled || outcome != WaitKilled || snap.AgentError != "stopped by the user" {
		t.Fatalf("snapshot = %+v outcome = %s, want killed runtime terminal", snap, outcome)
	}
	if len(events) != 2 || events[1].Event != TaskEventKilled || events[1].Status != TaskKilled {
		t.Fatalf("terminal events = %+v, want one killed event", events)
	}
}

func TestCompleteSpawnTaskStoresBranches(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "parallel research")
	if err != nil {
		t.Fatalf("StartSpawnTask returned error: %v", err)
	}
	branches := []SpawnBranch{
		{Task: "alpha", ChildSessionID: "child-a", Status: TaskCompleted, Report: "alpha result"},
		{Task: "beta", ChildSessionID: "child-b", Status: TaskFailed, Error: "beta failed"},
	}
	mgr.CompleteSpawnTask(taskID, branches)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != TaskFailed {
		t.Fatalf("status = %s, want failed when one branch fails", snap.Status)
	}
	if len(snap.Branches) != 2 || snap.Branches[0].Report != "alpha result" || snap.Branches[1].Error != "beta failed" {
		t.Fatalf("branches not preserved: %+v", snap.Branches)
	}
}

func TestKilledQueuedAgentTaskDoesNotStart(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-1", "queued", "worker: queued", true)
	if err != nil {
		t.Fatalf("StartAgentTask returned error: %v", err)
	}
	if err := mgr.KillForSession("bot1", "sess1", taskID); err != nil {
		t.Fatalf("KillForSession returned error: %v", err)
	}
	ctx, ok, err := mgr.MarkAgentTaskRunning(context.Background(), taskID)
	if err != nil {
		t.Fatalf("MarkAgentTaskRunning returned error: %v", err)
	}
	if ok || ctx != nil {
		t.Fatalf("MarkAgentTaskRunning ok=%v ctx=%v, want killed queued task to stay stopped", ok, ctx)
	}
}

func TestRunningTasksSummaryIncludesSpawnTasks(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "Parallel research")
	if err != nil {
		t.Fatalf("StartSpawnTask returned error: %v", err)
	}
	summary := mgr.RunningTasksSummary("bot1", "sess1")
	for _, want := range []string{taskID, "Parallel research", "wait_until(task_id)"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	_ = mgr.Kill(taskID)
}

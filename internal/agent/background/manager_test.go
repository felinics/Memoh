package background

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestSpawnCompletesAndWaits(t *testing.T) {
	mgr := New(nil)
	taskID, outputFile := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"echo ok",
		"/data",
		"Say ok",
		func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
			return &bridge.ExecResult{Stdout: "ok\n", ExitCode: 0}, nil
		},
		nil,
		nil,
	)
	if taskID == "" || outputFile == "" {
		t.Fatalf("Spawn returned taskID=%q outputFile=%q", taskID, outputFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != TaskCompleted || outcome != WaitCompleted {
		t.Fatalf("status = %s outcome = %s, want completed", snap.Status, outcome)
	}
	if snap.OutputTail != "ok\n" {
		t.Fatalf("output tail = %q, want ok", snap.OutputTail)
	}
}

func TestSpawnFailurePreservesUnknownExitCode(t *testing.T) {
	mgr := New(nil)
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"broken",
		"/data",
		"Broken command",
		func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
			return nil, errors.New("stream broke before exit")
		},
		nil,
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != TaskFailed || outcome != WaitFailed {
		t.Fatalf("status = %s outcome = %s, want failed", snap.Status, outcome)
	}
	if snap.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", snap.ExitCode)
	}
}

func TestSpawnAdoptUsesSelectedOutputDirectory(t *testing.T) {
	mgr := New(nil)
	resultCh := make(chan AdoptResult, 1)
	resultCh <- AdoptResult{Stdout: "ok\n", ExitCode: 0, ExitReceived: true}
	writes := make(chan string, 8)
	const outputDir = "/data/.memoh/background"

	taskID, outputFile := mgr.SpawnAdopt(
		context.Background(),
		"bot1",
		"sess1",
		"echo ok",
		"/data",
		"Say ok",
		outputDir,
		resultCh,
		func(_ context.Context, path string, _ []byte) error {
			writes <- path
			return nil
		},
	)
	if !strings.HasPrefix(outputFile, outputDir+"/") {
		t.Fatalf("output file = %q, want it under %q", outputFile, outputDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0); err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}

	seenOutput := false
	for len(writes) > 0 {
		writtenPath := <-writes
		if !strings.HasPrefix(writtenPath, outputDir+"/") {
			t.Fatalf("write path = %q, want it under %q", writtenPath, outputDir)
		}
		if writtenPath == outputFile {
			seenOutput = true
		}
	}
	if !seenOutput {
		t.Fatalf("output file %q was not written", outputFile)
	}
}

func TestKillWakesWaiter(t *testing.T) {
	mgr := New(nil)
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"sleep 30",
		"/data",
		"Sleep",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
		nil,
	)

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan TaskSnapshot, 1)
	errCh := make(chan error, 1)
	go func() {
		snap, _, err := mgr.WaitForSessionTask(waitCtx, "bot1", "sess1", taskID, 0)
		if err != nil {
			errCh <- err
			return
		}
		done <- snap
	}()

	if err := mgr.KillForSession("bot1", "sess1", taskID); err != nil {
		t.Fatalf("KillForSession returned error: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	case snap := <-done:
		if snap.Status != TaskKilled {
			t.Fatalf("status = %s, want killed", snap.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for waiter to wake")
	}
}

func TestWaitForSessionTaskReturnsStalled(t *testing.T) {
	mgr := New(nil)
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"read -p password",
		"/data",
		"Prompt",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
		nil,
	)
	task := mgr.Get(taskID)
	if task == nil {
		t.Fatal("task not found")
	}
	if !task.MarkStalled() {
		t.Fatal("expected MarkStalled to flip state")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if !snap.Stalled || snap.Status != TaskRunning || outcome != WaitStalled {
		t.Fatalf("snapshot = %+v outcome = %s, want running stalled task", snap, outcome)
	}
	_ = mgr.Kill(taskID)
}

func TestWaitForSessionTaskReturnsIdleWhenOutputSettles(t *testing.T) {
	mgr := New(nil)
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"npm run dev",
		"/data",
		"Dev server",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
		nil,
	)
	mgr.RecordOutput(taskID, "stdout", "VITE ready\n  ➜  Local: http://localhost:5173/\n")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if outcome != WaitIdle {
		t.Fatalf("outcome = %s, want idle", outcome)
	}
	if snap.Status != TaskRunning {
		t.Fatalf("status = %s, want running", snap.Status)
	}
	if !strings.Contains(snap.OutputTail, "localhost:5173") {
		t.Fatalf("output tail = %q, want ready banner", snap.OutputTail)
	}
	_ = mgr.Kill(taskID)
}

func TestWaitForSessionTaskIdleIgnoresNonExecTasks(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "long spawn")
	if err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded (idle must not fire for spawn tasks)", err)
	}
	_ = mgr.Kill(taskID)
}

func TestRunningTasksSummaryMentionsWaitTools(t *testing.T) {
	mgr := New(nil)
	taskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"npm test",
		"/data",
		"Run tests",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
		nil,
	)
	summary := mgr.RunningTasksSummary("bot1", "sess1")
	for _, want := range []string{taskID, "Run tests", "wait_until(task_id)", "get_background_status(task_id)"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	_ = mgr.Kill(taskID)
}

func TestCleanupKeepsQueuedAgentTasks(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartAgentTask(
		context.Background(), "bot1", "sess1", "agent1", "child1", "queued work", "Queued work", true,
	)
	if err != nil {
		t.Fatalf("StartAgentTask failed: %v", err)
	}

	mgr.Cleanup(0)
	if task := mgr.Get(taskID); task == nil {
		t.Fatal("cleanup removed a queued agent task")
	}
}

func TestCleanupRemovesOldTerminalAgentTasks(t *testing.T) {
	mgr := New(nil)
	taskID, _, err := mgr.StartAgentTask(
		context.Background(), "bot1", "sess1", "agent1", "child1", "work", "Work", false,
	)
	if err != nil {
		t.Fatalf("StartAgentTask failed: %v", err)
	}
	mgr.CompleteAgentTask(taskID, AgentTaskResult{Status: TaskCompleted})
	task := mgr.Get(taskID)
	if task == nil {
		t.Fatal("completed task not found")
	}
	task.mu.Lock()
	task.CompletedAt = time.Now().Add(-time.Hour)
	task.mu.Unlock()

	mgr.Cleanup(time.Minute)
	if task := mgr.Get(taskID); task != nil {
		t.Fatal("cleanup kept an old terminal agent task")
	}
}

package background

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
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

func TestShutdownCancelsAndJoinsExecAndAdoptOwners(t *testing.T) {
	mgr := New(nil)
	execStarted := make(chan struct{})
	execExited := make(chan struct{})
	execTaskID, _ := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"sleep 30",
		"/data",
		"Sleep",
		func(ctx context.Context, _, _ string, _ int32) (*bridge.ExecResult, error) {
			close(execStarted)
			<-ctx.Done()
			close(execExited)
			return nil, ctx.Err()
		},
		nil,
		nil,
	)
	adoptTaskID, _ := mgr.SpawnAdopt(
		context.Background(),
		"bot1",
		"sess1",
		"sleep 30",
		"/data",
		"Adopted sleep",
		OutputLogDir,
		make(chan AdoptResult),
		nil,
	)

	select {
	case <-execStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exec owner to start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	select {
	case <-execExited:
	default:
		t.Fatal("Shutdown returned before exec owner exited")
	}
	for _, taskID := range []string{execTaskID, adoptTaskID} {
		if status := mgr.Get(taskID).Snapshot().Status; status != TaskKilled {
			t.Fatalf("task %s status = %s, want killed", taskID, status)
		}
	}
}

func TestShutdownDeadlineCanBeRetried(t *testing.T) {
	mgr := New(nil)
	taskID, taskCtx, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "slow owner")
	if err != nil {
		t.Fatalf("StartSpawnTask returned error: %v", err)
	}
	canceled := make(chan struct{})
	release := make(chan struct{})
	go func() {
		<-taskCtx.Done()
		close(canceled)
		<-release
		mgr.CompleteSpawnTask(taskID, nil)
	}()

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = mgr.Shutdown(firstCtx)
	firstCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown error = %v, want deadline exceeded", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not cancel the active task context")
	}

	close(release)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	if err := mgr.Shutdown(secondCtx); err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
}

func TestTaskContextOutlivesRequestButNotManager(t *testing.T) {
	mgr := New(nil)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	taskID, taskCtx, err := mgr.StartSpawnTask(requestCtx, "bot1", "sess1", "detached task")
	if err != nil {
		t.Fatalf("StartSpawnTask returned error: %v", err)
	}
	cancelRequest()
	select {
	case <-taskCtx.Done():
		t.Fatalf("request cancellation propagated to background task: %v", taskCtx.Err())
	default:
	}

	ownerExited := make(chan struct{})
	go func() {
		<-taskCtx.Done()
		mgr.CompleteSpawnTask(taskID, nil)
		close(ownerExited)
	}()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := mgr.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	select {
	case <-ownerExited:
	default:
		t.Fatal("Shutdown returned before the task owner exited")
	}
}

func TestManagerRejectsWorkAfterShutdown(t *testing.T) {
	mgr := New(nil)
	queuedID, _, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-1", "queued", "queued", true)
	if err != nil {
		t.Fatalf("StartAgentTask returned error: %v", err)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	called := false
	if taskID, outputFile := mgr.Spawn(
		context.Background(),
		"bot1",
		"sess1",
		"echo no",
		"/data",
		"rejected",
		func(context.Context, string, string, int32) (*bridge.ExecResult, error) {
			called = true
			return &bridge.ExecResult{}, nil
		},
		nil,
		nil,
	); taskID != "" || outputFile != "" {
		t.Fatalf("Spawn after Shutdown = (%q, %q), want empty rejection", taskID, outputFile)
	}
	if taskID, outputFile := mgr.SpawnAdopt(
		context.Background(),
		"bot1",
		"sess1",
		"echo no",
		"/data",
		"rejected",
		OutputLogDir,
		make(chan AdoptResult),
		nil,
	); taskID != "" || outputFile != "" {
		t.Fatalf("SpawnAdopt after Shutdown = (%q, %q), want empty rejection", taskID, outputFile)
	}
	if called {
		t.Fatal("Spawn invoked exec function after Shutdown")
	}
	if _, _, err := mgr.StartAgentTask(context.Background(), "bot1", "sess1", "worker", "child-2", "no", "no", false); !errors.Is(err, ErrManagerStopped) {
		t.Fatalf("StartAgentTask error = %v, want ErrManagerStopped", err)
	}
	if _, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "no"); !errors.Is(err, ErrManagerStopped) {
		t.Fatalf("StartSpawnTask error = %v, want ErrManagerStopped", err)
	}
	if _, _, err := mgr.StartVideoTask(context.Background(), "bot1", "sess1", "no"); !errors.Is(err, ErrManagerStopped) {
		t.Fatalf("StartVideoTask error = %v, want ErrManagerStopped", err)
	}
	if _, ok, err := mgr.MarkAgentTaskRunning(context.Background(), queuedID); ok || !errors.Is(err, ErrManagerStopped) {
		t.Fatalf("MarkAgentTaskRunning after Shutdown ok=%v err=%v, want stopped rejection", ok, err)
	}
}

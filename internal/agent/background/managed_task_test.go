package background

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventRecorder collects task events behind a mutex; managed tasks emit from
// their own goroutine.
type eventRecorder struct {
	mu     sync.Mutex
	events []TaskEvent
}

func (r *eventRecorder) record(event TaskEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) types() []TaskEventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TaskEventType, 0, len(r.events))
	for _, event := range r.events {
		out = append(out, event.Event)
	}
	return out
}

func (r *eventRecorder) last() TaskEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return TaskEvent{}
	}
	return r.events[len(r.events)-1]
}

// awaitTerminal waits for the terminal event, which is emitted after the
// waiter has already been woken by the status change.
func (r *eventRecorder) awaitTerminal(t *testing.T) TaskEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		switch last := r.last(); last.Event {
		case TaskEventCompleted, TaskEventFailed, TaskEventKilled:
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no terminal event arrived: %v", r.types())
	return TaskEvent{}
}

func waitManaged(t *testing.T, mgr *Manager, taskID string) (TaskSnapshot, WaitOutcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snap, outcome, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	return snap, outcome
}

func TestSpawnManagedCompletesWithEventsAndLogs(t *testing.T) {
	mgr := New(nil)
	rec := &eventRecorder{}
	mgr.SetEventFunc(rec.record)

	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent() // a finished request must not abort the job

	var runCtxErr error
	taskID := mgr.SpawnManaged(parent, "bot1", "sess1", "Install Tool", func(ctx context.Context, log func(stream, chunk string)) error {
		runCtxErr = ctx.Err()
		log("stdout", "step one\n")
		log("stderr", "warning\n")
		return nil
	})
	if taskID == "" {
		t.Fatal("SpawnManaged returned an empty task id")
	}

	snap, outcome := waitManaged(t, mgr, taskID)
	if snap.Status != TaskCompleted || outcome != WaitCompleted {
		t.Fatalf("status = %s outcome = %s, want completed", snap.Status, outcome)
	}
	if runCtxErr != nil {
		t.Fatalf("run context error = %v, want a context detached from the parent", runCtxErr)
	}
	if snap.Kind != KindDependency || snap.Description != "Install Tool" || snap.ExitCode != 0 || snap.Error != "" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.OutputTail != "step one\nwarning\n" {
		t.Fatalf("output tail = %q", snap.OutputTail)
	}

	final := rec.awaitTerminal(t)
	got := rec.types()
	want := []TaskEventType{TaskEventStarted, TaskEventOutput, TaskEventOutput, TaskEventCompleted}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
	rec.mu.Lock()
	output := rec.events[1]
	rec.mu.Unlock()
	if output.Stream != "stdout" || output.Chunk != "step one\n" || output.Kind != KindDependency || output.Command != "Install Tool" {
		t.Fatalf("output event = %+v", output)
	}
	if final.Status != TaskCompleted || final.TaskID != taskID || final.BotID != "bot1" || final.SessionID != "sess1" {
		t.Fatalf("completed event = %+v", final)
	}
}

func TestSpawnManagedFailureRecordsError(t *testing.T) {
	mgr := New(nil)
	rec := &eventRecorder{}
	mgr.SetEventFunc(rec.record)

	taskID := mgr.SpawnManaged(context.Background(), "bot1", "sess1", "Install Tool", func(_ context.Context, log func(stream, chunk string)) error {
		log("stdout", "downloading\n")
		return errors.New("install script exited 3")
	})

	snap, outcome := waitManaged(t, mgr, taskID)
	if snap.Status != TaskFailed || outcome != WaitFailed {
		t.Fatalf("status = %s outcome = %s, want failed", snap.Status, outcome)
	}
	if snap.Error != "install script exited 3" || snap.ExitCode != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !strings.HasSuffix(snap.OutputTail, "[error] install script exited 3\n") || !strings.HasPrefix(snap.OutputTail, "downloading\n") {
		t.Fatalf("output tail = %q", snap.OutputTail)
	}
	if last := rec.awaitTerminal(t); last.Event != TaskEventFailed || last.Status != TaskFailed {
		t.Fatalf("last event = %+v, want failed", last)
	}
}

func TestSpawnManagedPanicFailsTask(t *testing.T) {
	mgr := New(nil)
	taskID := mgr.SpawnManaged(context.Background(), "bot1", "sess1", "Install Tool", func(context.Context, func(string, string)) error {
		panic("boom")
	})
	snap, outcome := waitManaged(t, mgr, taskID)
	if snap.Status != TaskFailed || outcome != WaitFailed {
		t.Fatalf("status = %s outcome = %s, want failed", snap.Status, outcome)
	}
	if !strings.Contains(snap.Error, "panicked: boom") {
		t.Fatalf("error = %q, want the panic text", snap.Error)
	}
}

func TestSpawnManagedKillCancelsRunAndKeepsKilledState(t *testing.T) {
	mgr := New(nil)
	rec := &eventRecorder{}
	mgr.SetEventFunc(rec.record)

	started := make(chan struct{})
	returned := make(chan error, 1)
	taskID := mgr.SpawnManaged(context.Background(), "bot1", "sess1", "Install Tool", func(ctx context.Context, _ func(string, string)) error {
		close(started)
		<-ctx.Done()
		returned <- ctx.Err()
		return ctx.Err()
	})
	<-started
	if err := mgr.Kill(taskID); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run context error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not observe the cancellation")
	}

	snap, outcome := waitManaged(t, mgr, taskID)
	if snap.Status != TaskKilled || outcome != WaitKilled {
		t.Fatalf("status = %s outcome = %s, want killed", snap.Status, outcome)
	}
	// The run returning after Kill must not publish a second terminal event.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := rec.types()
	if len(got) != 2 || got[0] != TaskEventStarted || got[1] != TaskEventKilled {
		t.Fatalf("events = %v, want started then killed only", got)
	}
}

package tools

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/background"
)

func TestRunSpawnProgressStopsWithParentWhileManagedChildContinues(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	manager := background.New(nil)
	taskID, childCtx, err := manager.StartAgentTask(
		context.WithoutCancel(parentCtx),
		"bot-1",
		"session-1",
		"agent-1",
		"child-1",
		"work",
		"Work",
		false,
	)
	if err != nil {
		t.Fatalf("StartAgentTask() error = %v", err)
	}
	defer manager.CompleteAgentTask(taskID, background.AgentTaskResult{Status: background.TaskCompleted})

	ticks := make(chan time.Time, 2)
	events := make(chan ToolStreamEvent, 1)
	done := make(chan struct{})
	go func() {
		runSpawnProgress(parentCtx, func(event ToolStreamEvent) {
			events <- event
		}, ticks)
		close(done)
	}()

	ticks <- time.Now()
	select {
	case event := <-events:
		if event.Type != StreamEventSpawnProgress {
			t.Fatalf("event type = %q, want %q", event.Type, StreamEventSpawnProgress)
		}
	case <-time.After(time.Second):
		t.Fatal("spawn progress did not emit for controlled tick")
	}

	parentCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("spawn progress did not stop after parent cancellation")
	}
	if childCtx.Err() != nil {
		t.Fatalf("managed child context error = %v, want child to remain active", childCtx.Err())
	}
	task := manager.Get(taskID)
	if task == nil || task.Snapshot().Status != background.TaskRunning {
		t.Fatalf("managed child task = %#v, want running", task)
	}

	ticks <- time.Now()
	select {
	case event := <-events:
		t.Fatalf("late spawn progress event = %#v", event)
	default:
	}
}

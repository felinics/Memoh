package background

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ManagedRun is the body of a managed task. Text passed to log is appended to
// the task output and emitted as an output event, exactly like RecordOutput.
type ManagedRun func(ctx context.Context, log func(stream, chunk string)) error

// ManagedTaskTimeout is the safety ceiling for an in-process managed task.
// It matches BackgroundExecTimeout: the longest managed job today is a
// workspace dependency install, whose catalog timeouts stay well below it.
const ManagedTaskTimeout = time.Duration(BackgroundExecTimeout) * time.Second

// SpawnManaged runs an in-process job as a background task. It reuses the
// Task/TaskEvent lifecycle so the UI can show progress and completion.
//
// The job receives a context detached from parentCtx (a finished request must
// not abort it) but bounded by ManagedTaskTimeout and cancelled by Kill. A nil
// error completes the task; a non-nil error (or a panic) fails it and stores
// the error text on the task.
func (m *Manager) SpawnManaged(parentCtx context.Context, botID, sessionID, description string, run ManagedRun) (taskID string) {
	ctx, cancel := detachedContextWithTimeout(parentCtx, ManagedTaskTimeout)

	m.mu.Lock()
	taskID = m.newTaskIDLocked(botID)
	task := &Task{
		ID:          taskID,
		Kind:        KindDependency,
		BotID:       botID,
		SessionID:   sessionID,
		Description: description,
		Status:      TaskRunning,
		StartedAt:   time.Now(),
		cancel:      cancel,
		changed:     make(chan struct{}),
	}
	m.tasks[taskID] = task
	m.mu.Unlock()

	m.logger.Info("background managed task started",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("description", truncate(description, 120)),
	)
	m.emitTaskEvent(task, TaskEventStarted, "", "")

	go m.runManaged(ctx, cancel, task, run)
	return taskID
}

func (m *Manager) runManaged(ctx context.Context, cancel context.CancelFunc, task *Task, run ManagedRun) {
	defer cancel()
	log := func(stream, chunk string) { m.RecordOutput(task.ID, stream, chunk) }
	m.completeManaged(task, callManaged(ctx, log, run))
}

// callManaged invokes run and turns a panic into an error so a faulty job
// fails its task instead of taking the process down.
func callManaged(ctx context.Context, log func(stream, chunk string), run ManagedRun) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("background: managed task panicked: %v", r)
		}
	}()
	if run == nil {
		return errors.New("background: managed task has no run function")
	}
	return run(ctx, log)
}

// completeManaged records the terminal state of a managed task unless Kill
// already did. Managed jobs have no process exit code; the synthetic 0/1
// keeps the generic task rendering consistent with the status.
func (m *Manager) completeManaged(task *Task, runErr error) {
	task.mu.Lock()
	if task.Status == TaskKilled {
		task.mu.Unlock()
		return
	}
	task.CompletedAt = time.Now()
	if runErr != nil {
		task.Status = TaskFailed
		task.ExitCode = 1
		task.Error = strings.TrimSpace(runErr.Error())
		task.appendOutputLocked(fmt.Sprintf("[error] %s\n", task.Error))
	} else {
		task.Status = TaskCompleted
		task.ExitCode = 0
	}
	status := task.Status
	duration := task.CompletedAt.Sub(task.StartedAt)
	task.signalChangedLocked()
	task.mu.Unlock()

	m.logger.Info("background managed task finished",
		slog.String("task_id", task.ID),
		slog.String("status", string(status)),
		slog.Duration("duration", duration),
	)

	eventType := TaskEventCompleted
	if status == TaskFailed {
		eventType = TaskEventFailed
	}
	m.emitTaskEvent(task, eventType, "", "")
}

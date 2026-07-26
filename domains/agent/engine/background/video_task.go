package background

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// StartVideoTask registers an asynchronous video generation task and returns a
// detached, cancelable context for the provider polling goroutine.
func (m *Manager) StartVideoTask(parentCtx context.Context, botID, sessionID, description string) (string, context.Context, error) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return "", nil, ErrManagerStopped
	}
	ctx, cancel := m.taskContextLocked(parentCtx, time.Duration(BackgroundExecTimeout)*time.Second)
	taskID := m.newTaskIDLocked(botID)
	task := &Task{
		ID:          taskID,
		Kind:        KindVideo,
		BotID:       botID,
		SessionID:   sessionID,
		Description: description,
		Status:      TaskRunning,
		StartedAt:   time.Now(),
		cancel:      cancel,
		changed:     make(chan struct{}),
	}
	m.tasks[taskID] = task
	m.trackOwnerLocked(task)
	m.mu.Unlock()

	m.logger.InfoContext(parentCtx, "background video task started",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("description", truncate(description, 120)),
	)
	m.emitTaskEvent(task, TaskEventStarted, "", "")
	return taskID, ctx, nil
}

// RecordVideoTaskProgress records the latest provider-visible video job state.
// The result map is merged into the current task result; outputLine is appended
// to the compact UI tail when non-empty.
func (m *Manager) RecordVideoTaskProgress(taskID string, result map[string]any, outputLine string) bool {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil || task.Kind != KindVideo {
		return false
	}

	outputLine = strings.TrimSpace(outputLine)
	task.mu.Lock()
	if task.Status != TaskRunning {
		task.mu.Unlock()
		return false
	}
	if len(result) > 0 {
		if task.Result == nil {
			task.Result = make(map[string]any, len(result))
		}
		for k, v := range result {
			task.Result[k] = v
		}
	}
	if outputLine != "" {
		task.appendOutputLocked(outputLine + "\n")
	}
	task.signalChangedLocked()
	task.mu.Unlock()

	if outputLine != "" {
		m.emitTaskEvent(task, TaskEventOutput, "video", outputLine+"\n")
	}
	return true
}

// CompleteVideoTask finalises a video generation task unless it was killed
// before the provider goroutine returned.
func (m *Manager) CompleteVideoTask(taskID string, status TaskStatus, result map[string]any, errorMessage string) {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil || task.Kind != KindVideo {
		return
	}
	defer m.finishOwner(task)
	defer task.Cancel()

	if status == "" {
		if strings.TrimSpace(errorMessage) != "" {
			status = TaskFailed
		} else {
			status = TaskCompleted
		}
	}
	if status != TaskCompleted && status != TaskFailed {
		status = TaskFailed
	}

	task.mu.Lock()
	if task.Status == TaskKilled {
		task.mu.Unlock()
		return
	}
	task.CompletedAt = time.Now()
	task.Status = status
	if len(result) > 0 {
		task.Result = cloneResultMap(result)
	}
	task.Error = strings.TrimSpace(errorMessage)
	if task.Error != "" {
		task.appendOutputLocked(fmt.Sprintf("[error] %s\n", task.Error))
	} else if status == TaskCompleted {
		task.appendOutputLocked("Video generation completed.\n")
	}
	duration := task.CompletedAt.Sub(task.StartedAt)
	task.signalChangedLocked()
	task.mu.Unlock()

	m.logger.Info("background video task finished",
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

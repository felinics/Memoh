package background

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// TaskKind identifies what kind of work a background task tracks.
type TaskKind string

const (
	// KindExec is a background container command execution.
	KindExec TaskKind = "exec"
	// KindSpawn is a background subagent batch run by the spawn tool.
	KindSpawn TaskKind = "spawn"
	// KindAgent is a single managed subagent task.
	KindAgent TaskKind = "agent"
	// KindVideo is an asynchronous video generation task.
	KindVideo TaskKind = "video"
)

// SpawnTaskTimeout is the safety ceiling for a background spawn task,
// mirroring BackgroundExecTimeout for exec tasks.
const SpawnTaskTimeout = 30 * time.Minute

// MaxRunningSpawnTasks caps concurrently running background spawn tasks per
// bot+session to prevent subagent storms across agent runs.
const MaxRunningSpawnTasks = 3

// spawnReportMaxBytes caps each branch report carried in task snapshots,
// mirroring maxTailBytes for exec output tails. Full transcripts stay in the
// child session.
const spawnReportMaxBytes = 2048

// spawnBranchTaskMaxBytes caps the per-branch task echo: identification only,
// the full task text is persisted as the child session's user message.
const spawnBranchTaskMaxBytes = 200

// spawnBranchErrorMaxBytes caps per-branch error strings, whose summary sits
// at the head.
const spawnBranchErrorMaxBytes = 512

// SpawnBranch is the join-record entry for one subagent in a spawn batch.
// ChildSessionID points at the persisted subagent session so the parent
// agent can read the full transcript via history tools when needed.
type SpawnBranch struct {
	Task           string
	ChildSessionID string
	Status         TaskStatus
	Report         string
	Error          string
}

// AgentTaskResult is the terminal output for one managed subagent run.
type AgentTaskResult struct {
	AgentID        string
	AgentSessionID string
	Message        string
	ModelID        string
	Provider       string
	Fork           bool
	Status         TaskStatus
	Report         string
	Error          string
}

// StartAgentTask registers a managed subagent task. Queued tasks are visible
// to background task status immediately but do not get a cancelable run context until
// MarkAgentTaskRunning is called.
func (m *Manager) StartAgentTask(parentCtx context.Context, botID, sessionID, agentID, agentSessionID, message, description string, queued bool) (string, context.Context, error) {
	status := TaskRunning
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if queued {
		status = TaskQueued
	} else {
		ctx, cancel = detachedContextWithTimeout(parentCtx, SpawnTaskTimeout)
	}

	m.mu.Lock()
	taskID := m.newTaskIDLocked(botID)
	task := &Task{
		ID:             taskID,
		Kind:           KindAgent,
		BotID:          botID,
		SessionID:      sessionID,
		Description:    description,
		AgentID:        agentID,
		AgentSessionID: agentSessionID,
		AgentMessage:   message,
		Status:         status,
		StartedAt:      time.Now(),
		cancel:         cancel,
		changed:        make(chan struct{}),
	}
	m.tasks[taskID] = task
	m.mu.Unlock()

	m.logger.Info("background agent task registered",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("agent_id", agentID),
		slog.String("status", string(status)),
	)
	if queued {
		m.emitTaskEvent(task, TaskEventQueued, "", "")
	} else {
		m.emitTaskEvent(task, TaskEventStarted, "", "")
	}
	return taskID, ctx, nil
}

// MarkAgentTaskRunning transitions a queued managed agent task into running
// and returns the cancelable run context. If the task was killed while queued,
// ok is false and no run should start.
func (m *Manager) MarkAgentTaskRunning(parentCtx context.Context, taskID string) (context.Context, bool, error) {
	ctx, cancel := detachedContextWithTimeout(parentCtx, SpawnTaskTimeout)
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil || task.Kind != KindAgent {
		cancel()
		return nil, false, fmt.Errorf("agent task %s not found", taskID)
	}
	task.mu.Lock()
	if task.Status == TaskKilled {
		task.mu.Unlock()
		cancel()
		return nil, false, nil
	}
	if task.Status != TaskQueued {
		task.mu.Unlock()
		cancel()
		return nil, false, fmt.Errorf("agent task %s is not queued (status: %s)", taskID, task.Status)
	}
	task.Status = TaskRunning
	task.cancel = cancel
	task.signalChangedLocked()
	task.mu.Unlock()

	m.emitTaskEvent(task, TaskEventStarted, "", "")
	return ctx, true, nil
}

// CompleteAgentTask finalises a managed agent task. Running task cancellation
// remains nonterminal until this call records the runtime's resolved outcome.
func (m *Manager) CompleteAgentTask(taskID string, result AgentTaskResult) {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil || task.Kind != KindAgent {
		return
	}
	defer task.Cancel()

	status := result.Status
	if status == "" {
		if result.Error != "" {
			status = TaskFailed
		} else {
			status = TaskCompleted
		}
	}

	task.mu.Lock()
	if task.Status == TaskKilled {
		task.mu.Unlock()
		return
	}
	task.CompletedAt = time.Now()
	task.Status = status
	task.AgentReport = result.Report
	task.AgentError = result.Error
	task.AgentModelID = result.ModelID
	task.AgentProvider = result.Provider
	task.AgentFork = result.Fork
	if result.Message != "" {
		task.AgentMessage = result.Message
	}
	task.signalChangedLocked()
	task.mu.Unlock()

	eventType := TaskEventCompleted
	switch status {
	case TaskKilled:
		eventType = TaskEventKilled
	case TaskCompleted:
	default:
		eventType = TaskEventFailed
	}
	m.emitTaskEvent(task, eventType, "", "")
}

// CompleteSpawnTask finalises a spawn task with its branch outcomes and
// records the join result. The task is completed when every branch completed
// and failed when any branch failed. Branch outcomes are recorded even for
// killed tasks.
func (m *Manager) CompleteSpawnTask(taskID string, branches []SpawnBranch) {
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil || task.Kind != KindSpawn {
		return
	}
	defer task.Cancel() // release the safety-timeout context

	branches = clampSpawnBranches(branches)

	status := TaskCompleted
	for _, b := range branches {
		if b.Status != TaskCompleted {
			status = TaskFailed
			break
		}
	}

	task.mu.Lock()
	task.branches = branches
	if task.Status == TaskKilled {
		task.mu.Unlock()
		return
	}
	task.CompletedAt = time.Now()
	task.Status = status
	duration := task.CompletedAt.Sub(task.StartedAt)
	task.signalChangedLocked()
	task.mu.Unlock()

	m.logger.Info("background spawn task finished",
		slog.String("task_id", task.ID),
		slog.String("status", string(status)),
		slog.Int("branches", len(branches)),
		slog.Duration("duration", duration),
	)

	eventType := TaskEventCompleted
	if status == TaskFailed {
		eventType = TaskEventFailed
	}
	m.emitTaskEvent(task, eventType, "", "")
}

// clampSpawnBranches returns a copy of branches with each text field bounded:
// reports keep their tail (findings live at the end per the subagent response
// contract), task echoes and errors keep their head (identification and
// summary sit at the front).
func clampSpawnBranches(branches []SpawnBranch) []SpawnBranch {
	out := append([]SpawnBranch(nil), branches...)
	for i := range out {
		if len(out[i].Report) > spawnReportMaxBytes {
			out[i].Report = out[i].Report[len(out[i].Report)-spawnReportMaxBytes:]
		}
		out[i].Task = truncate(out[i].Task, spawnBranchTaskMaxBytes)
		out[i].Error = truncate(out[i].Error, spawnBranchErrorMaxBytes)
	}
	return out
}

// runningSpawnCountLocked counts running spawn tasks for a bot+session.
// Caller must hold m.mu.
func (m *Manager) runningSpawnCountLocked(botID, sessionID string) int {
	count := 0
	for _, t := range m.tasks {
		if t.Kind != KindSpawn || t.BotID != botID || t.SessionID != sessionID {
			continue
		}
		t.mu.Lock()
		if t.Status == TaskRunning {
			count++
		}
		t.mu.Unlock()
	}
	return count
}

// StartSpawnTask registers a background task for a spawn (subagent batch)
// whose execution is driven by the spawn tool. It returns the task ID and a
// detached, cancelable context that subagent branches must derive from so
// Kill can stop in-flight work.
func (m *Manager) StartSpawnTask(parentCtx context.Context, botID, sessionID, description string) (string, context.Context, error) {
	ctx, cancel := detachedContextWithTimeout(parentCtx, SpawnTaskTimeout)

	m.mu.Lock()
	if m.runningSpawnCountLocked(botID, sessionID) >= MaxRunningSpawnTasks {
		m.mu.Unlock()
		cancel()
		return "", nil, fmt.Errorf("spawn limit reached: max %d concurrently running background spawn tasks per session", MaxRunningSpawnTasks)
	}
	taskID := m.newTaskIDLocked(botID)
	task := &Task{
		ID:          taskID,
		Kind:        KindSpawn,
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

	m.logger.Info("background spawn task started",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("description", truncate(description, 120)),
	)
	m.emitTaskEvent(task, TaskEventStarted, "", "")
	return taskID, ctx, nil
}

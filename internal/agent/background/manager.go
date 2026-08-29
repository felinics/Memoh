// Package background implements a background task manager for long-running
// commands and subagent work. Tasks can be started asynchronously, observed via
// live UI events, waited on by tools, and queried for their final results.
package background

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// DefaultExecTimeout is the default timeout for foreground exec calls.
	DefaultExecTimeout int32 = 30
	// MaxExecTimeout is the maximum allowed timeout (10 minutes).
	MaxExecTimeout int32 = 600
	// BackgroundExecTimeout is the timeout for background tasks (30 minutes).
	BackgroundExecTimeout int32 = 1800
	// DefaultWaitTimeout bounds a single wait_until call. The task keeps
	// running afterwards; callers re-wait to keep observing.
	DefaultWaitTimeout = 120 * time.Second
	// DefaultIdleThreshold is how long an exec task's output must stay quiet
	// before a waiter is woken with WaitIdle.
	DefaultIdleThreshold = 20 * time.Second
	// DefaultCleanupInterval is how often the manager prunes old completed tasks.
	DefaultCleanupInterval = time.Hour
	// DefaultTaskRetention is how long completed tasks are retained in memory.
	DefaultTaskRetention = 24 * time.Hour
	// OutputLogDir is the default directory where background task output logs
	// are written.
	OutputLogDir = "/tmp/memoh-bg"

	// stallCheckInterval is how often the stall watchdog checks output growth.
	stallCheckInterval = 5 * time.Second
	// stallThreshold is the duration of zero output growth before we consider
	// the command stalled and possibly waiting for interactive input.
	stallThreshold = 45 * time.Second
)

// ExecFunc executes a command in a container and returns the result.
// This is the signature that bridge.Client.Exec satisfies.
type ExecFunc func(ctx context.Context, command, workDir string, timeout int32) (*bridge.ExecResult, error)

// WriteFileFunc writes content to a file in the container.
type WriteFileFunc func(ctx context.Context, path string, data []byte) error

// ReadFileFunc reads content from a file in the container.
type ReadFileFunc func(ctx context.Context, path string) ([]byte, error)

// Manager tracks background tasks and emits live task events.
type Manager struct {
	mu        sync.Mutex
	tasks     map[string]*Task // taskID -> Task
	logger    *slog.Logger
	eventFunc func(TaskEvent) // optional callback for live UI task updates
}

// New creates a new background task Manager.
func New(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		tasks:  make(map[string]*Task),
		logger: logger.With(slog.String("service", "background")),
	}
}

// SetEventFunc registers a callback for live background task events.
func (m *Manager) SetEventFunc(fn func(TaskEvent)) {
	m.mu.Lock()
	m.eventFunc = fn
	m.mu.Unlock()
}

func (m *Manager) emitEvent(event TaskEvent) {
	m.mu.Lock()
	fn := m.eventFunc
	m.mu.Unlock()
	if fn != nil {
		fn(event)
	}
}

func (m *Manager) emitTaskEvent(task *Task, event TaskEventType, stream, chunk string) {
	if task == nil {
		return
	}
	task.mu.Lock()
	command := task.Command
	if command == "" {
		command = task.Description
	}
	if command == "" {
		command = task.AgentMessage
	}
	payload := TaskEvent{
		Event:          event,
		TaskID:         task.ID,
		Kind:           task.Kind,
		BotID:          task.BotID,
		SessionID:      task.SessionID,
		Command:        command,
		AgentID:        task.AgentID,
		AgentSessionID: task.AgentSessionID,
		Status:         task.Status,
		Stream:         stream,
		Chunk:          chunk,
		Tail:           task.outputTailLocked(),
		OutputFile:     task.OutputFile,
		ExitCode:       task.ExitCode,
		Duration:       time.Since(task.StartedAt).Round(time.Millisecond).String(),
		Stalled:        event == TaskEventStalled,
	}
	task.mu.Unlock()
	if event == TaskEventCompleted || event == TaskEventFailed || event == TaskEventStalled {
		payload.Duration = strings.TrimSpace(payload.Duration)
	}
	m.emitEvent(payload)
}

// Spawn starts a command in the background. It returns the task ID immediately.
// The command runs asynchronously and can be observed through task status tools.
//
// execFn should call bridge.Client.Exec (or equivalent).
// writeFn should call bridge.Client.WriteFile to persist output logs.
func (m *Manager) Spawn(
	parentCtx context.Context,
	botID, sessionID, command, workDir, description string,
	execFn ExecFunc,
	writeFn WriteFileFunc,
	readFn ReadFileFunc,
) (taskID, outputFile string) {
	m.mu.Lock()
	taskID = m.newTaskIDLocked(botID)
	outputFile = fmt.Sprintf("%s/%s.log", OutputLogDir, taskID)

	task := &Task{
		ID:          taskID,
		Kind:        KindExec,
		BotID:       botID,
		SessionID:   sessionID,
		Command:     command,
		Description: description,
		WorkDir:     workDir,
		Status:      TaskRunning,
		OutputFile:  outputFile,
		StartedAt:   time.Now(),
		changed:     make(chan struct{}),
	}
	m.tasks[taskID] = task
	m.mu.Unlock()

	m.initializeOutputFile(parentCtx, task, writeFn)

	m.logger.Info("background task spawned",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("command", truncate(command, 120)),
	)
	m.emitTaskEvent(task, TaskEventStarted, "", "")

	go m.run(parentCtx, task, execFn, writeFn, readFn)
	return taskID, outputFile
}

// SpawnAdopt registers a background task for a command that is already running
// externally (e.g. via ExecStream). Instead of re-executing the command, it
// waits for the result on the provided channel. This enables "flip to background"
// where a foreground stream is handed off without killing the process.
func (m *Manager) SpawnAdopt(
	parentCtx context.Context,
	botID, sessionID, command, workDir, description, outputDir string,
	resultCh <-chan AdoptResult,
	writeFn WriteFileFunc,
) (taskID, outputFile string) {
	m.mu.Lock()
	taskID = m.newTaskIDLocked(botID)
	outputFile = backgroundOutputFile(outputDir, taskID)

	task := &Task{
		ID:          taskID,
		Kind:        KindExec,
		BotID:       botID,
		SessionID:   sessionID,
		Command:     command,
		Description: description,
		WorkDir:     workDir,
		Status:      TaskRunning,
		OutputFile:  outputFile,
		StartedAt:   time.Now(),
		changed:     make(chan struct{}),
	}
	m.tasks[taskID] = task
	m.mu.Unlock()

	m.initializeOutputFile(parentCtx, task, writeFn)

	m.logger.Info("background task adopted",
		slog.String("task_id", taskID),
		slog.String("bot_id", botID),
		slog.String("command", truncate(command, 120)),
	)
	m.emitTaskEvent(task, TaskEventStarted, "", "")

	go m.runAdopt(parentCtx, task, resultCh, writeFn)
	return taskID, outputFile
}

// RecordOutput appends live output for a running task and emits a UI event.
func (m *Manager) RecordOutput(taskID, stream, chunk string) {
	chunk = strings.TrimSuffix(chunk, "\x00")
	if strings.TrimSpace(taskID) == "" || chunk == "" {
		return
	}
	m.mu.Lock()
	task := m.tasks[taskID]
	m.mu.Unlock()
	if task == nil {
		return
	}
	task.AppendOutput(chunk)
	m.emitTaskEvent(task, TaskEventOutput, stream, chunk)
}

func (m *Manager) newTaskIDLocked(botID string) string {
	prefix := botID[:min(8, len(botID))]
	for {
		id := fmt.Sprintf("bg_%s_%s", prefix, shortRandHex(4))
		if _, exists := m.tasks[id]; !exists {
			return id
		}
	}
}

func shortRandHex(n int) string {
	if n <= 0 {
		n = 4
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("background: read random bytes: %w", err))
	}
	return hex.EncodeToString(buf)
}

// runAdopt waits for the adopted stream result and handles completion.
func (m *Manager) runAdopt(parentCtx context.Context, task *Task, resultCh <-chan AdoptResult, writeFn WriteFileFunc) {
	ctx, cancel := detachedContextWithTimeout(parentCtx, time.Duration(BackgroundExecTimeout)*time.Second)
	task.mu.Lock()
	task.cancel = cancel
	task.mu.Unlock()
	defer cancel()

	// Ensure output directory exists.
	_ = ensureOutputDir(ctx, writeFn, task.OutputFile)

	// Start stall watchdog.
	go m.stallWatchdog(ctx, task)

	// Wait for the result from the already-running stream.
	var result AdoptResult
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		result = AdoptResult{Err: ctx.Err()}
	}

	// Write output to log file in container.
	if writeFn != nil {
		combined := combineOutput(result.Stdout, result.Stderr)
		if result.Err != nil {
			combined = appendLogError(combined, result.Err)
		}
		if combined != "" || result.Err != nil {
			if err := writeFn(context.WithoutCancel(ctx), task.OutputFile, []byte(combined)); err != nil {
				m.logger.Warn("background task: write output log failed",
					slog.String("task_id", task.ID),
					slog.String("output_file", task.OutputFile),
					slog.Any("error", err),
				)
			}
		}
	}

	stdout := result.Stdout
	stderr := result.Stderr
	if result.OutputRecorded {
		stdout = ""
		stderr = ""
	}
	m.completeTask(task, stdout, stderr, result.Err, result.ExitCode, result.ExitReceived)
}

func (m *Manager) run(parentCtx context.Context, task *Task, execFn ExecFunc, writeFn WriteFileFunc, readFn ReadFileFunc) {
	ctx, cancel := detachedContextWithTimeout(parentCtx, time.Duration(BackgroundExecTimeout)*time.Second)
	task.mu.Lock()
	task.cancel = cancel
	task.mu.Unlock()
	defer cancel()

	// Ensure output directory exists.
	_ = ensureOutputDir(ctx, writeFn, task.OutputFile)

	// Start stall watchdog to detect commands waiting for interactive input.
	go m.stallWatchdog(ctx, task)

	// Wrap command to tee output to the log file inside the container and
	// capture the command exit code into a sentinel file via fd 3 redirect.
	// Even if the gRPC stream dies after process completion, we can recover
	// the actual exit code by reading the sentinel file.
	wrappedCmd := fmt.Sprintf(
		"{ { ( %s ) ; echo $? >&3 ; } 2>&1 | tee %s ; } 3>%s.exit",
		task.Command, task.OutputFile, task.OutputFile,
	)

	result, err := execFn(ctx, wrappedCmd, task.WorkDir, BackgroundExecTimeout)
	if err != nil {
		m.logger.Warn("background task: execFn returned error",
			slog.String("task_id", task.ID),
			slog.Any("exec_error", err),
		)
	}

	// Always prefer the sentinel file for the real exit code.
	// The wrappedCmd uses a pipeline: the shell exits with tee's code (0),
	// not the actual command's code. The sentinel captures the real value.
	// On gRPC error the sentinel also lets us recover without -1.
	if readFn != nil {
		ec, recoverErr := readSentinelExitCode(ctx, task.OutputFile+".exit", readFn)
		if recoverErr == nil {
			if err != nil {
				m.logger.Info("background task: recovered exit code from sentinel file after stream error",
					slog.String("task_id", task.ID),
					slog.Int("recovered_exit_code", int(ec)),
					slog.Any("stream_error", err),
				)
			}
			result = &bridge.ExecResult{ExitCode: ec}
			err = nil
		} else if err != nil {
			m.logger.Warn("background task: sentinel recovery failed",
				slog.String("task_id", task.ID),
				slog.Any("recover_error", recoverErr),
			)
		}
		// If err==nil but sentinel unreadable: fall through to use gRPC exit code
	}

	var stdout, stderr string
	var exitCode int32
	// `result` is non-nil whenever execFn returned a populated ExecResult or
	// sentinel recovery succeeded. In either case the exit code is the real
	// value the command returned, not a guess.
	exitKnown := result != nil
	if result != nil {
		stdout = result.Stdout
		stderr = result.Stderr
		exitCode = result.ExitCode
	}
	m.completeTask(task, stdout, stderr, err, exitCode, exitKnown)
}

// completeTask finalises a task's bookkeeping after execution.
//
// exitKnown distinguishes two cases when execErr is non-nil:
//   - exitKnown=true: the bridge already delivered an EXIT frame (or the
//     sentinel file was recovered) before the stream broke, so exitCode is
//     real — record it instead of overwriting with -1.
//   - exitKnown=false: we genuinely have no exit code (stream died before
//     EXIT, sentinel unreadable) — fall back to -1 to flag the unknown.
func (m *Manager) completeTask(task *Task, stdout, stderr string, execErr error, exitCode int32, exitKnown bool) {
	if execErr != nil {
		task.AppendOutput(fmt.Sprintf("[error] %v\n", execErr))
	} else {
		task.AppendOutput(stdout)
		if stderr != "" {
			task.AppendOutput(stderr)
		}
	}

	task.mu.Lock()
	if task.Status == TaskKilled {
		task.mu.Unlock()
		return
	}
	task.CompletedAt = time.Now()
	switch {
	case execErr != nil && !exitKnown:
		task.Status = TaskFailed
		task.ExitCode = -1
	case execErr != nil && exitKnown:
		task.Status = TaskFailed
		task.ExitCode = exitCode
	default:
		task.ExitCode = exitCode
		if exitCode == 0 {
			task.Status = TaskCompleted
		} else {
			task.Status = TaskFailed
		}
	}
	status := task.Status
	finalExitCode := task.ExitCode
	duration := task.CompletedAt.Sub(task.StartedAt)
	task.signalChangedLocked()
	task.mu.Unlock()

	m.logger.Info("background task finished",
		slog.String("task_id", task.ID),
		slog.String("status", string(status)),
		slog.Int("exit_code", int(finalExitCode)),
		slog.Duration("duration", duration),
	)

	eventType := TaskEventCompleted
	if status == TaskFailed {
		eventType = TaskEventFailed
	}
	m.emitTaskEvent(task, eventType, "", "")
}

func readSentinelExitCode(ctx context.Context, path string, readFn ReadFileFunc) (int32, error) {
	data, err := readFn(ctx, path)
	if err != nil {
		return 0, err
	}
	ec, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse exit code %q: %w", string(data), err)
	}
	return int32(ec), nil //nolint:gosec // G115: exit codes are 0-255
}

func backgroundOutputFile(outputDir, taskID string) string {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		outputDir = OutputLogDir
	}
	return path.Join(outputDir, taskID+".log")
}

func ensureOutputDir(ctx context.Context, writeFn WriteFileFunc, outputFile string) error {
	if writeFn == nil {
		return nil
	}
	// Create a marker file to ensure the directory exists.
	return writeFn(ctx, path.Join(path.Dir(outputFile), ".keep"), []byte(""))
}

func ensureOutputFile(ctx context.Context, writeFn WriteFileFunc, path string) error {
	if writeFn == nil {
		return nil
	}
	if err := ensureOutputDir(ctx, writeFn, path); err != nil {
		return err
	}
	return writeFn(ctx, path, []byte(""))
}

func (m *Manager) initializeOutputFile(parentCtx context.Context, task *Task, writeFn WriteFileFunc) {
	if writeFn == nil || task == nil || strings.TrimSpace(task.OutputFile) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 5*time.Second)
	defer cancel()
	if err := ensureOutputFile(ctx, writeFn, task.OutputFile); err != nil {
		m.logger.Warn("background task: initialize output log failed",
			slog.String("task_id", task.ID),
			slog.String("output_file", task.OutputFile),
			slog.Any("error", err),
		)
	}
}

func combineOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + "\n--- stderr ---\n" + stderr
}

func appendLogError(output string, err error) string {
	if err == nil {
		return output
	}
	line := fmt.Sprintf("[error] %v\n", err)
	if output == "" {
		return line
	}
	if strings.HasSuffix(output, "\n") {
		return output + line
	}
	return output + "\n" + line
}

// Kill cancels a running background task. A running agent task records the
// request without publishing a terminal state: its runtime outcome is the
// single authority that wakes waiters as killed, failed, or completed.
func (m *Manager) Kill(taskID string) error {
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	task.mu.Lock()
	if task.Status != TaskRunning && task.Status != TaskQueued {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not running (status: %s)", taskID, task.Status)
	}
	if task.Kind == KindAgent && task.Status == TaskRunning {
		if task.stopRequested {
			task.mu.Unlock()
			return fmt.Errorf("task %s stop already requested", taskID)
		}
		task.stopRequested = true
		task.mu.Unlock()

		task.Cancel()
		m.logger.Info("background agent task stop requested", slog.String("task_id", taskID))
		return nil
	}
	task.Status = TaskKilled
	task.CompletedAt = time.Now()
	task.signalChangedLocked()
	task.mu.Unlock()

	task.Cancel()
	m.logger.Info("background task killed", slog.String("task_id", taskID))
	m.emitTaskEvent(task, TaskEventKilled, "", "")
	return nil
}

// AgentTaskStopRequested reports whether Kill has asked a running managed
// agent to stop. The status intentionally remains running until its runtime
// publishes the authoritative terminal outcome.
func (m *Manager) AgentTaskStopRequested(taskID string) bool {
	task := m.Get(taskID)
	if task == nil || task.Kind != KindAgent {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.stopRequested
}

// Get returns a task by ID, or nil if not found.
func (m *Manager) Get(taskID string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tasks[taskID]
}

// GetForSession returns a task by ID only if it belongs to the provided
// bot+session.
func (m *Manager) GetForSession(botID, sessionID, taskID string) *Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[taskID]
	if task == nil || task.BotID != botID || task.SessionID != sessionID {
		return nil
	}
	return task
}

// ListForSession returns all tasks for a given bot+session, most recent first.
func (m *Manager) ListForSession(botID, sessionID string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*Task
	for _, t := range m.tasks {
		if t.BotID == botID && t.SessionID == sessionID {
			result = append(result, t)
		}
	}
	return result
}

// ListSnapshotsForSession returns lock-safe snapshots for all tasks in a
// bot+session, most recent first.
func (m *Manager) ListSnapshotsForSession(botID, sessionID string) []TaskSnapshot {
	m.mu.Lock()
	tasks := make([]*Task, 0)
	for _, t := range m.tasks {
		if t.BotID == botID && t.SessionID == sessionID {
			tasks = append(tasks, t)
		}
	}
	m.mu.Unlock()

	snapshots := make([]TaskSnapshot, 0, len(tasks))
	for _, task := range tasks {
		snapshots = append(snapshots, task.Snapshot())
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
	})
	return snapshots
}

// KillForSession cancels a running background task only when it belongs to the
// provided bot+session.
func (m *Manager) KillForSession(botID, sessionID, taskID string) error {
	task := m.GetForSession(botID, sessionID, taskID)
	if task == nil {
		return fmt.Errorf("task %s not found", taskID)
	}
	return m.Kill(taskID)
}

// WaitForSessionTask waits until a task reaches a terminal state or needs
// attention, and reports why the wait ended. Besides completed/failed/killed/
// stalled, a running exec task whose output stays quiet for idleThreshold
// returns WaitIdle — the signal that a server-style command has settled and
// its ready banner is in the output tail. idleThreshold <= 0 disables idle
// wake-ups; non-exec kinds (agent, spawn, video) have no output stream, so
// idle never applies to them.
func (m *Manager) WaitForSessionTask(ctx context.Context, botID, sessionID, taskID string, idleThreshold time.Duration) (TaskSnapshot, WaitOutcome, error) {
	task := m.GetForSession(botID, sessionID, taskID)
	if task == nil {
		return TaskSnapshot{}, "", fmt.Errorf("task %s not found", taskID)
	}
	for {
		task.mu.Lock()
		status := task.Status
		stalled := task.stalled && status == TaskRunning
		kind := task.Kind
		lastOutputAt := task.lastOutputAt
		if lastOutputAt.IsZero() {
			lastOutputAt = task.StartedAt
		}
		ch := task.changeChanLocked()
		task.mu.Unlock()

		switch {
		case status == TaskCompleted:
			return task.Snapshot(), WaitCompleted, nil
		case status == TaskFailed:
			return task.Snapshot(), WaitFailed, nil
		case status == TaskKilled:
			return task.Snapshot(), WaitKilled, nil
		case stalled:
			return task.Snapshot(), WaitStalled, nil
		}

		var idleC <-chan time.Time
		var idleTimer *time.Timer
		if idleThreshold > 0 && kind == KindExec && status == TaskRunning {
			remaining := time.Until(lastOutputAt.Add(idleThreshold))
			if remaining <= 0 {
				return task.Snapshot(), WaitIdle, nil
			}
			idleTimer = time.NewTimer(remaining)
			idleC = idleTimer.C
		}
		select {
		case <-ctx.Done():
			if idleTimer != nil {
				idleTimer.Stop()
			}
			return task.Snapshot(), "", ctx.Err()
		case <-ch:
			if idleTimer != nil {
				idleTimer.Stop()
			}
		case <-idleC:
			// Re-loop: output may have grown since the timer was armed, in
			// which case the next iteration re-arms with the new deadline.
		}
	}
}

// RunningTasksSummary returns a text summary of currently running tasks
// for a given bot+session. This is injected into the system prompt so the
// agent knows about ongoing background work.
func (m *Manager) RunningTasksSummary(botID, sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var lines []string
	for _, t := range m.tasks {
		t.mu.Lock()
		matches := t.BotID == botID && t.SessionID == sessionID && t.Status == TaskRunning
		id := t.ID
		desc := t.Description
		command := t.Command
		startedAt := t.StartedAt
		outputFile := t.OutputFile
		t.mu.Unlock()
		if !matches {
			continue
		}
		if desc == "" {
			desc = truncate(command, 80)
		}
		line := fmt.Sprintf("- [%s] %s (started %s ago", id, desc, time.Since(startedAt).Round(time.Second))
		if outputFile != "" {
			line += fmt.Sprintf(", output: %s", outputFile)
		}
		lines = append(lines, line+")")
	}
	if len(lines) == 0 {
		return ""
	}
	return "Currently running background tasks:\n" + joinLines(lines) + "Use wait_until(task_id) to wait for a task, then get_background_status(task_id) to inspect its result.\n"
}

// Cleanup removes completed tasks older than the given duration.
func (m *Manager) Cleanup(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, t := range m.tasks {
		// Task lifecycle fields are protected by task.mu, not Manager.mu. Keep
		// queued and running tasks until their runtime records a terminal state;
		// queued subagent requests may legitimately wait longer than one cleanup
		// interval before promotion.
		t.mu.Lock()
		terminal := t.Status == TaskCompleted || t.Status == TaskFailed || t.Status == TaskKilled
		old := !t.CompletedAt.IsZero() && t.CompletedAt.Before(cutoff)
		t.mu.Unlock()
		if terminal && old {
			delete(m.tasks, id)
		}
	}
}

// StartCleanupLoop periodically removes old completed tasks until done is closed.
func (m *Manager) StartCleanupLoop(done <-chan struct{}, interval, maxAge time.Duration) {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	if maxAge <= 0 {
		maxAge = DefaultTaskRetention
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.Cleanup(maxAge)
		case <-done:
			return
		}
	}
}

// promptPatterns matches common interactive prompt endings that indicate
// a command is waiting for user input.
var promptPatterns = regexp.MustCompile(
	`(?i)(\$ ?$|> ?$|# ?$|password\s*:|passphrase\s*:|y/n\]|yes/no\)|enter .*:|Press .* to continue|Are you sure|Continue\?|Proceed\?)`,
)

// stallWatchdog monitors a background task's output for stalls that might
// indicate the command is waiting for interactive input. If detected, it marks
// the task so waiters and status tools can surface the state.
func (m *Manager) stallWatchdog(ctx context.Context, task *Task) {
	ticker := time.NewTicker(stallCheckInterval)
	defer ticker.Stop()

	var lastLen int
	var stalledSince time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		task.mu.Lock()
		if task.Status != TaskRunning {
			task.mu.Unlock()
			return
		}
		currentLen := task.output.Len()
		// Read tail inline (we already hold the lock).
		tail := task.output.String()
		if len(tail) > maxTailBytes {
			tail = tail[len(tail)-maxTailBytes:]
		}
		task.mu.Unlock()

		if currentLen != lastLen {
			// Output is still growing — reset stall timer.
			lastLen = currentLen
			stalledSince = time.Time{}
			continue
		}

		// Output hasn't grown.
		if stalledSince.IsZero() {
			stalledSince = time.Now()
			continue
		}

		if time.Since(stalledSince) < stallThreshold {
			continue
		}

		// Stalled long enough. Check if the tail looks like an interactive prompt.
		if !promptPatterns.MatchString(tail) {
			continue
		}

		m.logger.Warn("background task appears stalled on interactive prompt",
			slog.String("task_id", task.ID),
		)

		if !task.MarkStalled() {
			return
		}

		m.emitTaskEvent(task, TaskEventStalled, "", "")
		return // only mark stalled once per task
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func detachedContextWithTimeout(parentCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parentCtx), timeout)
}

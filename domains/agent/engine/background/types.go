package background

import (
	"context"
	"strings"
	"sync"
	"time"
)

// TaskStatus represents the lifecycle state of a background task.
type TaskStatus string

const (
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskKilled    TaskStatus = "killed"
)

// Task represents a single background task (a container command execution
// or a spawn subagent batch, per Kind).
type Task struct {
	ID             string
	Kind           TaskKind
	BotID          string
	SessionID      string
	Command        string
	Description    string
	AgentID        string
	AgentSessionID string
	AgentMessage   string
	AgentReport    string
	AgentError     string
	AgentModelID   string
	AgentProvider  string
	AgentFork      bool
	WorkDir        string
	Status         TaskStatus
	ExitCode       int32
	OutputFile     string // path inside the workspace where output is being written
	Result         map[string]any
	Error          string
	StartedAt      time.Time
	CompletedAt    time.Time

	mu           sync.Mutex
	cancel       context.CancelFunc
	stalled      bool            // true once the task appears stuck on interactive input
	changed      chan struct{}   // closed and replaced whenever waiters should re-check task state
	output       strings.Builder // buffered output tail
	lastOutputAt time.Time       // when output last grew; zero means no output yet
	branches     []SpawnBranch   // spawn-kind branch outcomes, set at completion
}

// WaitOutcome explains why a wait on a task returned.
type WaitOutcome string

const (
	WaitCompleted WaitOutcome = "completed"
	WaitFailed    WaitOutcome = "failed"
	WaitKilled    WaitOutcome = "killed"
	WaitStalled   WaitOutcome = "stalled"
	// WaitIdle means the command is still running but produced no new output
	// for the idle threshold — for server-style commands this usually means
	// startup is done and the ready banner is already in the output tail.
	WaitIdle WaitOutcome = "idle"
	// WaitTimeout is never returned by the manager itself; the tool layer uses
	// it when the caller-supplied wait budget elapses before any other outcome.
	WaitTimeout WaitOutcome = "timeout"
)

// TaskSnapshot is a lock-safe, immutable view of a task for handler/UI code.
type TaskSnapshot struct {
	TaskID         string
	Kind           TaskKind
	BotID          string
	SessionID      string
	Command        string
	Description    string
	AgentID        string
	AgentSessionID string
	AgentMessage   string
	AgentReport    string
	AgentError     string
	AgentModelID   string
	AgentProvider  string
	AgentFork      bool
	WorkDir        string
	Status         TaskStatus
	ExitCode       int32
	OutputFile     string
	OutputTail     string
	Result         map[string]any
	Error          string
	Branches       []SpawnBranch
	StartedAt      time.Time
	CompletedAt    time.Time
	LastOutputAt   time.Time
	Duration       time.Duration
	Stalled        bool
}

// Snapshot returns a consistent view of the task without exposing its mutex.
func (t *Task) Snapshot() TaskSnapshot {
	if t == nil {
		return TaskSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	duration := time.Since(t.StartedAt)
	if !t.CompletedAt.IsZero() {
		duration = t.CompletedAt.Sub(t.StartedAt)
	}
	return TaskSnapshot{
		TaskID:         t.ID,
		Kind:           t.Kind,
		BotID:          t.BotID,
		SessionID:      t.SessionID,
		Command:        t.Command,
		Description:    t.Description,
		AgentID:        t.AgentID,
		AgentSessionID: t.AgentSessionID,
		AgentMessage:   t.AgentMessage,
		AgentReport:    t.AgentReport,
		AgentError:     t.AgentError,
		AgentModelID:   t.AgentModelID,
		AgentProvider:  t.AgentProvider,
		AgentFork:      t.AgentFork,
		WorkDir:        t.WorkDir,
		Status:         t.Status,
		ExitCode:       t.ExitCode,
		OutputFile:     t.OutputFile,
		OutputTail:     t.outputTailLocked(),
		Result:         cloneResultMap(t.Result),
		Error:          t.Error,
		Branches:       append([]SpawnBranch(nil), t.branches...),
		StartedAt:      t.StartedAt,
		CompletedAt:    t.CompletedAt,
		LastOutputAt:   t.lastOutputAt,
		Duration:       duration,
		Stalled:        t.stalled && t.Status == TaskRunning,
	}
}

// MarkStalled atomically marks the task as stalled and wakes any waiters.
func (t *Task) MarkStalled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stalled {
		return false
	}
	t.stalled = true
	t.signalChangedLocked()
	return true
}

// Cancel requests cancellation of the task's context.
func (t *Task) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
}

// AppendOutput appends text to the buffered output tail.
// Only the last maxTailBytes are kept.
func (t *Task) AppendOutput(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appendOutputLocked(s)
}

func (t *Task) appendOutputLocked(s string) {
	t.output.WriteString(s)
	t.lastOutputAt = time.Now()
	// Keep tail bounded
	if t.output.Len() > maxTailBytes*2 {
		tail := t.output.String()
		t.output.Reset()
		if len(tail) > maxTailBytes {
			t.output.WriteString(tail[len(tail)-maxTailBytes:])
		} else {
			t.output.WriteString(tail)
		}
	}
}

// OutputTail returns the last portion of collected output.
func (t *Task) OutputTail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outputTailLocked()
}

func (t *Task) outputTailLocked() string {
	s := t.output.String()
	if len(s) > maxTailBytes {
		return s[len(s)-maxTailBytes:]
	}
	return s
}

func (t *Task) changeChanLocked() <-chan struct{} {
	if t.changed == nil {
		t.changed = make(chan struct{})
	}
	return t.changed
}

func (t *Task) signalChangedLocked() {
	if t.changed == nil {
		t.changed = make(chan struct{})
		return
	}
	close(t.changed)
	t.changed = make(chan struct{})
}

const maxTailBytes = 4096

// AdoptResult carries the outcome of a command whose execution was started
// externally (e.g. via ExecStream) and then handed off to the Manager.
//
// ExitReceived distinguishes "the bridge actually sent us an EXIT frame"
// (so ExitCode is the real value the process returned, even if the gRPC
// stream errored out afterwards) from "we never saw an EXIT frame, ExitCode
// is just its zero value". Without this flag, downstream code can't tell
// "the command finished with exit 0 right before the stream died" from
// "we have no idea what the exit code was".
type AdoptResult struct {
	Stdout         string
	Stderr         string
	ExitCode       int32
	ExitReceived   bool
	Err            error
	OutputRecorded bool
}

// TaskEventType identifies a UI-facing background task event.
type TaskEventType string

const (
	TaskEventQueued    TaskEventType = "queued"
	TaskEventStarted   TaskEventType = "started"
	TaskEventOutput    TaskEventType = "output"
	TaskEventCompleted TaskEventType = "completed"
	TaskEventFailed    TaskEventType = "failed"
	TaskEventKilled    TaskEventType = "killed"
	TaskEventStalled   TaskEventType = "stalled"
)

// TaskEvent is emitted for live UI updates. Output events are intentionally
// lightweight and non-persistent; task snapshots remain the source of truth
// for tool-visible state.
type TaskEvent struct {
	Event          TaskEventType `json:"event"`
	TaskID         string        `json:"task_id"`
	Kind           TaskKind      `json:"kind,omitempty"`
	BotID          string        `json:"bot_id,omitempty"`
	SessionID      string        `json:"session_id,omitempty"`
	Command        string        `json:"command,omitempty"`
	AgentID        string        `json:"agent_id,omitempty"`
	AgentSessionID string        `json:"agent_session_id,omitempty"`
	Status         TaskStatus    `json:"status,omitempty"`
	Stream         string        `json:"stream,omitempty"`
	Chunk          string        `json:"chunk,omitempty"`
	Tail           string        `json:"tail,omitempty"`
	OutputFile     string        `json:"output_file,omitempty"`
	ExitCode       int32         `json:"exit_code,omitempty"`
	Duration       string        `json:"duration,omitempty"`
	Stalled        bool          `json:"stalled,omitempty"`
}

func cloneResultMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

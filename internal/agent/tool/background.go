package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
)

const (
	maxWaitDuration                = 300 * time.Second
	backgroundWaitProgressInterval = 30 * time.Second

	minWaitUntilTimeout = 1 * time.Second
	maxWaitUntilTimeout = 600 * time.Second
	minIdleTimeout      = 1 * time.Second
	maxIdleTimeout      = 300 * time.Second
)

// BackgroundProvider exposes background task observation and control tools.
type BackgroundProvider struct {
	bgManager *background.Manager
}

func NewBackgroundProvider(_ *slog.Logger, bgManager *background.Manager) *BackgroundProvider {
	return &BackgroundProvider{
		bgManager: bgManager,
	}
}

func (*BackgroundProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	if ref, ok := available.Ref(ToolListBackground()); ok {
		parts = append(parts, ref+": list background tasks for this session")
	}
	if ref, ok := available.Ref(ToolWait()); ok {
		parts = append(parts, ref+": wait for a short fixed duration when there is no specific task to observe")
	}
	if ref, ok := available.Ref(ToolWaitUntil()); ok {
		parts = append(parts, ref+": observe a background task until it finishes, stalls, goes quiet (idle), or the wait times out")
	}
	if ref, ok := available.Ref(ToolGetBackgroundStatus()); ok {
		parts = append(parts, ref+": inspect a background task and read its result")
	}
	if ref, ok := available.Ref(ToolKillBackground()); ok {
		parts = append(parts, ref+": stop a running or queued background task")
	}
	if len(parts) == 0 {
		return ""
	}
	parts = append(parts, "After starting long work in the background, call `wait_until(task_id)`: it returns with a `reason` (completed/failed/killed/stalled/idle/timeout) and the latest `output_tail`. For finite work (installs, builds, tests), re-wait until it completes, then read `result` via `get_background_status(task_id)`. For servers/watchers that never exit, `reason: \"idle\"` with a ready message in `output_tail` means the service is up — proceed instead of waiting for completion.")
	return usageSection("Background Tasks", parts)
}

func (p *BackgroundProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.bgManager == nil {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolListBackground().String(),
			Description: "List background tasks for the current session.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execListBackground(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolWait().String(),
			Description: "Wait for a fixed duration in seconds. Use wait_until when you have a background task_id.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"duration": map[string]any{"type": "number", "description": "Seconds to wait. Must be > 0 and at most 300.", "minimum": 0, "maximum": 300},
				},
				"required": []string{"duration"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execWait(ctx.Context, sess, inputAsMap(input), ctx.SendProgress)
			},
		},
		{
			Name:        ToolWaitUntil().String(),
			Description: "Observe a background task for a bounded time. Returns with a reason: completed/failed/killed, stalled (interactive prompt), idle (still running but output quiet for idle_timeout), or timeout — always with the latest output_tail. For servers/watchers that never exit (dev server, watch mode), reason 'idle' plus a ready message in output_tail (e.g. a local URL) means the service is up; do not keep waiting for completion.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id":      map[string]any{"type": "string", "description": "Background task ID"},
					"timeout":      map[string]any{"type": "number", "description": "Max seconds to wait before returning with reason 'timeout'. Default 120, max 600. The task keeps running; call wait_until again to keep observing.", "minimum": 1, "maximum": 600},
					"idle_timeout": map[string]any{"type": "number", "description": "Seconds of output silence after which a running command returns with reason 'idle'. Default 20, max 300. Only applies to exec tasks.", "minimum": 1, "maximum": 300},
				},
				"required": []string{"task_id"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execWaitUntil(ctx.Context, sess, inputAsMap(input), ctx.SendProgress)
			},
		},
		{
			Name:        ToolGetBackgroundStatus().String(),
			Description: "Get the status and details of a background task. For completed agent/spawn tasks, read the result field.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string", "description": "Background task ID"},
				},
				"required": []string{"task_id"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execGetBackgroundStatus(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolKillBackground().String(),
			Description: "Kill a running or queued background task.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{"type": "string", "description": "Background task ID"},
				},
				"required": []string{"task_id"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execKillBackground(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *BackgroundProvider) execListBackground(_ context.Context, session SessionContext, _ map[string]any) (any, error) {
	snapshots := p.bgManager.ListSnapshotsForSession(session.BotID, session.SessionID)
	entries := make([]map[string]any, 0, len(snapshots))
	for _, s := range snapshots {
		entry := map[string]any{
			"task_id":     s.TaskID,
			"kind":        string(s.Kind),
			"description": s.Description,
			"status":      statusString(s),
			"started_at":  session.FormatTime(s.StartedAt),
		}
		if s.Kind == background.KindAgent {
			entry["agent_id"] = s.AgentID
			entry["session_id"] = s.AgentSessionID
		}
		if s.Kind == background.KindExec {
			entry["command"] = truncateStr(s.Command, 120)
			entry["output_file"] = s.OutputFile
		}
		entries = append(entries, entry)
	}
	return map[string]any{"tasks": entries, "count": len(entries)}, nil
}

func (*BackgroundProvider) execWait(ctx context.Context, _ SessionContext, args map[string]any, sendProgress func(any)) (any, error) {
	duration, err := durationArg(args, "duration")
	if err != nil {
		return nil, err
	}
	progress := func() {
		emitWaitProgress(sendProgress, map[string]any{
			"status":   "waiting",
			"duration": duration.Seconds(),
		})
	}
	progress()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(backgroundWaitProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return map[string]any{"ok": false, "duration": duration.Seconds(), "error": ctx.Err().Error()}, ctx.Err()
		case <-timer.C:
			return map[string]any{"ok": true, "duration": duration.Seconds()}, nil
		case <-ticker.C:
			progress()
		}
	}
}

func (p *BackgroundProvider) execWaitUntil(ctx context.Context, session SessionContext, args map[string]any, sendProgress func(any)) (any, error) {
	taskID := strings.TrimSpace(StringArg(args, "task_id"))
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	timeout, err := optionalDurationArg(args, "timeout", background.DefaultWaitTimeout, minWaitUntilTimeout, maxWaitUntilTimeout)
	if err != nil {
		return nil, err
	}
	idleThreshold, err := optionalDurationArg(args, "idle_timeout", background.DefaultIdleThreshold, minIdleTimeout, maxIdleTimeout)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, outcome, err := p.waitForSessionTaskWithProgress(waitCtx, session.BotID, session.SessionID, taskID, idleThreshold, sendProgress)
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, err
		}
		// The wait budget elapsed; the task itself is still running.
		outcome = background.WaitTimeout
	}
	result := map[string]any{
		"task_id":    s.TaskID,
		"kind":       string(s.Kind),
		"status":     statusString(s),
		"reason":     string(outcome),
		"stalled":    s.Stalled,
		"started_at": session.FormatTime(s.StartedAt),
	}
	if s.OutputTail != "" {
		result["output_tail"] = s.OutputTail
	}
	if s.CompletedAt.IsZero() {
		return result, nil
	}
	result["completed_at"] = session.FormatTime(s.CompletedAt)
	result["duration"] = s.Duration.Round(time.Millisecond).String()
	return result, nil
}

func (p *BackgroundProvider) waitForSessionTaskWithProgress(ctx context.Context, botID, sessionID, taskID string, idleThreshold time.Duration, sendProgress func(any)) (background.TaskSnapshot, background.WaitOutcome, error) {
	if sendProgress == nil {
		return p.bgManager.WaitForSessionTask(ctx, botID, sessionID, taskID, idleThreshold)
	}

	type waitResult struct {
		snapshot background.TaskSnapshot
		outcome  background.WaitOutcome
		err      error
	}
	resultCh := make(chan waitResult, 1)
	go func() {
		s, outcome, err := p.bgManager.WaitForSessionTask(ctx, botID, sessionID, taskID, idleThreshold)
		resultCh <- waitResult{snapshot: s, outcome: outcome, err: err}
	}()

	progress := func() {
		emitWaitProgress(sendProgress, map[string]any{
			"status":  "waiting",
			"task_id": taskID,
		})
	}
	progress()
	ticker := time.NewTicker(backgroundWaitProgressInterval)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result.snapshot, result.outcome, result.err
		case <-ctx.Done():
			// Give the waiter goroutine a moment to hand back the snapshot it
			// captured at cancellation, so timeout results still carry a tail.
			select {
			case result := <-resultCh:
				return result.snapshot, result.outcome, result.err
			case <-time.After(time.Second):
				return background.TaskSnapshot{}, "", ctx.Err()
			}
		case <-ticker.C:
			progress()
		}
	}
}

func emitWaitProgress(sendProgress func(any), payload map[string]any) {
	if sendProgress != nil {
		sendProgress(payload)
	}
}

func (p *BackgroundProvider) execGetBackgroundStatus(_ context.Context, session SessionContext, args map[string]any) (any, error) {
	taskID := strings.TrimSpace(StringArg(args, "task_id"))
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	task := p.bgManager.GetForSession(session.BotID, session.SessionID, taskID)
	if task == nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return backgroundStatusMap(session, task.Snapshot()), nil
}

func (p *BackgroundProvider) execKillBackground(_ context.Context, session SessionContext, args map[string]any) (any, error) {
	taskID := strings.TrimSpace(StringArg(args, "task_id"))
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	if err := p.bgManager.KillForSession(session.BotID, session.SessionID, taskID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf("Task %s has been killed.", taskID)}, nil
}

func backgroundStatusMap(session SessionContext, s background.TaskSnapshot) map[string]any {
	result := map[string]any{
		"task_id":     s.TaskID,
		"kind":        string(s.Kind),
		"description": s.Description,
		"status":      statusString(s),
		"started_at":  session.FormatTime(s.StartedAt),
		"stalled":     s.Stalled,
	}
	if !s.CompletedAt.IsZero() {
		result["completed_at"] = session.FormatTime(s.CompletedAt)
		result["duration"] = s.Duration.Round(time.Millisecond).String()
	}
	switch s.Kind {
	case background.KindAgent:
		result["agent_id"] = s.AgentID
		result["session_id"] = s.AgentSessionID
		if s.AgentModelID != "" {
			result["model_id"] = s.AgentModelID
		}
		if s.AgentProvider != "" {
			result["provider"] = s.AgentProvider
		}
		result["fork"] = s.AgentFork
		if s.AgentMessage != "" {
			result["input"] = s.AgentMessage
		}
		result["result"] = s.AgentReport
		if s.AgentError != "" {
			result["error"] = s.AgentError
		}
	case background.KindSpawn:
		branches := make([]map[string]any, 0, len(s.Branches))
		for _, br := range s.Branches {
			item := map[string]any{
				"task":   br.Task,
				"status": string(br.Status),
				"result": br.Report,
			}
			if br.ChildSessionID != "" {
				item["session_id"] = br.ChildSessionID
			}
			if br.Error != "" {
				item["error"] = br.Error
			}
			branches = append(branches, item)
		}
		result["result"] = map[string]any{"branches": branches}
		// Keep the branch list at the top level for existing UI rendering.
		if len(branches) > 0 {
			result["branches"] = branches
		}
	case background.KindVideo:
		videoResult := make(map[string]any, len(s.Result)+1)
		for k, v := range s.Result {
			videoResult[k] = v
		}
		if s.Error != "" {
			videoResult["error"] = s.Error
			result["error"] = s.Error
		}
		result["result"] = videoResult
		if s.OutputTail != "" {
			result["output_tail"] = s.OutputTail
		}
	default:
		result["command"] = s.Command
		result["output_file"] = s.OutputFile
		// The tail is live while the task runs — it is how the agent sees a
		// server's ready banner without waiting for the process to exit.
		result["output_tail"] = s.OutputTail
		execResult := map[string]any{"output_file": s.OutputFile, "output_tail": s.OutputTail}
		if s.Status != background.TaskRunning && s.Status != background.TaskQueued {
			result["exit_code"] = s.ExitCode
			execResult["exit_code"] = s.ExitCode
		}
		result["result"] = execResult
	}
	return result
}

func statusString(s background.TaskSnapshot) string {
	if s.Stalled {
		return "stalled"
	}
	return string(s.Status)
}

func durationArg(args map[string]any, key string) (time.Duration, error) {
	raw, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	duration, err := secondsValue(raw, key)
	if err != nil {
		return 0, err
	}
	if duration > maxWaitDuration {
		duration = maxWaitDuration
	}
	return duration, nil
}

// optionalDurationArg reads a seconds argument, falling back to def when the
// key is absent and clamping present values into [minD, maxD].
func optionalDurationArg(args map[string]any, key string, def, minD, maxD time.Duration) (time.Duration, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return def, nil
	}
	duration, err := secondsValue(raw, key)
	if err != nil {
		return 0, err
	}
	if duration < minD {
		duration = minD
	}
	if duration > maxD {
		duration = maxD
	}
	return duration, nil
}

func secondsValue(raw any, key string) (time.Duration, error) {
	var seconds float64
	switch v := raw.(type) {
	case float64:
		seconds = v
	case float32:
		seconds = float64(v)
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case jsonNumber:
		parsed, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", key, err)
		}
		seconds = parsed
	default:
		return 0, fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, fmt.Errorf("%s must be > 0", key)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

type jsonNumber interface {
	Float64() (float64, error)
}

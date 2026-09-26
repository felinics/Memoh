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
	"github.com/felinics/memoh/internal/agent/toolexec"
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
	parts = append(parts, "After starting long work in the background, call `wait_until(task_id)`: it returns with a `reason` (completed/failed/killed/unknown/stalled/idle/timeout) and the latest `output_tail`. For finite work (installs, builds, tests), re-wait until it completes, then read `result` via `get_background_status(task_id)`. For servers/watchers that never exit, `reason: \"idle\"` with a ready message in `output_tail` means the service is up — proceed instead of waiting for completion.")
	return usageSection("Background Tasks", parts)
}

// Argument shapes of the background tools. The json tag names the property
// and marks it optional; the jsonschema tag is the description the model reads.
// Seconds arrive as pointers so an omitted value is told apart from zero.
type (
	listBackgroundArgs struct{}
	waitArgs           struct {
		Duration *float64 `json:"duration" jsonschema:"Seconds to wait. Must be > 0 and at most 300."`
	}
	waitUntilArgs struct {
		TaskID      string   `json:"task_id" jsonschema:"Background task ID"`
		Timeout     *float64 `json:"timeout,omitempty" jsonschema:"Max seconds to wait before returning with reason 'timeout'. Default 120, max 600. The task keeps running; call wait_until again to keep observing."`
		IdleTimeout *float64 `json:"idle_timeout,omitempty" jsonschema:"Seconds of output silence after which a running command returns with reason 'idle'. Default 20, max 300. Only applies to exec tasks."`
	}
	taskArgs struct {
		TaskID string `json:"task_id" jsonschema:"Background task ID"`
	}
)

func (p *BackgroundProvider) Tools(_ context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.bgManager == nil {
		return nil, nil
	}
	sess := session
	return []toolexec.Tool{
		toolexec.Define(ToolListBackground().String(), "List background tasks for the current session.",
			func(ctx *toolexec.ToolExecContext, _ listBackgroundArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execListBackground(ctx.Context, sess))
			}),
		toolexec.Define(ToolWait().String(), "Wait for a fixed duration in seconds. Use wait_until when you have a background task_id.",
			func(ctx *toolexec.ToolExecContext, args waitArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execWait(ctx.Context, sess, args, ctx.SendProgress))
			}, toolexec.Range("duration", 0, 300)),
		toolexec.Define(ToolWaitUntil().String(), "Observe a background task for a bounded time. Returns with a reason: completed/failed/killed, unknown (execution connection lost; refresh dependency state before retrying), stalled (interactive prompt), idle (still running but output quiet for idle_timeout), or timeout — always with the latest output_tail. For servers/watchers that never exit (dev server, watch mode), reason 'idle' plus a ready message in output_tail (e.g. a local URL) means the service is up; do not keep waiting for completion.",
			func(ctx *toolexec.ToolExecContext, args waitUntilArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execWaitUntil(ctx.Context, sess, args, ctx.SendProgress))
			}, toolexec.Range("timeout", 1, 600), toolexec.Range("idle_timeout", 1, 300)),
		toolexec.Define(ToolGetBackgroundStatus().String(), "Get the status and details of a background task. For completed agent/spawn tasks, read the result field.",
			func(ctx *toolexec.ToolExecContext, args taskArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execGetBackgroundStatus(ctx.Context, sess, args))
			}),
		toolexec.Define(ToolKillBackground().String(), "Kill a running or queued background task.",
			func(ctx *toolexec.ToolExecContext, args taskArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execKillBackground(ctx.Context, sess, args))
			}),
	}, nil
}

func (p *BackgroundProvider) execListBackground(_ context.Context, session SessionContext) (any, error) {
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

func (*BackgroundProvider) execWait(ctx context.Context, _ SessionContext, args waitArgs, sendProgress func(sdk.ToolOutput)) (any, error) {
	duration, err := requiredSeconds(args.Duration, "duration", maxWaitDuration)
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

func (p *BackgroundProvider) execWaitUntil(ctx context.Context, session SessionContext, args waitUntilArgs, sendProgress func(sdk.ToolOutput)) (any, error) {
	taskID := strings.TrimSpace(args.TaskID)
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	timeout, err := optionalSeconds(args.Timeout, "timeout", background.DefaultWaitTimeout, minWaitUntilTimeout, maxWaitUntilTimeout)
	if err != nil {
		return nil, err
	}
	idleThreshold, err := optionalSeconds(args.IdleTimeout, "idle_timeout", background.DefaultIdleThreshold, minIdleTimeout, maxIdleTimeout)
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

func (p *BackgroundProvider) waitForSessionTaskWithProgress(ctx context.Context, botID, sessionID, taskID string, idleThreshold time.Duration, sendProgress func(sdk.ToolOutput)) (background.TaskSnapshot, background.WaitOutcome, error) {
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

func emitWaitProgress(sendProgress func(sdk.ToolOutput), payload map[string]any) {
	if sendProgress != nil {
		sendProgress(toolexec.OutputFromValue(payload))
	}
}

func (p *BackgroundProvider) execGetBackgroundStatus(_ context.Context, session SessionContext, args taskArgs) (any, error) {
	taskID := strings.TrimSpace(args.TaskID)
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	task := p.bgManager.GetForSession(session.BotID, session.SessionID, taskID)
	if task == nil {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return backgroundStatusMap(session, task.Snapshot()), nil
}

func (p *BackgroundProvider) execKillBackground(_ context.Context, session SessionContext, args taskArgs) (any, error) {
	taskID := strings.TrimSpace(args.TaskID)
	if taskID == "" {
		return nil, errors.New("task_id is required")
	}
	if err := p.bgManager.KillForSession(session.BotID, session.SessionID, taskID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "message": fmt.Sprintf("Stop requested for task %s. Check its status to confirm the execution outcome.", taskID)}, nil
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

// requiredSeconds reads a required seconds argument, clamping it to maxD.
func requiredSeconds(value *float64, key string, maxD time.Duration) (time.Duration, error) {
	if value == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	duration, err := secondsDuration(*value, key)
	if err != nil {
		return 0, err
	}
	if duration > maxD {
		duration = maxD
	}
	return duration, nil
}

// optionalSeconds reads a seconds argument, falling back to def when the
// value is absent and clamping present values into [minD, maxD].
func optionalSeconds(value *float64, key string, def, minD, maxD time.Duration) (time.Duration, error) {
	if value == nil {
		return def, nil
	}
	duration, err := secondsDuration(*value, key)
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

func secondsDuration(seconds float64, key string) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, fmt.Errorf("%s must be > 0", key)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

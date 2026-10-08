package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/reasoning"
)

// idleCancel watches the currently executing phase. Model silence, tool
// silence and human decisions have separate owners; completed tools never
// increase the budget of later model calls.
type idleCancel struct {
	cancel      context.CancelCauseFunc
	timer       *time.Timer
	mu          sync.Mutex
	fired       bool
	stopped     bool
	baseTimeout time.Duration
	maxTimeout  time.Duration
	toolTimeout time.Duration
	deadline    time.Time
	tools       map[string]idleTool
	decisions   map[string]string
}

type idleTool struct {
	name         string
	lastProgress time.Time
}

func (ic *idleCancel) Reset() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.rearmLocked(time.Now())
}

// Observe is called before publishing an event. Waiting for a decision pauses
// only that tool's watchdog, so a parallel tool can still time out. The decision
// flow owns expiry and always remains subject to explicit cancellation.
func (ic *idleCancel) Observe(ev native.StreamEvent) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	now := time.Now()
	id := ev.ToolCallID
	if id == "" {
		id = ev.ToolName
	}
	switch ev.Type {
	case native.EventToolCallStart:
		ic.tools[id] = idleTool{name: ev.ToolName, lastProgress: now}
	case native.EventToolCallProgress:
		if tool, ok := ic.tools[id]; ok {
			tool.lastProgress = now
			ic.tools[id] = tool
		}
	case native.EventToolCallEnd:
		delete(ic.tools, id)
	case native.EventToolApprovalRequest, native.EventUserInputRequest:
		key := string(ev.Type) + ":" + ev.ApprovalID + ":" + ev.UserInputID
		if ev.Status == "pending" || ev.Status == "" {
			ic.decisions[key] = id
		} else {
			delete(ic.decisions, key)
			if tool, ok := ic.tools[id]; ok {
				tool.lastProgress = now
				ic.tools[id] = tool
			}
		}
	case native.EventProgress:
		if ev.ProgressStatus == "spawn_running" {
			// This keeps the parent wait alive; each child has its own watchdog.
			for key, tool := range ic.tools {
				if tool.name == "spawn_agent" || tool.name == "send_message" {
					tool.lastProgress = now
					ic.tools[key] = tool
				}
			}
		}
	}
	ic.rearmLocked(now)
}

func (ic *idleCancel) rearmLocked(now time.Time) {
	if ic.fired || ic.stopped {
		return
	}
	ic.timer.Stop()
	ic.deadline = time.Time{}
	if len(ic.tools) == 0 && len(ic.decisions) == 0 {
		ic.deadline = now.Add(ic.currentTimeout())
	} else {
		for id, tool := range ic.tools {
			waiting := false
			for _, decisionTool := range ic.decisions {
				if decisionTool == id {
					waiting = true
					break
				}
			}
			if waiting {
				continue
			}
			deadline := tool.lastProgress.Add(ic.toolTimeout)
			if ic.deadline.IsZero() || deadline.Before(ic.deadline) {
				ic.deadline = deadline
			}
		}
	}
	if !ic.deadline.IsZero() {
		ic.timer.Reset(time.Until(ic.deadline))
	}
}

func (ic *idleCancel) expire() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.fired || ic.stopped || ic.deadline.IsZero() {
		return
	}
	// AfterFunc's callback can race a rearm; consult the current deadline.
	if remaining := time.Until(ic.deadline); remaining > 0 {
		ic.timer.Reset(remaining)
		return
	}
	ic.fired = true
	code := apperror.CodeAgentResponseTimeout
	if len(ic.tools) > 0 {
		code = apperror.CodeAgentToolTimeout
	}
	ic.cancel(apperror.Wrap(code, context.DeadlineExceeded, nil))
}

func (ic *idleCancel) Stop() {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.stopped = true
	ic.timer.Stop()
}

func (ic *idleCancel) DidFire() bool {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.fired
}

func (ic *idleCancel) currentTimeout() time.Duration {
	if ic.maxTimeout > 0 && ic.baseTimeout > ic.maxTimeout {
		return ic.maxTimeout
	}
	return ic.baseTimeout
}

const (
	defaultIdleTimeout    = 5 * time.Minute
	defaultIdleTimeoutMax = 15 * time.Minute
	// Leave headroom for a foreground command to reach its ten-minute soft limit.
	defaultToolIdleTimeout = 15 * time.Minute
)

// withIdleTimeout starts a model-phase watchdog. Optional durations are the
// model window and its cap; tool execution uses its own inactivity window.
func withIdleTimeout(parent context.Context, timeouts ...time.Duration) (context.Context, *idleCancel) {
	base, maxTimeout := defaultIdleTimeout, defaultIdleTimeoutMax
	if len(timeouts) > 0 && timeouts[0] > 0 {
		base = timeouts[0]
	}
	if len(timeouts) > 1 && timeouts[1] > 0 {
		maxTimeout = timeouts[1]
	}
	ctx, cancel := context.WithCancelCause(parent)
	ic := &idleCancel{
		cancel: cancel, baseTimeout: base, maxTimeout: maxTimeout,
		toolTimeout: defaultToolIdleTimeout, tools: make(map[string]idleTool), decisions: make(map[string]string),
	}
	ic.deadline = time.Now().Add(ic.currentTimeout())
	ic.timer = time.AfterFunc(ic.currentTimeout(), ic.expire)
	return ctx, ic
}

func (s *Service) withStreamIdleTimeout(parent context.Context, effort string) (context.Context, *idleCancel) {
	base := defaultIdleTimeout
	maxTimeout := defaultIdleTimeoutMax
	if s != nil && s.streamIdleTimeout > 0 {
		base = s.streamIdleTimeout
	}
	if s != nil && s.streamIdleTimeoutMax > 0 {
		maxTimeout = s.streamIdleTimeoutMax
	}
	return withIdleTimeout(parent, scaleIdleTimeoutForEffort(base, effort), maxTimeout)
}

func scaleIdleTimeoutForEffort(base time.Duration, effort string) time.Duration {
	if base <= 0 {
		base = defaultIdleTimeout
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case reasoning.EffortMax:
		return base * 8
	case reasoning.EffortXHigh:
		return base * 6
	case reasoning.EffortHigh:
		return base * 4
	case reasoning.EffortMedium:
		return base * 2
	default:
		return base
	}
}

func reasoningEffortForIdle(cfg native.RunConfig) string {
	if cfg.ReasoningConfig != nil {
		if effort := strings.TrimSpace(cfg.ReasoningConfig.Effort); effort != "" {
			return effort
		}
	}
	return strings.TrimSpace(cfg.ReasoningRequestedEffort)
}

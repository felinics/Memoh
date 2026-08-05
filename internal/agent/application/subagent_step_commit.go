package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/runtimefence"
)

// SubagentStepCommit returns the per-step persistence callback for one spawned
// agent run, or nil when incremental persistence is unavailable — no admitted
// handle on the context, no fence, no persisted request row to bind steps to,
// or a message service that cannot write fenced steps. A nil return means the
// spawn path keeps its terminal-snapshot persistence, so this is a capability
// probe as much as a constructor.
//
// The callback persists exactly what the legacy terminal path persists — every
// non-user message of the step, marshalled whole — only earlier: each complete
// step lands in one fenced transaction instead of the whole run landing at the
// end. onPersisted fires after a step has durably committed; the spawn provider
// uses it to stop retrying an attempt that already produced durable output.
func (s *Service) SubagentStepCommit(ctx context.Context, botID, sessionID, modelID, turnRequestMessageID string, onPersisted func()) func(context.Context, int, *sdk.StepResult) error {
	if s == nil || s.messageService == nil {
		return nil
	}
	persister, ok := s.messageService.(messagepkg.AgentStepPersister)
	if !ok {
		return nil
	}
	handle, ok := SubagentRunHandleFromContext(ctx)
	if !ok || strings.TrimSpace(handle.RunID) == "" || handle.FencingToken <= 0 {
		return nil
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return nil
	}
	if strings.TrimSpace(botID) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	// No request row means the pre-run user message write failed. Committing
	// steps anyway would file them under no turn and — because the spawn path
	// then reports the run as persisted — permanently drop the task prompt.
	// Decline instead: terminal persistence still writes the user message and
	// the whole run behind it.
	if strings.TrimSpace(turnRequestMessageID) == "" {
		return nil
	}
	committer := &subagentStepCommitter{
		persister:            persister,
		runID:                handle.RunID,
		botID:                botID,
		sessionID:            sessionID,
		modelID:              modelID,
		turnRequestMessageID: strings.TrimSpace(turnRequestMessageID),
		onPersisted:          onPersisted,
	}
	return committer.commit
}

// subagentStepCommitter appends a spawned agent's complete steps to its
// thread's history as they finish. Unlike agentStepCommitter it carries no
// ChatRequest: the subagent path has no resolved chat context, and its user
// message is persisted by the spawn provider before execution starts.
type subagentStepCommitter struct {
	persister messagepkg.AgentStepPersister
	runID     string
	botID     string
	sessionID string
	modelID   string
	// turnRequestMessageID binds every step row to the task's persisted user
	// message, so the whole run files under the turn admission allocated
	// instead of splitting into history-minted turns.
	turnRequestMessageID string
	onPersisted          func()

	mu       sync.Mutex
	nextStep int // In-process ordering guard, not a durable replay cursor.
}

func (c *subagentStepCommitter) commit(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
	if c == nil || step == nil {
		return errors.New("complete agent step is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if stepIndex != c.nextStep {
		return fmt.Errorf("unexpected agent step %d, want %d", stepIndex, c.nextStep)
	}
	inputs := make([]messagepkg.PersistInput, 0, len(step.Messages))
	for _, msg := range step.Messages {
		if msg.Role == sdk.MessageRoleUser {
			continue
		}
		content, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		var usage json.RawMessage
		if msg.Usage != nil {
			usage, _ = json.Marshal(msg.Usage)
		}
		inputs = append(inputs, messagepkg.PersistInput{
			BotID:                c.botID,
			SessionID:            c.sessionID,
			Role:                 string(msg.Role),
			Content:              content,
			Usage:                usage,
			ModelID:              c.modelID,
			RunID:                c.runID,
			TurnRequestMessageID: c.turnRequestMessageID,
		})
	}
	if len(inputs) == 0 {
		c.nextStep++
		return nil
	}
	if _, err := c.persister.PersistAgentStep(context.WithoutCancel(ctx), messagepkg.AgentStep{
		RunID: c.runID, Messages: inputs,
	}); err != nil {
		return err
	}
	c.nextStep++
	if c.onPersisted != nil {
		c.onPersisted()
	}
	return nil
}

// SubagentRunObserver returns a per-event publisher that feeds one spawned
// agent run's stream into the session runtime, or nil when there is nothing to
// publish to — no runtime configured, or a context without an admitted handle.
//
// The returned function mirrors forwardWSStreamEvents' publishing discipline:
// events are published on a context that survives the run's cancellation
// (an aborted run's final events are exactly the ones a subscriber must see),
// and a lost ownership stops publishing outright because every later event
// would fail identically.
func (s *Service) SubagentRunObserver(ctx context.Context) func(native.StreamEvent) {
	if s == nil || s.decisionRuntime == nil {
		return nil
	}
	handle, ok := SubagentRunHandleFromContext(ctx)
	if !ok || strings.TrimSpace(handle.RunID) == "" || handle.FencingToken <= 0 {
		return nil
	}
	publishCtx := context.WithoutCancel(ctx)
	var lost atomic.Bool
	return func(event native.StreamEvent) {
		if lost.Load() {
			return
		}
		if _, err := s.decisionRuntime.HandleAgentEvent(publishCtx, handle, event); err != nil {
			if errors.Is(err, sessionruntime.ErrRunOwnershipLost) {
				lost.Store(true)
			}
			if s.logger != nil {
				s.logger.Warn("publish subagent runtime event failed",
					slog.Any("error", err),
					slog.String("bot_id", handle.BotID),
					slog.String("session_id", handle.SessionID),
					slog.String("run_id", handle.RunID),
				)
			}
		}
	}
}

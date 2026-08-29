package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

// agentStepCommitter bridges Twilight's complete-step barrier to history
// persistence. It is intentionally enabled only for admitted, fenced turns;
// legacy calls and replacement flows keep their terminal-snapshot behavior.
type agentStepCommitter struct {
	service         *Service
	req             ChatRequest
	rc              resolvedContext
	persister       messagepkg.AgentStepPersister
	reasoningTiming *reasoningTimingTracker

	mu                   sync.Mutex
	turnRequestMessageID string
	persisted            []messagepkg.Message
	memoryPersisted      []messagepkg.Message
	messages             []ModelMessage
	nextStep             int // In-process ordering guard, not a durable replay cursor.
	commitErr            error
	finalized            bool
}

func (s *Service) newAgentStepCommitter(ctx context.Context, req ChatRequest, rc resolvedContext) *agentStepCommitter {
	if s == nil || s.messageService == nil || strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.ThreadID) == "" ||
		req.SkipHistoryTurn || req.ReusePersistedUserMessage {
		return nil
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return nil
	}
	persister, ok := s.messageService.(messagepkg.AgentStepPersister)
	if !ok {
		return nil
	}
	requestMessageID := ""
	if req.UserMessagePersisted {
		requestMessageID = strings.TrimSpace(req.PersistedUserMessageID)
		if requestMessageID == "" {
			return nil
		}
	} else if strings.TrimSpace(req.TurnID) == "" || req.TurnPosition == nil {
		return nil
	}
	return &agentStepCommitter{
		service: s, req: req, rc: rc, persister: persister,
		turnRequestMessageID: requestMessageID,
	}
}

func (c *agentStepCommitter) commit(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
	return c.persist(ctx, stepIndex, step, false)
}

func (c *agentStepCommitter) interrupt(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
	return c.persist(ctx, stepIndex, step, true)
}

func (c *agentStepCommitter) persist(ctx context.Context, stepIndex int, step *sdk.StepResult, interrupted bool) error {
	if c == nil || step == nil {
		return errors.New("agent step is missing")
	}
	messages := sdkMessagesToModelMessages(step.Messages)
	timingState := "completed"
	if interrupted {
		timingState = "interrupted"
	}
	reasoningTiming := c.reasoningTiming.take(timingState)

	c.mu.Lock()
	defer c.mu.Unlock()
	// A failed interrupted checkpoint is reported to its caller but never
	// recorded as a commit failure: the turn is already ending, and losing an
	// unfinished snapshot must not turn an abort into a turn error.
	fail := func(err error) error {
		if !interrupted {
			c.commitErr = err
		}
		return err
	}
	if stepIndex != c.nextStep {
		return fail(fmt.Errorf("unexpected agent step %d, want %d", stepIndex, c.nextStep))
	}
	if !hasPersistableAssistantOutput(messages) {
		c.nextStep++
		return nil
	}
	if stepIndex == 0 && !c.req.UserMessagePersisted {
		messages = prependTurnUserMessage(c.req, messages)
	}
	storeReq := c.req
	// Outbound assets are linked once after the stream closes; including the
	// collector in every step would attach the same accumulated assets again.
	storeReq.OutboundAssetCollector = nil
	opts := storeRoundOptions{
		AllowPendingToolCalls: step.DeferredToolApproval != nil,
		ContextLifecycle:      c.rc.runConfig.ContextLifecycle,
		ReasoningTiming:       reasoningTiming,
	}
	if interrupted {
		opts.MessageMetadataByIndex = make(map[int]map[string]any)
		for i, message := range messages {
			if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
				opts.MessageMetadataByIndex[i] = map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true}
			}
		}
	}
	opts = opts.withContextLifecycleMetadata(c.service.logger, storeReq, messages)
	inputs, err := c.service.buildPersistInputs(context.WithoutCancel(ctx), storeReq, messages, c.rc.model.ID, opts)
	if err != nil {
		return fail(err)
	}
	for i := range inputs {
		inputs[i].TurnRequestMessageID = c.turnRequestMessageID
	}
	persisted, err := c.persister.PersistAgentStep(context.WithoutCancel(ctx), messagepkg.AgentStep{
		RunID: c.req.RunID, Messages: inputs, Interrupted: interrupted,
	})
	if err != nil {
		return fail(err)
	}
	c.rc.runConfig.ContextLifecycle.SetAssistantMessageID(lastPersistedAssistantMessageID(persisted))
	c.nextStep++
	for _, message := range persisted {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			c.turnRequestMessageID = message.ID
		}
	}
	c.persisted = append(c.persisted, persisted...)
	if !interrupted {
		// Unfinished reasoning/text is history context, not a fact source for
		// asynchronous long-term memory extraction.
		c.memoryPersisted = append(c.memoryPersisted, persisted...)
		c.messages = append(c.messages, messages...)
	}
	return nil
}

func (c *agentStepCommitter) err() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitErr
}

func (c *agentStepCommitter) finish(ctx context.Context, inputTokens int) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.finalized {
		c.mu.Unlock()
		return nil
	}
	c.finalized = true
	persisted := append([]messagepkg.Message(nil), c.persisted...)
	memoryPersisted := append([]messagepkg.Message(nil), c.memoryPersisted...)
	messages := append([]ModelMessage(nil), c.messages...)
	c.mu.Unlock()
	if len(persisted) == 0 {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	if err := c.service.persistSessionWorkspaceTarget(ctx, c.req); err != nil {
		return err
	}
	if c.req.OutboundAssetCollector != nil {
		c.service.LinkOutboundAssets(ctx, c.req.BotID, c.req.ThreadID, outboundAssetRefsToMessageRefs(c.req.OutboundAssetCollector()))
	}
	if !c.req.SkipMemoryExtraction && len(memoryPersisted) == len(messages) && len(memoryPersisted) > 0 {
		go c.service.storeMemory(ctx, c.req, memoryPersisted)
	}
	if inputTokens > 0 {
		go c.service.maybeCompact(ctx, c.req, c.rc, inputTokens)
	}
	return nil
}

func (c *agentStepCommitter) persistedMessages() []messagepkg.Message {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]messagepkg.Message(nil), c.persisted...)
}

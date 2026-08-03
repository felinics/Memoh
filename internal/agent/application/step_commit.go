package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/memohai/twilight-ai/sdk"

	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/runtimefence"
)

// agentStepCommitter bridges Twilight's complete-step barrier to history
// persistence. It is intentionally enabled only for admitted, fenced turns;
// legacy calls and replacement flows keep their terminal-snapshot behavior.
type agentStepCommitter struct {
	service   *Service
	req       ChatRequest
	rc        resolvedContext
	persister messagepkg.AgentStepPersister

	mu                   sync.Mutex
	turnRequestMessageID string
	persisted            []messagepkg.Message
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
	if c == nil || step == nil {
		return errors.New("complete agent step is missing")
	}
	messages := sdkMessagesToModelMessages(step.Messages)

	c.mu.Lock()
	defer c.mu.Unlock()
	if stepIndex != c.nextStep {
		err := fmt.Errorf("unexpected agent step %d, want %d", stepIndex, c.nextStep)
		c.commitErr = err
		return err
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
	inputs, err := c.service.buildPersistInputs(context.WithoutCancel(ctx), storeReq, messages, c.rc.model.ID, storeRoundOptions{
		AllowPendingToolCalls: step.DeferredToolApproval != nil,
	})
	if err != nil {
		c.commitErr = err
		return err
	}
	for i := range inputs {
		inputs[i].TurnRequestMessageID = c.turnRequestMessageID
	}
	persisted, err := c.persister.PersistAgentStep(context.WithoutCancel(ctx), messagepkg.AgentStep{
		RunID: c.req.RunID, Messages: inputs,
	})
	if err != nil {
		c.commitErr = err
		return err
	}
	c.nextStep++
	for _, message := range persisted {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			c.turnRequestMessageID = message.ID
		}
	}
	c.persisted = append(c.persisted, persisted...)
	c.messages = append(c.messages, messages...)
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
	if !c.req.SkipMemoryExtraction {
		go c.service.storeMemory(ctx, c.req, messages)
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

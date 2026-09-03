package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	sdk "github.com/felinics/twilight/sdk"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

// agentStepCommitter bridges Twilight's complete-step barrier to history
// persistence. It is intentionally enabled only for admitted, fenced turns;
// legacy calls without a runtime owner keep their terminal-snapshot behavior.
type agentStepCommitter struct {
	service            *Service
	req                ChatRequest
	rc                 resolvedContext
	persister          messagepkg.AgentStepPersister
	reasoningTiming    *reasoningTimingTracker
	queueStep          *queueStepTransaction
	continueAfterFinal atomic.Bool
	nextModelInputs    []sdk.Message

	mu                   sync.Mutex
	turnRequestMessageID string
	persisted            []messagepkg.Message
	memoryPersisted      []messagepkg.Message
	messages             []ModelMessage
	nextStep             int // In-process ordering guard, not a durable replay cursor.
	commitErr            error
	finalized            bool
	replacementFinalized bool
}

func (s *Service) newAgentStepCommitter(ctx context.Context, req ChatRequest, rc resolvedContext) *agentStepCommitter {
	if s == nil || s.messageService == nil || strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.ThreadID) == "" ||
		((req.SkipHistoryTurn || req.ReusePersistedUserMessage) && req.TurnReplacement == nil) {
		return nil
	}
	if _, ok := runtimefence.FromContext(ctx); !ok {
		return nil
	}
	persister, ok := s.messageService.(messagepkg.AgentStepPersister)
	if !ok {
		return nil
	}
	queueStep := newQueueStepTransaction(s, req, rc.model.ID)
	if req.TurnReplacement != nil && queueStep == nil {
		return nil
	}
	requestMessageID := ""
	switch {
	case req.ReusePersistedUserMessage:
		requestMessageID = strings.TrimSpace(req.PersistedUserMessageID)
		if requestMessageID == "" {
			return nil
		}
	case req.UserMessagePersisted:
		requestMessageID = strings.TrimSpace(req.PersistedUserMessageID)
	case strings.TrimSpace(req.TurnID) == "" || req.TurnPosition == nil:
		return nil
	}
	return &agentStepCommitter{
		service: s, req: req, rc: rc, persister: persister,
		queueStep:            queueStep,
		turnRequestMessageID: requestMessageID,
		nextStep:             req.StepIndexOffset,
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
	hasAssistantOutput := hasPersistableAssistantOutput(messages)
	// A durable run must still cross the coordinator boundary for an empty
	// provider result: final handoff and queue reconciliation are keyed to the
	// step, not to whether the provider emitted a message. Legacy/non-durable
	// paths retain the old cheap no-op behavior.
	if !hasAssistantOutput && (c.queueStep == nil || interrupted) {
		c.nextStep++
		return nil
	}
	if hasAssistantOutput && stepIndex == 0 && !c.req.UserMessagePersisted && !c.req.ReusePersistedUserMessage {
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
	var (
		inputs []messagepkg.PersistInput
		err    error
	)
	if len(messages) > 0 {
		inputs, err = c.service.buildPersistInputs(context.WithoutCancel(ctx), storeReq, messages, c.rc.model.ID, opts)
		if err != nil {
			return fail(err)
		}
	}
	for i := range inputs {
		inputs[i].TurnRequestMessageID = c.turnRequestMessageID
	}
	agentStep := messagepkg.AgentStep{RunID: c.req.RunID, Messages: inputs, Interrupted: interrupted}
	commitHash := agentStepCommitHash(agentStep, step)
	var persisted []messagepkg.Message
	if c.queueStep != nil && !interrupted {
		stepCtx := context.WithoutCancel(ctx)
		outcome, commitErr := c.queueStep.commit(
			stepCtx, stepIndex, commitHash, classifyQueueStep(step), agentStep, c.persisted,
		)
		if commitErr != nil {
			return fail(commitErr)
		}
		persisted = outcome.persisted
		if c.req.TurnReplacement == nil {
			if publisher, ok := c.service.messageService.(messagepkg.AgentStepPublisher); ok {
				publisher.PublishAgentStep(persisted)
			}
		}
		c.replacementFinalized = outcome.replacementFinalized
		if outcome.result.Action == sessionqueue.ContinueWithSteer && classifyQueueStep(step) == sessionqueue.StepFinal {
			if outcome.result.Steer != nil {
				c.nextModelInputs = append(c.nextModelInputs, sdk.UserMessage(continuationPayloadText(outcome.result.Steer.Payload)))
			}
			c.continueAfterFinal.Store(true)
		}
		c.publishQueueUserTurns(context.WithoutCancel(ctx), stepIndex, outcome)
	} else {
		persisted, err = c.persister.PersistAgentStep(context.WithoutCancel(ctx), agentStep)
	}
	if err != nil {
		return fail(err)
	}
	if len(persisted) > 0 {
		c.rc.runConfig.ContextLifecycle.SetAssistantMessageID(lastPersistedAssistantMessageID(persisted))
	}
	c.nextStep++
	for _, message := range persisted {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			c.turnRequestMessageID = message.ID
		}
	}
	c.persisted = append(c.persisted, persisted...)
	if c.replacementFinalized {
		c.service.publishReplacementMessageCreated(c.req.BotID, c.persisted)
	}
	if !interrupted {
		// Unfinished reasoning/text is history context, not a fact source for
		// asynchronous long-term memory extraction.
		c.memoryPersisted = append(c.memoryPersisted, persisted...)
		c.messages = append(c.messages, messages...)
	}
	return nil
}

func (c *agentStepCommitter) publishQueueUserTurns(ctx context.Context, stepIndex int, outcome queueStepOutcome) {
	if c == nil || c.service == nil || c.service.sessionManager == nil {
		return
	}
	projected := chatview.ConvertMessagesToUITurns(outcome.persisted)
	userTurns := make([]chatview.UITurn, 0, len(projected))
	for _, turn := range projected {
		if strings.EqualFold(strings.TrimSpace(turn.Role), "user") {
			userTurns = append(userTurns, turn)
		}
	}
	update := sessionruntime.QueueUserTurnUpdate{
		PersistedTurns:     userTurns,
		AppliedSteerItemID: strings.TrimSpace(outcome.appliedSteerItemID),
	}
	if update.AppliedSteerItemID != "" && len(userTurns) > 0 {
		// A queue claim is the only mid-run user input admitted by this adapter.
		// The step capture places it after any earlier durable users, so the last
		// persisted user is the history identity for the applied item.
		applied := userTurns[len(userTurns)-1]
		update.AppliedSteerTurn = &applied
	}
	if outcome.claimedSteer != nil {
		update.ClaimedSteerItemID = string(outcome.claimedSteer.ID)
		update.ClaimedSteerText = continuationPayloadText(outcome.claimedSteer.Payload)
		update.ClaimedSteerTimestamp = outcome.claimedSteer.CreatedAt
		// Anchor after the step that just committed. Its step_end marker was
		// emitted by the native loop before the commit barrier ran, so the wait
		// only covers event consumption and is bounded.
		after := stepIndex
		update.AfterStepIndex = &after
	}
	if len(update.PersistedTurns) == 0 && update.AppliedSteerItemID == "" && update.ClaimedSteerItemID == "" {
		return
	}
	if err := c.service.sessionManager.PublishQueueUserTurns(ctx, c.req.RunHandle, update); err != nil && c.service.logger != nil {
		c.service.logger.Warn("publish runtime queue user turns failed",
			slog.String("run_id", c.req.RunID), slog.Any("error", err))
	}
}

func agentStepCommitHash(step messagepkg.AgentStep, result *sdk.StepResult) string {
	payload, err := json.Marshal(struct {
		Step   messagepkg.AgentStep `json:"step"`
		Result *sdk.StepResult      `json:"result"`
	}{step, result})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func classifyQueueStep(step *sdk.StepResult) sessionqueue.StepKind {
	if step == nil || step.DeferredToolApproval != nil {
		return sessionqueue.StepDeferredDecision
	}
	if step.FinishReason == sdk.FinishReasonToolCalls && len(step.ToolCalls) > 0 {
		return sessionqueue.StepToolLoop
	}
	return sessionqueue.StepFinal
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
	persisted := append([]messagepkg.Message(nil), c.persisted...)
	memoryPersisted := append([]messagepkg.Message(nil), c.memoryPersisted...)
	messages := append([]ModelMessage(nil), c.messages...)
	if len(persisted) == 0 {
		c.finalized = true
	}
	c.mu.Unlock()
	if len(persisted) == 0 {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	if err := c.service.persistSessionWorkspaceTarget(ctx, c.req); err != nil {
		return err
	}
	c.mu.Lock()
	c.finalized = true
	c.mu.Unlock()
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

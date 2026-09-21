package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

// agentStepCommitter bridges Twilight's complete-step barrier to history
// persistence. It is intentionally enabled only for admitted, fenced turns;
// legacy calls without a runtime owner keep their terminal-snapshot behavior.
type agentStepCommitter struct {
	ownerContext       context.Context
	service            *Service
	req                ChatRequest
	rc                 resolvedContext
	persister          messagepkg.AgentStepPersister
	reasoningTiming    *reasoningTimingTracker
	queueStep          *queueStepCoordinator
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
	queueStep := newQueueStepCoordinator(s, req)
	if queueStep != nil && queueStep.steerEnabled {
		if err := s.sessionManager.EnableSteer(ctx, req.RunHandle); err != nil {
			queueStep.steerEnabled = false
			if s.logger != nil {
				s.logger.WarnContext(ctx, "steer consumer could not be published", slog.String("run_id", req.RunID), slog.Any("error", err))
			}
		}
	}
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
		service: s, req: req, rc: rc, persister: persister, ownerContext: ctx,
		queueStep:            queueStep,
		turnRequestMessageID: requestMessageID,
		nextStep:             req.StepIndexOffset,
	}
}

func (c *agentStepCommitter) bindContinuation(cfg *native.RunConfig) {
	if c == nil || cfg == nil {
		return
	}
	cfg.ContinueAfterFinal = &c.continueAfterFinal
	cfg.NextModelInputs = &c.nextModelInputs
	if c.queueStep != nil && c.queueStep.steerEnabled {
		cfg.SteerWake = c.service.sessionManager.SteerWake(c.req.RunHandle)
		cfg.PendingSteer = func(ctx context.Context) (bool, error) {
			items, _, err := c.service.sessionManager.PendingQueues(ctx, sessionruntime.Key{
				BotID: c.req.BotID, SessionID: c.req.ThreadID,
			}, sessionruntime.MaxPendingQueueItems)
			for _, item := range items {
				if item.TargetRunID == c.req.RunID && item.Status == sessionruntime.QueueAccepted {
					return true, err
				}
			}
			return false, err
		}
		cfg.OnSteer = func(ctx context.Context, index int, step *sdk.StepResult) error {
			return c.persist(ctx, index, step, stepSteered)
		}
	}
}

type stepCommitMode uint8

const (
	stepCompleted stepCommitMode = iota
	stepInterrupted
	stepSteered
)

func (c *agentStepCommitter) commit(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
	return c.persist(ctx, stepIndex, step, stepCompleted)
}

func (c *agentStepCommitter) interrupt(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
	return c.persist(ctx, stepIndex, step, stepInterrupted)
}

func (c *agentStepCommitter) persist(ctx context.Context, stepIndex int, step *sdk.StepResult, mode stepCommitMode) error {
	interrupted := mode != stepCompleted
	if c == nil || step == nil {
		return errors.New("agent step is missing")
	}
	persistCtx, ownershipErr := stepPersistenceContext(ctx, c.ownerContext)
	if ownershipErr != nil {
		return ownershipErr
	}
	ctx = persistCtx
	messages := sdkMessagesWithOrigins(step.Messages, native.InternalFeedbackIndexes(ctx))
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
		if mode != stepInterrupted {
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
	if !hasAssistantOutput && mode != stepSteered && (c.queueStep == nil || interrupted) {
		c.nextStep++
		return nil
	}
	if (hasAssistantOutput || mode == stepSteered) && stepIndex == 0 && !c.req.UserMessagePersisted && !c.req.ReusePersistedUserMessage {
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
	c.stampSteerTurn(inputs)
	agentStep := messagepkg.AgentStep{RunID: c.req.RunID, Messages: inputs, Interrupted: interrupted}
	var persisted []messagepkg.Message
	var queueErr error
	if c.queueStep != nil && mode != stepInterrupted {
		stepCtx := context.WithoutCancel(ctx)
		kind := classifyQueueStep(step)
		if mode == stepSteered {
			kind = queueStepSteered
		}
		outcome, commitErr := c.queueStep.commit(
			stepCtx, kind, agentStep, c.persisted,
		)
		if !outcome.historyCommitted {
			return fail(commitErr)
		}
		// History is already durable even if later queue coordination failed.
		// Record that prefix below before returning the error; a cleanup must
		// not lose it or write the same step again.
		queueErr = commitErr
		persisted = outcome.persisted
		c.replacementFinalized = outcome.replacementFinalized
		if queueErr == nil {
			if outcome.claimedSteer != nil {
				c.nextModelInputs = append(c.nextModelInputs, sdk.UserMessage(QueuePayloadText(outcome.claimedSteer.Payload)))
			}
			c.continueAfterFinal.Store(outcome.continueAfterFinal)
		}
		if queueErr == nil {
			c.publishQueueUserTurns(context.WithoutCancel(ctx), stepIndex, outcome)
		}
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
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") && !messagepkg.IsInternalFeedback(message.Metadata) {
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
	if queueErr != nil {
		return fail(queueErr)
	}
	return nil
}

// stampSteerTurn files this step's injected steer input under the turn drawn
// when the input was claimed, instead of letting persistence mint a second name
// for it. The row is identified by position rather than by text, and the
// position to take is the last one: the steer is the final user row a step can
// carry. PrepareStep's other user-row injectors — the image-only row read_media
// appends after reading media, and mid-turn platform injects — are wrapped
// inside prepareQueuedSteer, so their rows are appended ahead of the steer, and
// sdk.StepResult.Messages holds only the assistant and tool rows the provider
// produced. So scan from the end and take the last user row nobody named.
// native.TestQueuedSteerIsAppendedAfterEveryOtherPreparedMessage pins the
// ordering this depends on.
func (c *agentStepCommitter) stampSteerTurn(inputs []messagepkg.PersistInput) {
	if c == nil || c.queueStep == nil {
		return
	}
	slot := c.queueStep.steerTurnForStep()
	if slot == nil || strings.TrimSpace(slot.TurnID) == "" {
		return
	}
	for i := len(inputs) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(inputs[i].Role), "user") || messagepkg.IsInternalFeedback(inputs[i].Metadata) {
			continue
		}
		if strings.TrimSpace(inputs[i].TurnID) != "" {
			continue
		}
		position := slot.Position
		inputs[i].TurnID = slot.TurnID
		inputs[i].TurnPosition = &position
		return
	}
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
		update.ClaimedSteerText = QueuePayloadText(outcome.claimedSteer.Payload)
		update.ClaimedSteerTimestamp = outcome.claimedSteer.CreatedAt
		if outcome.claimedSteerTurn != nil {
			update.ClaimedSteerTurnID = outcome.claimedSteerTurn.TurnID
			update.ClaimedSteerTurnPosition = outcome.claimedSteerTurn.Position
		}
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
		c.service.logger.WarnContext(ctx, "publish runtime queue user turns failed",
			slog.String("run_id", c.req.RunID), slog.Any("error", err))
	}
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

// The run's owner context remains authoritative even when the SDK supplies a
// detached cleanup context. User abort checkpoints may outlive cancellation;
// a revoked owner must never write in the reaper's grace window.
func stepPersistenceContext(ctx, owner context.Context) (context.Context, error) {
	if runOwnershipLost(ctx) || runOwnershipLost(owner) {
		return nil, sessionruntime.ErrRunOwnershipLost
	}
	return context.WithoutCancel(ctx), nil
}

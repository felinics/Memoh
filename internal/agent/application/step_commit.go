package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

// agentStepCommitter bridges Twilight's complete-step barrier to history
// persistence. It is intentionally enabled only for admitted, fenced turns;
// legacy calls without a runtime owner keep their terminal-snapshot behavior.
type agentStepCommitter struct {
	ownerContext    context.Context
	service         *Service
	req             ChatRequest
	rc              resolvedContext
	persister       messagepkg.AgentStepPersister
	reasoningTiming *reasoningTimingTracker
	queueStep       *queueStepCoordinator

	mu                   sync.Mutex
	turnRequestMessageID string
	persisted            []messagepkg.Message
	inputSourceIDs       []string
	memoryPersisted      []messagepkg.Message
	memoryExpectedRows   int
	nextStep             int // In-process ordering guard, not a durable replay cursor.
	commitErr            error
	finalized            bool
	replacementStarted   bool
	// checkpointed means an interrupted step was written: the run already has
	// a history row for the model call it stopped in.
	checkpointed bool
	// retriedRequest is the request row a retry renders as its input, and
	// requestCopy the row its first step copied it to.
	retriedRequest string
	requestCopy    string
}

func (s *Service) newAgentStepCommitter(ctx context.Context, req ChatRequest, rc resolvedContext) *agentStepCommitter {
	if s == nil || s.messageService == nil || strings.TrimSpace(req.RunID) == "" ||
		strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.ThreadID) == "" ||
		(req.ReusePersistedUserMessage && req.TurnReplacement == nil) {
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
	committer := &agentStepCommitter{
		service: s, req: req, rc: rc, persister: persister, ownerContext: ctx,
		queueStep:            queueStep,
		turnRequestMessageID: requestMessageID,
		// A continuation resumes a run whose earlier steps are already
		// persisted; the loop numbers its commits from the same offset.
		nextStep: req.StepIndexOffset,
	}
	if requestMessageID != "" {
		committer.inputSourceIDs = []string{requestMessageID}
	}
	return committer
}

// bindNativeRun arms one native run: step persistence when the turn is
// admitted and fenced, provider part timing for the drivers that sample it,
// and the continuation hooks bindContinuation decides from the request. Every
// native driver goes through it, so which hooks a run gets is declared in one
// place instead of per driver.
func (s *Service) bindNativeRun(ctx context.Context, req ChatRequest, rc resolvedContext, cfg *native.RunConfig, timing *reasoningTimingTracker) *agentStepCommitter {
	committer := s.newAgentStepCommitter(ctx, req, rc)
	if timing != nil {
		configureNativeReasoningTiming(cfg, timing, committer)
	} else if committer != nil {
		cfg.OnStepCommitted = committer.commit
	}
	committer.bindContinuation(cfg)
	// A run that persists its steps compacts in place when a step's request
	// no longer fits. A retry or edit folds what its request renders like
	// any other run: the history before its cutoff, and the steps of the
	// attempt its first step selected.
	if committer != nil && s.compactionService != nil && s.settingsService != nil {
		cfg.SwitchContext = s.switchContext(req, committer, cfg.CurrentModelUUID)
	}
	return committer
}

func (c *agentStepCommitter) bindContinuation(cfg *native.RunConfig) {
	if c == nil || cfg == nil {
		return
	}
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
		// The steered checkpoint is also a claim boundary: its directive
		// carries whatever input the claim admitted, so the loop continues
		// with the new input instead of restarting the run.
		cfg.OnSteer = func(ctx context.Context, index int, record *step.Record) (native.StepDirective, error) {
			return c.persist(ctx, index, record, stepSteered, "")
		}
	}
}

// withOutputTruncatedNotice records the truncation notice on the assistant row
// of a final step the model cut off at its output limit, so history shows it
// the way the live stream did.
func withOutputTruncatedNotice(opts storeRoundOptions, record *step.Record, messages []ModelMessage) storeRoundOptions {
	if record.Result.FinishReason != sdk.FinishReasonLength || len(record.Result.ToolCalls) > 0 {
		return opts
	}
	idx := lastAssistantMessageIndex(messages)
	if idx < 0 {
		return opts
	}
	definition, ok := apperror.Lookup(apperror.CodeRuntimeOutputTruncated)
	if !ok {
		return opts
	}
	if opts.MessageMetadataByIndex == nil {
		opts.MessageMetadataByIndex = make(map[int]map[string]any, 1)
	}
	if opts.MessageMetadataByIndex[idx] == nil {
		opts.MessageMetadataByIndex[idx] = map[string]any{}
	}
	opts.MessageMetadataByIndex[idx][event.RuntimeNoticesMetadataKey] = []event.Notice{{
		Code:    string(apperror.CodeRuntimeOutputTruncated),
		Content: definition.Detail,
	}}
	return opts
}

type stepCommitMode uint8

const (
	stepCompleted stepCommitMode = iota
	stepInterrupted
	stepSteered
)

func (c *agentStepCommitter) commit(ctx context.Context, stepIndex int, record *step.Record) (native.StepDirective, error) {
	return c.persist(ctx, stepIndex, record, stepCompleted, "")
}

func (c *agentStepCommitter) interrupt(ctx context.Context, stepIndex int, record *step.Record) error {
	_, err := c.persist(ctx, stepIndex, record, stepInterrupted, "")
	return err
}

// persist writes one step. failureCode, when set, names the failure that ended
// the run on an interrupted step, and is recorded on its assistant rows.
func (c *agentStepCommitter) persist(ctx context.Context, stepIndex int, record *step.Record, mode stepCommitMode, failureCode apperror.Code) (native.StepDirective, error) {
	interrupted := mode != stepCompleted
	if c == nil || record == nil {
		return native.StepDirective{}, errors.New("agent step is missing")
	}
	persistCtx, ownershipErr := stepPersistenceContext(ctx, c.ownerContext)
	if ownershipErr != nil {
		return native.StepDirective{}, ownershipErr
	}
	ctx = persistCtx
	messages := sdkMessagesWithOrigins(record.Messages, native.InternalFeedbackIndexes(ctx))
	timingState := "completed"
	if interrupted {
		timingState = "interrupted"
	}
	reasoningTiming := c.reasoningTiming.take(timingState)

	c.mu.Lock()
	defer c.mu.Unlock()
	var dir native.StepDirective
	// A failed interrupted checkpoint is reported to its caller but never
	// recorded as a commit failure: the turn is already ending, and losing an
	// unfinished snapshot must not turn an abort into a turn error.
	fail := func(err error) (native.StepDirective, error) {
		if mode != stepInterrupted {
			c.commitErr = err
		}
		return native.StepDirective{}, err
	}
	if stepIndex != c.nextStep {
		return fail(fmt.Errorf("unexpected agent step %d, want %d", stepIndex, c.nextStep))
	}
	hasAssistantOutput := hasPersistableAssistantOutput(messages)
	// A durable run must still cross the coordinator boundary for an empty
	// provider result: final handoff and queue reconciliation are keyed to the
	// step, not to whether the provider emitted a message. Legacy/non-durable
	// paths retain the old cheap no-op behavior.
	if !hasAssistantOutput && failureCode == "" && mode != stepSteered && (c.queueStep == nil || interrupted) {
		c.nextStep++
		return native.StepDirective{}, nil
	}
	stepMessages := len(messages)
	if (hasAssistantOutput || failureCode != "" || mode == stepSteered) && len(c.persisted) == 0 && !c.req.UserMessagePersisted && !c.req.ReusePersistedUserMessage {
		messages = prependTurnUserMessage(c.req, messages)
	}
	storeReq := c.req
	// Outbound assets are linked once after the stream closes; including the
	// collector in every step would attach the same accumulated assets again.
	storeReq.OutboundAssetCollector = nil
	opts := storeRoundOptions{
		AllowPendingToolCalls: record.Deferred != nil,
		ContextLifecycle:      c.rc.runConfig.ContextLifecycle,
		ReasoningTiming:       reasoningTiming,
	}
	if interrupted {
		opts.MessageMetadataByIndex = make(map[int]map[string]any)
		for i, message := range messages {
			if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
				opts.MessageMetadataByIndex[i] = map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true}
				if failureCode != "" {
					opts.MessageMetadataByIndex[i][messagepkg.HistoryErrorCodeMetadataKey] = string(failureCode)
				}
			}
		}
	}
	opts = withOutputTruncatedNotice(opts, record, messages)
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
	steerIndex := c.stampSteerTurn(inputs)
	agentStep := messagepkg.AgentStep{RunID: c.req.RunID, Messages: inputs, Interrupted: interrupted}
	if !c.replacementStarted && len(inputs) > 0 {
		agentStep.Replacement = c.req.TurnReplacement
	}
	var persisted []messagepkg.Message
	var queueErr error
	if c.queueStep != nil && mode != stepInterrupted {
		stepCtx := context.WithoutCancel(ctx)
		kind := classifyQueueStep(record)
		if mode == stepSteered {
			kind = queueStepSteered
		}
		outcome, commitErr := c.queueStep.commit(
			stepCtx, kind, agentStep,
		)
		if !outcome.historyCommitted {
			return fail(commitErr)
		}
		// History is already durable even if later queue coordination failed.
		// Record that prefix below before returning the error; a cleanup must
		// not lose it or write the same step again.
		queueErr = commitErr
		persisted = outcome.persisted
		if queueErr == nil {
			c.publishQueueUserTurns(context.WithoutCancel(ctx), stepIndex, outcome)
			if outcome.claimedSteer != nil {
				if text := QueuePayloadText(outcome.claimedSteer.Payload); text != "" {
					dir.NextInputs = []native.DirectiveInput{{
						ID:   string(outcome.claimedSteer.ID),
						Text: text,
					}}
				}
			}
		}
	} else {
		persisted, err = c.persister.PersistAgentStep(context.WithoutCancel(ctx), agentStep)
	}
	if err != nil {
		return fail(err)
	}
	if agentStep.Replacement != nil {
		c.replacementStarted = true
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
	// A retry's first step writes the copy of its request ahead of the step's
	// own rows. The run renders its input from the row it read, which the
	// same transaction hid; the copy is the input from then on.
	rows := persisted
	if agentStep.Replacement != nil && len(persisted) > len(inputs) {
		c.retriedRequest, c.requestCopy = strings.TrimSpace(agentStep.Replacement.RequestMessageID), persisted[0].ID
		for i, id := range c.inputSourceIDs {
			if id == c.retriedRequest {
				c.inputSourceIDs[i] = c.requestCopy
			}
		}
		rows = persisted[1:]
	}
	if len(rows) == len(inputs) {
		if len(messages) > stepMessages {
			c.inputSourceIDs = append(c.inputSourceIDs, rows[0].ID)
		}
		if steerIndex >= 0 {
			c.inputSourceIDs = append(c.inputSourceIDs, rows[steerIndex].ID)
		}
	}
	if len(rows) == len(messages) && stepMessages == len(record.Messages) {
		dir.Provenance = stepProvenance(rows[len(messages)-stepMessages:], c.inputSourceIDs)
	}
	if mode == stepInterrupted && len(persisted) > 0 {
		c.checkpointed = true
	}
	if !interrupted {
		// Unfinished reasoning/text is history context, not a fact source for
		// asynchronous long-term memory extraction.
		c.memoryPersisted = append(c.memoryPersisted, persisted...)
		c.memoryExpectedRows += len(inputs)
		if agentStep.Replacement != nil && strings.TrimSpace(agentStep.Replacement.RequestMessageID) != "" {
			c.memoryExpectedRows++ // The retry transaction also writes its own request.
		}
	} else {
		// A checkpoint can be the first transaction of a replacement. Its
		// user inputs are complete facts even when its assistant output is
		// partial; retain their durable identities for a later successful run.
		for _, message := range persisted {
			if strings.EqualFold(strings.TrimSpace(message.Role), "user") && !messagepkg.IsInternalFeedback(message.Metadata) {
				c.memoryPersisted = append(c.memoryPersisted, message)
				c.memoryExpectedRows++
			}
		}
	}
	if queueErr != nil {
		return fail(queueErr)
	}
	return dir, nil
}

// stepProvenance carries the request and queue claim's explicit input IDs.
func stepProvenance(rows []messagepkg.Message, inputs []string) []native.MessageProvenance {
	provenance := make([]native.MessageProvenance, len(rows))
	for i, row := range rows {
		source := turn.ContextMessageSource{Kind: "history", ID: row.ID}
		if len(row.Content) > 0 {
			source.ContentHash = historyfrag.MessageContentHash(historyfrag.DecodeStoredModelMessage(nil, row.ID, row.Role, row.Content))
		}
		provenance[i] = native.MessageProvenance{
			Sources: []turn.ContextMessageSource{source},
			Anchor:  slices.Contains(inputs, row.ID),
		}
	}
	return provenance
}

// inputSources names the run's input and the steers its steps persisted.
func (c *agentStepCommitter) inputSources() []turn.ContextMessageSource {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var sources []turn.ContextMessageSource
	for _, id := range c.inputSourceIDs {
		sources = append(sources, turn.ContextMessageSource{Kind: "history", ID: id})
	}
	return sources
}

// currentSources names sources the run rendered by the rows that hold them
// now: a retry's request by the copy its first step wrote, with the same
// content.
func (c *agentStepCommitter) currentSources(sources []turn.ContextMessageSource) []turn.ContextMessageSource {
	out := slices.Clone(sources)
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, source := range out {
		if c.requestCopy != "" && source.Kind == "history" && source.ID == c.retriedRequest {
			out[i].ID = c.requestCopy
		}
	}
	return out
}

// stampSteerTurn files this step's injected steer input under the turn drawn
// when the input was claimed, instead of letting persistence mint a second name
// for it. The row is identified by position rather than by text, and the
// position to take is the last one: the steer is the final user row a step can
// carry. The loop appends its other user rows first at a step boundary — the
// read-media carrier, then injected platform messages — and the steer
// directive last (see streamEngine.run), and step.Record.Messages holds only
// the assistant and tool rows the provider produced. So scan from the end and
// take the last user row nobody named.
// native.TestQueuedSteerIsAppendedAfterEveryOtherPreparedMessage pins the
// ordering this depends on.
func (c *agentStepCommitter) stampSteerTurn(inputs []messagepkg.PersistInput) int {
	if c == nil || c.queueStep == nil {
		return -1
	}
	slot := c.queueStep.steerTurnForStep()
	if slot == nil || strings.TrimSpace(slot.TurnID) == "" {
		return -1
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
		return i
	}
	return -1
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

// recordFailure writes the history of a run that failed with code, a failure
// the history shows (see wsFailureCode), before the run is finished. Text
// the stream showed for the model call that failed is written as an
// interrupted step carrying the code. An empty failure still records an
// assistant row through the same transaction, including a replacement's first
// user clone and turn binding. A model call the runtime already checkpointed
// is not written again.
func (c *agentStepCommitter) recordFailure(ctx context.Context, partialText string, code apperror.Code) ([]messagepkg.Message, error) {
	if c == nil || code == "" {
		return nil, nil
	}
	c.mu.Lock()
	skip := c.finalized || c.checkpointed
	stepIndex := c.nextStep
	c.mu.Unlock()
	if skip {
		return nil, nil
	}
	record := &step.Record{Messages: toolexec.BuildStepMessages(partialText, nil, nil, nil, nil, nil)}
	_, err := c.persist(ctx, stepIndex, record, stepInterrupted, code)
	return nil, err
}

func (c *agentStepCommitter) finish(ctx context.Context) error {
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
	memoryExpectedRows := c.memoryExpectedRows
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
	if !c.req.SkipMemoryExtraction && len(memoryPersisted) == memoryExpectedRows && len(memoryPersisted) > 0 {
		c.service.storeMemory(ctx, c.req, memoryPersisted)
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

// uncommittedStepText is the answer text the stream showed for the model call
// that has not committed yet. A step end closes the call and a retry discards
// it, so the text starts over at either; the runtime regenerates a retried
// call from its last committed step.
type uncommittedStepText struct {
	text strings.Builder
}

func (t *uncommittedStepText) observe(event native.StreamEvent) {
	switch event.Type {
	case native.EventTextDelta:
		t.text.WriteString(event.Delta)
	case native.EventStepEnd, native.EventRetry:
		t.text.Reset()
	}
}

func (t *uncommittedStepText) String() string {
	return t.text.String()
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

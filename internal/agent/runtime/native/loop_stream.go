package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/hooks"
)

// runStream runs the streaming agent invocation with a Memoh-owned step loop:
// every step performs exactly one Provider.DoStream call, consumes its part
// stream into events, and executes its tool batch through toolexec.ExecuteTools. A
// final step whose commit returns NextInputs continues on the next inner-loop
// iteration of the same engine.
// streamEventBuffer is the capacity of the engine→forwarder event channel. It
// mirrors the part buffer the SDK loop used between its step goroutine and the
// event consumer: the engine can run a whole step (and its commit barrier)
// ahead of a consumer that has not drained the live events yet.
const streamEventBuffer = 64

func (a *Agent) runStream(ctx context.Context, cfg RunConfig, ch chan<- StreamEvent) {
	// Tools report capability changes here; the loop re-assembles its tool
	// set at the next committed step.
	cfg.capabilityChanges = &atomic.Bool{}
	// The gate stops a model call that is still sampling once a queued steer
	// is accepted. It lives inside the provider observer so the decision is
	// made on the raw part stream, ahead of the live event consumer, and
	// cancels only that call: the loop checkpoints the attempt and continues.
	var steerGate *modelSteerGate
	if cfg.PendingSteer != nil && cfg.OnSteer != nil {
		steerGate = &modelSteerGate{ready: make(chan struct{}, 1)}
	}
	cfg.Model = modelWithProviderCallObserver(cfg.Model, cfg.OnProviderStreamEventObserved, steerGate)
	if cfg.ContextLifecycle == nil {
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
	if steerGate != nil {
		go a.watchSteer(streamCtx, cfg, steerGate)
	}
	eventGate := newStreamEmitterGate(streamCtx, ch)
	defer func() {
		cancel(nil)
		eventGate.close()
	}()
	aborted := false
	turnError := ""
	defer func() {
		event := hooks.EventTurnEnd
		if aborted || strings.TrimSpace(turnError) != "" {
			event = hooks.EventTurnError
			if strings.TrimSpace(turnError) == "" {
				turnError = "agent run aborted"
			}
		}
		a.runTurnHook(context.WithoutCancel(ctx), cfg, event, turnError)
	}()
	defer func() {
		a.logContextLifecycle(cfg)
	}()

	// Stream emitter: tools targeting the current conversation push
	// side-effect events (attachments, reactions, speech) directly here.
	// Uses sendEvent to avoid goroutine leaks when the consumer stops reading.
	streamEmitter := tools.StreamEmitter(eventGate.emit)
	if cfg.ForkContext == nil {
		cfg.ForkContext = tools.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	}

	var sdkTools []toolexec.Tool
	cfg.ContextToolDefsResolved = true
	if cfg.SupportsToolCall {
		var toolUsage string
		var toolUsageFrags []contextfrag.ContextFrag
		var toolDefs []contextfrag.ToolDefAccounting
		var err error
		sdkTools, toolUsage, toolUsageFrags, toolDefs, err = a.assembleTools(streamCtx, cfg, streamEmitter, cfg.LiveToolStream)
		if err != nil {
			turnError = fmt.Sprintf("assemble tools: %v", err)
			sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
			return
		}
		cfg.ContextToolDefs = toolDefs
		if toolUsage != "" {
			// Must run before dispatch assembly so prompt caching and
			// background task summaries see the usage-augmented text.
			cfg.System = appendToolUsageToSystem(cfg.System, toolUsage)
			cfg.ContextToolUsage = toolUsage
			cfg.ContextToolUsageFrags = toolUsageFrags
		}
	}
	sdkTools, readMediaState := decorateReadMediaTools(cfg.Model, sdkTools)
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMediaToolPresent(sdkTools), a != nil && a.hookService != nil, true)
	var contextViewErr error
	cfg, contextViewErr = a.applyContextView(streamCtx, cfg)
	if contextViewErr != nil {
		publicError := contextViewStreamError(contextViewErr)
		turnError = publicError.Error
		a.logger.WarnContext(ctx, "context view preflight failed", slog.Any("error", contextViewErr))
		sendEvent(ctx, ch, publicError)
		return
	}
	cfg = captureProviderAttemptPrefix(cfg)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(func(call sdk.ToolCall, metadata map[string]any) {
		eventGate.emitAgentEvent(StreamEvent{
			Type:       EventToolCallMetadata,
			ToolName:   call.ToolName,
			ToolCallID: call.ToolCallID,
			Input:      toolexec.ArgumentsValue(call.Input),
			Metadata:   metadata,
		})
	})
	cfg.ToolApprovalHandler = toolExecutionMetadata.wrap(cfg.ToolApprovalHandler)

	// Loop detection setup. The text probe lives on the engine: it inspects
	// deltas exactly where the part switch consumes them.
	var textLoopGuard *TextLoopGuard
	var toolLoopGuard *ToolLoopGuard
	toolLoopAbortCallIDs := newToolAbortRegistry()
	if cfg.LoopDetection.Enabled {
		textLoopGuard = NewTextLoopGuard(LoopDetectedStreakThreshold, LoopDetectedMinNewGramsPerChunk, SentialOptions{})
		toolLoopGuard = NewToolLoopGuard(ToolLoopRepeatThreshold, ToolLoopWarningsBeforeAbort)
	}

	// The execution chain over the assembled tools; a capability refresh
	// rebuilds the same chain over the re-assembled set.
	sdkTools, approvalTools := a.wrapExecutableTools(ctx, cfg, sdkTools, toolExecutionMetadata, toolLoopGuard, toolLoopAbortCallIDs)

	// The loop owns the dynamic input messages it appends at step boundaries:
	// live InjectCh messages and read-media carriers.
	dynamic := newLoopDynamicInputs(cfg.StepIndexOffset)
	cfg.dynamicInputs = dynamic
	prepareStep := a.wrapPrepareStepWithModelHook(streamCtx, cfg, nil)
	var err error
	cfg, err = a.applyBeforeModelCallHook(streamCtx, cfg, 0)
	if err != nil {
		turnError = err.Error()
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}
	installContextStepFailureHandler(&cfg, cancel)
	dispatch, dispatchErr := a.buildGenerateDispatch(streamCtx, cfg, sdkTools, approvalTools, prepareStep)
	if dispatchErr != nil {
		turnError = fmt.Sprintf("stream start: %v", dispatchErr)
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}
	if stepErr := contextStepBudgetError(streamCtx); stepErr != nil {
		publicError := contextViewStreamError(stepErr)
		turnError = publicError.Error
		aborted = true
		sendEvent(ctx, ch, publicError)
		return
	}
	// The SDK's StreamText validated these before starting; the loop keeps the
	// same "stream start" error surface for the same failures.
	if cfg.Model == nil {
		turnError = fmt.Sprintf("stream start: %v", errors.New("twilightai: model is required (use WithModel)"))
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}
	if cfg.Model.Provider == nil {
		turnError = fmt.Sprintf("stream start: %v", fmt.Errorf("twilightai: model %q has no provider", cfg.Model.ID))
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}

	sendEvent(ctx, ch, StreamEvent{Type: EventAgentStart})

	// The engine owns the step loop on its own goroutine so a commit barrier
	// never waits on the live event consumer: the session runtime blocks
	// inside OnStepCommitted until the projection has consumed the step_end
	// marker this forwarder delivers.
	eng := &streamEngine{
		agent:                 a,
		baseCfg:               cfg,
		cfg:                   cfg,
		streamCtx:             streamCtx,
		cancel:                cancel,
		events:                make(chan StreamEvent, streamEventBuffer),
		done:                  make(chan struct{}),
		sdkTools:              sdkTools,
		approvalTools:         approvalTools,
		prepareStep:           prepareStep,
		toolExecutionMetadata: toolExecutionMetadata,
		dynamic:               dynamic,
		readMedia:             readMediaState,
		toolLoopAbortCallIDs:  toolLoopAbortCallIDs,
		steer:                 steerGate,
	}
	eng.refreshTools = func() (refreshedTools, error) {
		refreshed, err := a.refreshCapabilities(streamCtx, ctx, &eng.baseCfg, streamEmitter, cfg.LiveToolStream,
			readMediaState, toolExecutionMetadata, toolLoopGuard, toolLoopAbortCallIDs)
		// The dispatch config carries the same prompt and usage as the base;
		// the retry-specific fields stay as the current attempt set them.
		eng.cfg.System = eng.baseCfg.System
		eng.cfg.ContextToolUsage = eng.baseCfg.ContextToolUsage
		eng.cfg.ContextToolUsageFrags = eng.baseCfg.ContextToolUsageFrags
		eng.cfg.ContextToolDefs = eng.baseCfg.ContextToolDefs
		eng.cfg.capabilityRefreshCount = eng.baseCfg.capabilityRefreshCount
		if err == nil {
			eng.sdkTools = refreshed.wrapped
			eng.approvalTools = refreshed.approval
		}
		return refreshed, err
	}
	// Durable step cursors are absolute: a continuation segment starts counting
	// at its offset so the interrupted/steered checkpoint matches the index
	// OnStepCommitted would have used.
	eng.reset(dispatch)
	eng.nextDurableStep = cfg.StepIndexOffset
	eng.interruptedStep.rebase(cfg.StepIndexOffset)
	if textLoopGuard != nil {
		installTextLoopGuard := func() {
			guard := NewTextLoopGuard(LoopDetectedStreakThreshold, LoopDetectedMinNewGramsPerChunk, SentialOptions{})
			eng.textLoopProbeBuffer = NewTextLoopProbeBuffer(LoopDetectedProbeChars, func(text string) {
				result := guard.Inspect(text)
				if result.Abort {
					a.logger.WarnContext(ctx, "text loop detected, will abort")
					eng.aborted = true
					cancel(ErrTextLoopDetected)
				}
			})
		}
		installTextLoopGuard()
		// A steer checkpoint starts a fresh answer; the guard must not carry
		// the interrupted attempt's text into it.
		eng.resetTextLoopGuard = installTextLoopGuard
	}
	// The engine runs on the segment context it was built with; the steer
	// checkpoint derives its origin-marked context from that field by design.
	go eng.run() //nolint:contextcheck

	engineClosed := false
	for !aborted && !engineClosed {
		select {
		case <-streamCtx.Done():
			aborted = true
		case evt, ok := <-eng.events:
			if !ok {
				engineClosed = true
				continue
			}
			if !sendEvent(ctx, ch, evt) {
				aborted = true
			}
		}
	}
	if ctx.Err() != nil {
		aborted = true
	}
	budgetErr := contextStepBudgetError(streamCtx)
	if budgetErr != nil {
		aborted = true
	}

	if aborted && !engineClosed {
		// The engine is expected to stop when the stream context is cancelled,
		// but run termination must not depend on a provider or a tool
		// cooperating promptly. Wait for its exit briefly, then stop waiting so
		// the caller can fence and finalize the run as aborted.
		cancel(context.Canceled)
		engineClosed = drainEventsUntilClosed(ctx, eng.events, streamCancelDrainGrace, ch)
		if !engineClosed {
			// The drain may have spent its grace forwarding the backlog to a
			// slow consumer; the engine's own exit signal says whether its
			// state is safe to read regardless of what is still queued.
			select {
			case <-eng.done:
				engineClosed = true
			default:
			}
		}
	}
	if budgetErr != nil {
		// Published after the backlog so the consumer sees the events the
		// engine produced before the boundary refused the next call.
		publicError := contextViewStreamError(budgetErr)
		turnError = publicError.Error
		sendEvent(ctx, ch, publicError)
	}
	// The engine goroutine having returned is what makes the reads below
	// safe: no further complete step can commit after this checkpoint, and
	// its published state is ordered by the channel close (or the done
	// signal). When the engine refuses to exit within the drain grace, its
	// state is dropped rather than risk racing a commit it is still about to
	// make.
	streamClosed := engineClosed
	if engineClosed {
		if eng.aborted {
			aborted = true
		}
		if turnError == "" {
			turnError = eng.turnError
		}
	}

	// Only external cancellation can represent a user/session abort. Provider
	// errors and loop guards keep their existing failure semantics.
	var interruptedMessages []sdk.Message
	var interruptedFeedbackIndexes []int
	interruptedDurableStep := -1
	if aborted && streamClosed && ctx.Err() != nil && cfg.OnStepInterrupted != nil {
		stepIndex := eng.nextDurableStep
		if step := eng.interruptedStep.snapshot(stepIndex); step != nil {
			step = decorateCommittedStep(dynamic.stepAdditions(stepIndex), step, toolExecutionMetadata)
			if err := cfg.OnStepInterrupted(dynamic.withMessageOrigins(streamCtx, stepIndex), stepIndex, step); err != nil {
				// An owner that lost its lease, or a run another writer already
				// finalized, is an expected outcome of racing an abort.
				a.logger.WarnContext(ctx, "persist interrupted model step failed", slog.Any("error", err))
			} else {
				interruptedMessages = step.Messages
				interruptedFeedbackIndexes = dynamic.feedbackIndexes(stepIndex)
				interruptedDurableStep = stepIndex
			}
		}
	}

	var finalMessages []sdk.Message
	var feedbackIndexes []int
	var totalUsage sdk.Usage
	if streamClosed {
		finalMessages = eng.outMessages
		finalMessages, feedbackIndexes = dynamic.mergeReadMedia(eng.steps, finalMessages, interruptedDurableStep)
		if eng.deferred != nil {
			finalMessages = annotateDeferredApproval(finalMessages, *eng.deferred)
		}
		finalMessages = toolExecutionMetadata.annotate(finalMessages)
		totalUsage = aggregateStepUsage(eng.steps)
	}
	for _, index := range interruptedFeedbackIndexes {
		feedbackIndexes = append(feedbackIndexes, len(finalMessages)+index)
	}
	finalMessages = append(finalMessages, interruptedMessages...)
	usageJSON, _ := json.Marshal(totalUsage)

	termEvent := StreamEvent{
		InternalFeedbackIndexes: feedbackIndexes,
		Messages:                mustMarshal(finalMessages),
		Usage:                   usageJSON,
	}
	if streamClosed && eng.deferred != nil {
		termEvent.ApprovalID = eng.deferred.ApprovalID
		if isUserInputMetadata(eng.deferred.Metadata) {
			termEvent.UserInputID = eng.deferred.ApprovalID
		}
		termEvent.ShortID = approvalShortID(eng.deferred.Metadata)
		termEvent.Status = "pending"
		termEvent.Metadata = eng.deferred.Metadata
		if toolName, ok := eng.deferred.Metadata["tool_name"].(string); ok {
			termEvent.ToolName = toolName
		}
		if toolCallID, ok := eng.deferred.Metadata["tool_call_id"].(string); ok {
			termEvent.ToolCallID = toolCallID
		}
	}
	if aborted {
		termEvent.Type = EventAgentAbort
	} else {
		termEvent.Type = EventAgentEnd
		// Warn if LLM produced no text and no tool calls — likely a context overflow.
		if eng.allText.Len() == 0 && eng.stepNumber == 0 {
			a.logger.WarnContext(ctx, "agent produced empty response (no text, no tool calls)",
				slog.String("bot_id", cfg.Identity.BotID),
				slog.Int("input_messages", len(cfg.Messages)),
				slog.Int("input_tokens", totalUsage.InputTokens),
			)
		}
	}
	// The legacy recorder is append-only durability state. Flush only messages
	// admitted by the final provider handoff whose target provider step also
	// completed, so provider-start failures and retry-revoked injections
	// cannot be persisted as durable input.
	// The engine state is written by the engine goroutine, so read it only
	// after its event channel has closed and published the final step slice.
	if streamClosed {
		dynamic.flushInjected(eng.steps, interruptedDurableStep, cfg.InjectedRecorder)
	}
	// Stop secondary producers before delivering the terminal event. The stream
	// context cancellation also unblocks an emitter already waiting on ch.
	cancel(context.Canceled)
	eventGate.close()

	// Deliver the terminal event using a context that is NOT cancelled when
	// the parent ctx is cancelled (user abort / idle timeout / loop-detect).
	// Otherwise sendEvent would short-circuit on <-ctx.Done() and the consumer
	// would never receive the partial messages accumulated so far, forcing it
	// to fall back to a synthetic placeholder. A 5s deadline guards against
	// a fully-disconnected consumer hanging this goroutine forever.
	deliveryCtx, deliveryCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer deliveryCancel()
	sendEvent(deliveryCtx, ch, termEvent)
}

// drainEventsUntilClosed discards what the engine still has buffered after
// drainEventsUntilClosed reads the engine channel until it closes or grace
// runs out. Events still queued are forwarded to ch while the consumer
// accepts them: a loop-guard abort queues the aborting call's tool_call_end
// before it cancels, and that event must reach the consumer. The grace bounds
// the whole drain, forwarding included; once the consumer's ctx is done the
// rest is discarded.
func drainEventsUntilClosed(ctx context.Context, events <-chan StreamEvent, grace time.Duration, ch chan<- StreamEvent) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	forward := ch != nil
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return true
			}
			if !forward {
				continue
			}
			select {
			case ch <- evt:
			case <-ctx.Done():
				forward = false
			case <-timer.C:
				return false
			}
		case <-timer.C:
			return false
		}
	}
}

// streamEngine owns one segment's step loop: per-step provider dispatch, part
// consumption, tool batches through toolexec.ExecuteTools, the commit barrier, and
// the inline mid-stream retry. It runs on its own goroutine, publishes
// StreamEvents to events, and closes that channel on exit; every other field
// under "published state" is safe to read only after the close is observed.
type streamEngine struct {
	agent *Agent
	// baseCfg is the segment configuration: commit identity, durable step
	// offset, and the base a mid-stream retry derives its rebuilt config from.
	baseCfg RunConfig
	// cfg is the configuration of the current dispatch; a mid-stream retry
	// replaces it (fresh attempt counter, retry input messages).
	cfg       RunConfig
	streamCtx context.Context
	cancel    context.CancelCauseFunc
	events    chan StreamEvent
	// done closes after events: the engine goroutine has returned and its
	// published state may be read even when events are still queued.
	done chan struct{}

	// stepThread is the request state the loop advances call by call; a
	// mid-stream retry resets it from the rebuilt dispatch.
	stepThread
	sdkTools      []toolexec.Tool
	approvalTools []toolexec.Tool
	prepareStep   func(*sdk.Request) *sdk.Request

	toolExecutionMetadata *toolExecutionMetadataRegistry
	dynamic               *loopDynamicInputs
	readMedia             *readMediaDecorationState
	textLoopProbeBuffer   *TextLoopProbeBuffer
	resetTextLoopGuard    func()
	toolLoopAbortCallIDs  *toolAbortRegistry
	// refreshTools re-assembles the tool set after a committed step reported a
	// capability change; the thread holds the result until the next call.
	refreshTools func() (refreshedTools, error)

	// steer is nil unless the caller supplied both a pending-steer probe and a
	// checkpoint callback. modelCtx is the cancellation scope of the current
	// provider call, which the gate cancels with errModelSteered.
	steer    *modelSteerGate
	modelCtx context.Context

	// mu serializes the tool-part bridge: toolexec.ExecuteTools may invoke OnPart
	// from parallel tool goroutines.
	mu sync.Mutex

	// published state, read by the segment owner after events closes.
	steps           []step.Record
	outMessages     []sdk.Message
	deferred        *toolexec.ToolApprovalResult
	interruptedStep interruptedStepCapture
	nextDurableStep int
	aborted         bool
	turnError       string
	allText         strings.Builder
	stepNumber      int
}

// emit sends one event to the forwarder. It fails only when the stream
// context is dead, which is exactly when the segment owner stopped consuming.
func (e *streamEngine) emit(evt StreamEvent) bool {
	select {
	case e.events <- evt:
		return true
	case <-e.streamCtx.Done():
		return false
	}
}

// run drives the step loop until a final step, a deferred approval, an abort,
// or an unrecoverable error. The mid-stream retry that used to restart the SDK
// loop is a continue here: same thread, same durable step counting.
func (e *streamEngine) run() {
	defer close(e.done)
	defer close(e.events)
	defer func() {
		// The trailing probe flush mirrors the legacy post-loop flush: a loop
		// detected in the final unflushed window still marks the run aborted.
		if e.textLoopProbeBuffer != nil {
			e.textLoopProbeBuffer.Flush()
		}
	}()

	retryCfg := e.baseCfg.Retry
	if retryCfg.MaxAttempts <= 0 {
		retryCfg = DefaultRetryConfig()
	}
	retryAttempts := 0

	// attemptSteps are the steps committed by the current dispatch, in
	// call-local order: providerAttemptState.retryInput indexes them by the
	// call-local step index it stored at publish time.
	var attemptSteps []step.Record
	attemptStep := 0

	for {
		// A retried dispatch's first call skips the boundary drain, exactly
		// like the initial dispatch's step zero.
		stepParams := e.advance(e.baseCfg, e.dynamic, stepBoundary{
			first:         attemptStep == 0,
			committed:     len(e.steps),
			readMedia:     e.readMedia,
			drainInjected: e.drainInjectedMessages,
		})

		// Dispatch boundary: never invoke the provider on a dead or
		// budget-failed context, and publish the staged provider-attempt state
		// exactly once per call. The budget failure surfaces as the public
		// context error in the segment owner, never as a raw provider error.
		if contextStepBudgetError(e.streamCtx) != nil {
			e.dispatch.handoff.reject()
			e.aborted = true
			return
		}
		if e.streamCtx.Err() != nil {
			e.dispatch.handoff.reject()
			e.aborted = true
			return
		}

		var retryMsg string
		retryableFailure := false
		if err := e.dispatch.handoff.publish(stepParams); err != nil {
			retryMsg, retryableFailure = e.streamFailure(fmt.Errorf("twilightai: stream step %d: %w", attemptStep, err))
		} else {
			var stepDone bool
			retryMsg, retryableFailure, stepDone = e.callModel(attemptStep, &stepParams, &attemptSteps)
			if stepDone {
				return
			}
		}
		if e.aborted {
			return
		}
		if !retryableFailure {
			attemptStep++
			continue
		}

		// Mid-stream retry: the errored step never committed, so the rebuilt
		// dispatch regenerates it from the last committed boundary and the
		// loop continues with the same durable step counting.
		if retryAttempts >= retryCfg.MaxAttempts {
			// Publish the giving-up error: every EventRetry retracts the
			// failure it retried, so without this last event a consumer would
			// see the run end with nothing to explain why it stopped.
			finalErr := fmt.Sprintf("mid-stream retry: all %d attempts failed (last: %s)", retryCfg.MaxAttempts, retryMsg)
			e.turnError = finalErr
			e.emit(StreamEvent{Type: EventError, Error: finalErr})
			e.aborted = true
			return
		}
		input, ok := e.baseCfg.providerAttemptState.retryInput(attemptSteps)
		if !ok {
			// Without a stored provider attempt (tests, defensive paths),
			// rebuild from the durable history so earlier attempts' committed
			// work still reaches the next call.
			input = retryProviderAttemptMessages(e.baseCfg, e.steps, e.outMessages)
		}
		// The failed attempt's partial output is regenerated from the last
		// committed boundary, so it must not survive as a checkpoint.
		e.interruptedStep.rebase(e.baseCfg.StepIndexOffset + len(e.steps))
		// The regenerated attempt starts a new answer; the repeated-text
		// detector must not carry the failed attempt's windows into it.
		if e.resetTextLoopGuard != nil {
			e.resetTextLoopGuard()
		}
		e.agent.logger.WarnContext(e.streamCtx, "mid-stream error, retrying",
			slog.Int("step", e.stepNumber),
			slog.Int("attempt", retryAttempts+1),
			slog.Int("max_attempts", retryCfg.MaxAttempts),
			slog.String("error", retryMsg),
		)
		if !e.emit(StreamEvent{
			Type:       EventRetry,
			Attempt:    retryAttempts + 1,
			MaxAttempt: retryCfg.MaxAttempts,
			RetryError: retryMsg,
		}) {
			e.aborted = true
			return
		}
		if delay := retryDelay(retryAttempts, retryCfg); delay > 0 {
			if err := sleepWithContext(e.streamCtx, delay); err != nil {
				e.aborted = true
				return
			}
		}
		retryAttempts++
		// Re-invoke from the failed attempt's exact provider input plus its
		// committed output, then run the same preflight as every other call.
		next := prepareMidStreamRetryConfigWithMessages(e.baseCfg, input.messages, input.dynamicRefs, len(e.outMessages), retryMsg)
		if e.agent == nil || e.agent.contextViewApplier == nil {
			next = next.RefreshContextFrag()
		}
		e.cfg = next
		dispatch, buildErr := e.agent.buildGenerateDispatch(e.streamCtx, next, e.sdkTools, e.approvalTools, e.prepareStep)
		if buildErr != nil {
			e.turnError = buildErr.Error()
			e.emit(StreamEvent{Type: EventError, Error: e.turnError})
			e.aborted = true
			return
		}
		e.reset(dispatch)
		if contextStepBudgetError(e.streamCtx) != nil {
			e.aborted = true
			return
		}
		e.turnError = ""
		attemptSteps = nil
		attemptStep = 0
	}
}

// callModel performs one provider call and consumes its part stream. The call
// runs under its own cancellation scope so the steer gate can stop sampling
// without ending the run; tool execution and the commit barrier stay on the
// segment context. Provider.DoStream is the single-call seam and hands out
// exactly the part channel the loop consumes: every live event, loop probe,
// and durable step record is derived here, so the step stays owned by Memoh.
func (e *streamEngine) callModel(
	attemptStep int,
	stepParams *sdk.Request,
	attemptSteps *[]step.Record,
) (retryMsg string, retryable bool, done bool) {
	e.modelCtx = e.streamCtx
	if e.steer != nil {
		modelCtx, cancelModel := context.WithCancelCause(e.streamCtx)
		defer cancelModel(nil)
		e.modelCtx = modelCtx
		e.steer.arm(cancelModel)
	}
	provParts, err := e.baseCfg.Model.Provider.DoStream(e.modelCtx, *stepParams)
	switch {
	case (err != nil || provParts == nil) && e.steeredAttempt():
		// A provider that reports the cancellation instead of closing a part
		// stream still leaves a steered attempt: checkpoint it exactly like a
		// stream that ended before finish-step.
		return e.checkpointSteeredStep(attemptStep, attemptSteps)
	case err != nil && e.streamCtx.Err() != nil:
		// The run was cancelled while the request was in flight; the
		// provider's report of it is the abort, not a failure to retry.
		e.aborted = true
		return "", false, true
	case err != nil:
		msg, retriable := e.streamFailure(fmt.Errorf("twilightai: stream step %d: %w", attemptStep, err))
		return msg, retriable, false
	case provParts == nil:
		msg, retriable := e.streamFailure(fmt.Errorf("twilightai: stream step %d ended before finish-step", attemptStep))
		return msg, retriable, false
	}
	return e.consumeStep(attemptStep, provParts, attemptSteps)
}

// steeredAttempt reports whether the current model call was stopped by the
// steer gate rather than by run cancellation or a provider failure.
func (e *streamEngine) steeredAttempt() bool {
	return e.modelCtx != nil && e.streamCtx.Err() == nil &&
		errors.Is(context.Cause(e.modelCtx), errModelSteered)
}

// checkpointSteeredStep persists the model attempt the steer gate stopped and
// continues the loop with whatever input the checkpoint claimed. The gate only
// fires while the provider is still sampling, so no tool call is in flight and
// the partial assistant output is the whole step. The checkpoint is persisted
// with its provider reasoning intact; only the replayed transcript rewrites
// unfinished reasoning, which providers reject on replay.
func (e *streamEngine) checkpointSteeredStep(
	attemptStep int,
	attemptSteps *[]step.Record,
) (retryMsg string, retryable bool, done bool) {
	stepIndex := e.baseCfg.StepIndexOffset + len(e.steps)
	snapshot := e.interruptedStep.snapshot(stepIndex)
	if snapshot == nil {
		// Checkpoint even a silent attempt: the original admission and the step
		// cursor must survive before the next input joins this run.
		snapshot = &step.Record{}
	}
	// Emitted before the checkpoint barrier, exactly like a completed step, so
	// the session runtime knows this step's live projection is done before it
	// anchors the claimed steer to it.
	if !e.emit(StreamEvent{Type: EventStepEnd, StepNumber: stepIndex}) {
		e.aborted = true
		return "", false, true
	}
	decorated := decorateCommittedStep(e.dynamic.stepAdditions(stepIndex), snapshot, e.toolExecutionMetadata)
	dir, err := e.baseCfg.OnSteer(e.dynamic.withMessageOrigins(e.streamCtx, stepIndex), stepIndex, decorated)
	if err != nil {
		// A failed checkpoint cannot resume: the claimed input is not durable
		// and the attempt's output is lost. Report the stable public
		// interruption error and keep the diagnostic in the log.
		e.agent.logger.ErrorContext(e.streamCtx, "checkpoint steered model invocation failed",
			slog.Int("step", attemptStep), slog.Any("error", err))
		e.aborted = true
		event := StreamEvent{Type: EventError, Error: publicResponseInterruptedError}
		if public, ok := apperror.PublicFrom(apperror.New(apperror.CodeAgentResponseInterrupted, nil), ""); ok {
			event.Code = string(public.Code)
			event.Error = public.Detail
		}
		e.turnError = event.Error
		e.emit(event)
		return "", false, true
	}
	// The published step slice carries step output only: terminal read-media
	// merging and the injected recorder position their own records around it,
	// so a decorated copy here would duplicate the admitted input.
	e.steps = append(e.steps, *snapshot)
	e.outMessages = append(e.outMessages, snapshot.Messages...)
	*attemptSteps = append(*attemptSteps, *snapshot)
	e.nextDurableStep = stepIndex + 1
	e.interruptedStep.rebase(stepIndex + 1)
	if e.resetTextLoopGuard != nil {
		e.resetTextLoopGuard()
	}
	e.takeDirective(dir)
	e.resetRefused()
	e.extend(steerCheckpointMessages(snapshot.Messages))
	return "", false, false
}

// consumeStep drains one provider stream, forwards its parts as events, and —
// when the step finished cleanly — executes its tool batch and commits it.
// retryMsg/retryable report a retryable failure the caller folds into the
// retry transition; done reports a terminal step (final answer, deferred
// approval, silent ask_user poison) that ends the engine.
func (e *streamEngine) consumeStep(
	attemptStep int,
	provParts <-chan sdk.StreamPart,
	attemptSteps *[]step.Record,
) (retryMsg string, retryable bool, done bool) {
	var (
		stepText            string
		stepTextMeta        sdk.ProviderMetadata
		stepReasoning       reasoningBlockCapture
		stepToolCalls       []sdk.ToolCall
		stepErrored         bool
		retryableFailure    bool
		failureMsg          string
		stepUsage           sdk.Usage
		stepResponse        sdk.ResponseMetadata
		stepFinishReason    sdk.FinishReason
		stepRawFinishReason string
		sawFinishStep       bool
	)

partLoop:
	for {
		var part sdk.StreamPart
		select {
		case <-e.streamCtx.Done():
			e.aborted = true
		case next, ok := <-provParts:
			if !ok {
				break partLoop
			}
			part = next
		}
		if e.aborted {
			break
		}
		e.interruptedStep.observe(part)

		switch p := part.(type) {
		case *sdk.StartPart, *sdk.StartStepPart:
			// stream start already emitted; step opening is implicit.

		case *sdk.FinishStepPart:
			sawFinishStep = true
			stepUsage = p.Usage
			stepResponse = p.Response
			stepFinishReason = p.FinishReason
			stepRawFinishReason = p.RawFinishReason
			// Emitted after every part of the step and before the commit
			// barrier runs. The session runtime uses it to know the step's
			// live projection is complete before it anchors a queue steer to
			// it; the index is the durable step index the commit will use.
			if !e.emit(StreamEvent{Type: EventStepEnd, StepNumber: e.baseCfg.StepIndexOffset + len(e.steps)}) {
				e.aborted = true
			}

		case *sdk.TextStartPart:
			if !e.emit(StreamEvent{Type: EventTextStart}) {
				e.aborted = true
			}

		case *sdk.TextDeltaPart:
			if p.Text != "" {
				stepText += p.Text
				if e.textLoopProbeBuffer != nil {
					e.textLoopProbeBuffer.Push(p.Text)
				}
				if !e.emit(StreamEvent{Type: EventTextDelta, Delta: p.Text}) {
					e.aborted = true
				}
				e.allText.WriteString(p.Text)
			}

		case *sdk.TextEndPart:
			if p.ProviderMetadata != nil {
				stepTextMeta = p.ProviderMetadata
			}
			if e.textLoopProbeBuffer != nil {
				e.textLoopProbeBuffer.Flush()
			}
			e.stepNumber++
			if !e.emit(StreamEvent{Type: EventTextEnd}) ||
				!e.emit(StreamEvent{
					Type:           EventProgress,
					StepNumber:     e.stepNumber,
					ProgressStatus: "text",
				}) {
				e.aborted = true
			}

		case *sdk.ReasoningStartPart:
			stepReasoning.observe(p.ID, "", p.Format, p.Model, p.ProviderMetadata)
			if !e.emit(StreamEvent{Type: EventReasoningStart}) {
				e.aborted = true
			}

		case *sdk.ReasoningDeltaPart:
			stepReasoning.observe(p.ID, p.Text, p.Format, p.Model, p.ProviderMetadata)
			if !e.emit(StreamEvent{Type: EventReasoningDelta, Delta: p.Text}) {
				e.aborted = true
			}

		case *sdk.ReasoningEndPart:
			stepReasoning.observe(p.ID, "", p.Format, p.Model, p.ProviderMetadata)
			if !e.emit(StreamEvent{Type: EventReasoningEnd}) {
				e.aborted = true
			}

		case *sdk.ToolInputStartPart:
			// ToolInputStartPart fires before tool input args have streamed.
			// We emit a lightweight tool_call_input_start (name + call ID, no
			// input) so the Web UI can render the tool block immediately while
			// arguments are still streaming. StreamToolCallPart below backfills
			// the fully-assembled Input under the same call ID. IM/Discuss
			// adapters do not map tool_call_input_start, so they keep their
			// single-start behavior and avoid duplicate "running" messages.
			if e.textLoopProbeBuffer != nil {
				e.textLoopProbeBuffer.Flush()
			}
			if !e.emit(StreamEvent{
				Type:       EventToolCallInputStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ID,
			}) {
				e.aborted = true
			}

		case *sdk.StreamToolCallPart:
			stepToolCalls = append(stepToolCalls, sdk.ToolCall{
				ToolCallID:       p.ToolCallID,
				ToolName:         p.ToolName,
				Input:            toolexec.ArgumentsFromValue(p.Input),
				ProviderMetadata: p.ProviderMetadata,
			})
			if e.textLoopProbeBuffer != nil {
				e.textLoopProbeBuffer.Flush()
			}
			if !e.emit(StreamEvent{
				Type:       EventToolCallStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Input:      toolexec.ArgumentsValue(p.Input),
			}) {
				e.aborted = true
			}

		case *sdk.StreamFilePart:
			mediaType := p.File.MediaType
			if mediaType == "" {
				mediaType = "image/png"
			}
			if !e.emit(StreamEvent{
				Type: EventAttachment,
				Attachments: []FileAttachment{{
					Type: "image",
					URL:  fmt.Sprintf("data:%s;base64,%s", mediaType, p.File.Data),
					Mime: mediaType,
				}},
			}) {
				e.aborted = true
			}

		case *sdk.ErrorPart:
			if contextStepBudgetError(e.streamCtx) != nil {
				e.aborted = true
				break
			}
			if e.streamCtx.Err() != nil {
				// The provider is reporting the run's own cancellation.
				e.aborted = true
				break
			}
			failureMsg, retryableFailure = e.streamFailure(p.Error)
			stepErrored = true

		case *sdk.FinishPart:
			// The provider's own finish is swallowed; the step boundary the
			// consumers see is FinishStepPart.
		}

		if e.aborted || retryableFailure {
			break
		}
	}

	if e.aborted {
		// A provider is expected to close its stream when the context is
		// cancelled, but run termination must not depend on that cooperation.
		// Preserve the final snapshot when it arrives promptly, then stop
		// waiting so the segment owner can finalize the run as aborted.
		drainStreamUntilClosed(provParts, streamCancelDrainGrace, e.interruptedStep.observe)
		return "", false, true
	}
	if retryableFailure {
		// Drain the failed stream before folding: the retry input must not
		// race the provider goroutine still flushing its buffer.
		for part := range provParts {
			e.interruptedStep.observe(part)
		}
		return failureMsg, true, false
	}
	if stepErrored {
		// A provider error that is neither retryable nor a cancellation:
		// streamFailure published it and marked the run aborted; the steps
		// committed so far stand.
		return "", false, true
	}
	if !sawFinishStep {
		if e.streamCtx.Err() != nil {
			e.aborted = true
			return "", false, true
		}
		if e.steeredAttempt() {
			return e.checkpointSteeredStep(attemptStep, attemptSteps)
		}
		msg, retriable := e.streamFailure(fmt.Errorf("twilightai: stream step %d ended before finish-step", attemptStep))
		return msg, retriable, e.aborted
	}

	// The step's model result is assembled from the parts the loop consumed
	// rather than read from a provider-side result: finish-step detection,
	// interruption and the live events all run on these same accumulators.
	result := sdk.ModelResult{
		Text:                 stepText,
		TextProviderMetadata: stepTextMeta,
		Reasoning:            stepReasoning.text(),
		ReasoningParts:       stepReasoning.parts,
		FinishReason:         stepFinishReason,
		RawFinishReason:      stepRawFinishReason,
		Usage:                stepUsage,
		ToolCalls:            stepToolCalls,
		Response:             stepResponse,
	}
	// The tool batch's OnPart callback bridges approval, progress, result,
	// and error parts onto the event channel.
	sr, kind, err := settleStep(e.streamCtx, e.dispatch, result, stepTextMeta, e.bridgeToolPart)
	if err != nil {
		msg, retriable := e.streamFailure(err)
		return msg, retriable, e.aborted
	}
	dir, err := e.commitStep(attemptStep, &sr)
	if err != nil {
		msg, retriable := e.streamFailure(err)
		return msg, retriable, e.aborted
	}
	e.steps = append(e.steps, sr)
	e.outMessages = append(e.outMessages, sr.Messages...)
	*attemptSteps = append(*attemptSteps, sr)
	if kind == stepDeferred {
		e.deferred = sr.Deferred
		e.afterStep(attemptStep, &sr)
		return "", false, true
	}
	e.afterStep(attemptStep, &sr)
	e.takeDirective(dir)
	refusedOut := e.refusedBatchEndsRun(kind)
	if kind == stepFinal {
		// A directive or a refreshed tool set gives the model another call
		// on the same thread; the step is committed either way.
		if !e.continues() {
			return "", false, true
		}
		e.extend(sr.Messages)
		return "", false, false
	}
	// A tool-loop abort raised by the batch stops the run after the step
	// committed, matching the legacy flow where the guard fenced only the
	// next provider call.
	if e.aborted {
		return "", false, true
	}
	if refusedOut {
		// Every call of the last maxRefusedBatches steps was refused and
		// nothing new arrived: the model is not converging on a call the
		// loop can run. Ended like a detected tool loop, with the answered
		// steps committed.
		e.cancel(ErrToolLoopDetected)
		e.aborted = true
		return "", false, true
	}
	e.extend(sr.Messages)
	return "", false, false
}

// streamFailure emits one raw EventError for a provider-reported failure and
// reports whether the run should retry it mid-stream. A failure observed after
// a context-budget cancellation stays off the wire: the segment owner emits
// the stable public error instead. Non-retryable failures abort the engine.
func (e *streamEngine) streamFailure(err error) (string, bool) {
	if contextStepBudgetError(e.streamCtx) != nil {
		e.aborted = true
		return "", false
	}
	msg := err.Error()
	e.turnError = msg
	e.emit(StreamEvent{Type: EventError, Error: msg})
	if isRetryableStreamError(err) {
		return msg, true
	}
	e.aborted = true
	return "", false
}

// commitStep runs the synchronous durability barrier for one completed step.
// The durable index continues across mid-stream retries: an errored step never
// commits, so the regenerated step reuses its index.
func (e *streamEngine) commitStep(attemptStep int, sr *step.Record) (StepDirective, error) {
	stepIndex := e.baseCfg.StepIndexOffset + len(e.steps)
	var dir StepDirective
	if e.baseCfg.OnStepCommitted != nil {
		decorated := decorateCommittedStep(e.dynamic.stepAdditions(stepIndex), sr, e.toolExecutionMetadata)
		var err error
		dir, err = e.baseCfg.OnStepCommitted(e.dynamic.withMessageOrigins(e.streamCtx, stepIndex), stepIndex, decorated)
		if err != nil {
			// The step's tools ran; a retry would run them again. The tag keeps
			// the failure out of the mid-stream retry path.
			return StepDirective{}, tagStepCommitError(fmt.Errorf("twilightai: commit step %d: %w", attemptStep, err))
		}
	}
	e.nextDurableStep = stepIndex + 1
	if e.baseCfg.capabilityChanges != nil && e.baseCfg.capabilityChanges.Swap(false) && sr.Deferred == nil && e.refreshTools != nil {
		refreshed, err := e.refreshTools()
		if err != nil {
			return StepDirective{}, tagStepCommitError(fmt.Errorf("twilightai: refresh capabilities after step %d: %w", attemptStep, err))
		}
		e.pendingRefresh = &refreshed
	}
	return dir, nil
}

// drainInjectedMessages moves queued live injections into the thread at the
// current step boundary. Each injected user message is tracked as a loop-owned
// dynamic input; its durability is decided when the boundary's provider
// attempt is published and its step commits.
func (e *streamEngine) drainInjectedMessages(boundary int, messages []sdk.Message) []sdk.Message {
	cfg := e.baseCfg
	if cfg.InjectCh == nil {
		return messages
	}
	for {
		select {
		case injected, ok := <-cfg.InjectCh:
			if !ok {
				break
			}
			text := injectedMessageText(injected)
			if text != "" || (cfg.SupportsImageInput && len(injected.ImageParts) > 0) {
				var extra []sdk.MessagePart
				if cfg.SupportsImageInput {
					for _, img := range injected.ImageParts {
						if strings.TrimSpace(img.Image) != "" {
							extra = append(extra, img)
						}
					}
				}
				message := sdk.UserMessage(text, extra...)
				cfg.ContextMutations.Record(contextfrag.MutationInjectedMessage, fmt.Sprintf("bytes=%d", len(text)))
				e.dynamic.append(message, false, text, len(messages))
				messages = append(messages, message)
				e.agent.logger.InfoContext(e.streamCtx, "injected user message into agent stream",
					slog.String("bot_id", cfg.Identity.BotID),
					slog.Int("after_step", boundary-1),
					slog.Int("image_parts", len(extra)),
				)
			}
			continue
		default:
		}
		break
	}
	return messages
}

// afterStep mirrors the legacy OnStep hook: cache-usage accounting and the
// after-model-call hook run for every committed step, with the call-local step
// index the previous per-invocation counter produced.
func (e *streamEngine) afterStep(attemptStep int, sr *step.Record) {
	recordContextCacheUsage(e.cfg.ContextMutations, attemptStep, sr)
	e.agent.runAfterModelCallHook(e.streamCtx, e.cfg, sr, attemptStep)
}

// bridgeToolPart is the toolexec.ExecuteTools OnPart callback. Parallel tool
// executions may invoke it concurrently, so it serializes on e.mu before
// touching the interrupted-step capture or the event channel.
func (e *streamEngine) bridgeToolPart(part sdk.StreamPart) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.interruptedStep.observe(part)
	e.forwardToolPart(part)
}

// forwardToolPart converts one tool-execution part into its stream event.
// Callers hold e.mu. ToolOutputDeniedPart intentionally maps to no event,
// matching the legacy consumer switch.
func (e *streamEngine) forwardToolPart(part sdk.StreamPart) {
	switch p := part.(type) {
	case *toolexec.ToolProgressPart:
		if !e.emit(StreamEvent{
			Type:       EventToolCallProgress,
			ToolName:   p.ToolName,
			ToolCallID: p.ToolCallID,
			Metadata:   e.toolExecutionMetadata.metadata(p.ToolCallID),
			Progress:   toolexec.OutputValue(p.Content),
		}) {
			e.aborted = true
		}

	case *toolexec.ToolApprovalRequestPart:
		eventType := EventToolApprovalRequest
		var userInputID string
		var approvalID string
		if isUserInputMetadata(p.Metadata) {
			eventType = EventUserInputRequest
			userInputID = p.ApprovalID
		} else {
			approvalID = p.ApprovalID
		}
		if !e.emit(StreamEvent{
			Type:        eventType,
			ToolName:    p.ToolName,
			ToolCallID:  p.ToolCallID,
			ApprovalID:  approvalID,
			UserInputID: userInputID,
			ShortID:     approvalShortID(p.Metadata),
			Status:      "pending",
			Input:       toolexec.ArgumentsValue(p.Input),
			Metadata:    p.Metadata,
		}) {
			e.aborted = true
		}

	case *toolexec.StreamToolResultPart:
		shouldAbort := e.toolLoopAbortCallIDs.Take(p.ToolCallID)
		e.stepNumber++
		if !e.emit(StreamEvent{
			Type:       EventToolCallEnd,
			ToolName:   p.ToolName,
			ToolCallID: p.ToolCallID,
			Input:      toolexec.ArgumentsValue(p.Input),
			Metadata:   e.toolExecutionMetadata.metadata(p.ToolCallID),
			Result:     toolexec.OutputValue(p.Output),
		}) || !e.emit(StreamEvent{
			Type:           EventProgress,
			StepNumber:     e.stepNumber,
			ToolName:       p.ToolName,
			ProgressStatus: "tool_result",
		}) {
			e.aborted = true
		}
		if shouldAbort {
			e.agent.logger.WarnContext(e.streamCtx, "tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
			e.cancel(ErrToolLoopDetected)
			e.aborted = true
		}

	case *toolexec.StreamToolErrorPart:
		// Take before errors.Is so registry IDs from the loop guard are always cleared.
		tookLoopAbort := e.toolLoopAbortCallIDs.Take(p.ToolCallID)
		shouldAbort := errors.Is(p.Error, ErrToolLoopDetected) || tookLoopAbort
		if !e.emit(StreamEvent{
			Type:       EventToolCallEnd,
			ToolName:   p.ToolName,
			ToolCallID: p.ToolCallID,
			Metadata:   e.toolExecutionMetadata.metadata(p.ToolCallID),
			Error:      p.Error.Error(),
		}) {
			e.aborted = true
		}
		if shouldAbort {
			e.agent.logger.WarnContext(e.streamCtx, "tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
			e.cancel(ErrToolLoopDetected)
			e.aborted = true
		}
	}
}

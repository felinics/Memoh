package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/background"
	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	tools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/hooks"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

// Agent is the core agent that handles LLM interactions.
type Agent struct {
	client             *sdk.Client
	toolProviders      []tools.ToolProvider
	bridgeProvider     bridge.Provider
	hookService        *hooks.Service
	logger             *slog.Logger
	limits             Limits
	contextViewApplier ContextViewApplier
}

const streamCancelDrainGrace = 250 * time.Millisecond

// New creates a new Agent with the given dependencies.
func New(deps Deps) *Agent {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		client:             sdk.NewClient(),
		bridgeProvider:     deps.BridgeProvider,
		hookService:        deps.HookService,
		logger:             logger.With(slog.String("service", "agent/runtime/native")),
		limits:             deps.Limits.Normalize(),
		contextViewApplier: deps.ContextViewApplier,
	}
}

// applyContextView compiles the provider-facing fields from authoritative
// fragments when the application installed the PR1 compiler. Direct Agent
// users retain the legacy refresh path.
func (a *Agent) applyContextView(ctx context.Context, cfg RunConfig) (RunConfig, error) {
	if a != nil && a.contextViewApplier != nil {
		return a.contextViewApplier(ctx, cfg)
	}
	return cfg.RefreshContextFrag(), nil
}

const publicContextPreparationError = "The model context could not be prepared."

func contextViewStreamError(err error) StreamEvent {
	var code apperror.Code
	switch {
	case errors.Is(err, contextfrag.ErrProtectedContextOverflow):
		code = apperror.CodeContextProtectedOverflow
	case errors.Is(err, contextfrag.ErrBudgetUnsatisfied):
		code = apperror.CodeContextBudgetUnsatisfied
	default:
		return StreamEvent{Type: EventError, Error: publicContextPreparationError}
	}
	public, ok := apperror.PublicFrom(apperror.New(code, nil), "")
	if !ok {
		return StreamEvent{Type: EventError, Error: publicContextPreparationError}
	}
	return StreamEvent{Type: EventError, Code: string(public.Code), Error: public.Detail}
}

// BridgeProvider returns the underlying bridge provider (workspace manager).
func (a *Agent) BridgeProvider() bridge.Provider {
	return a.bridgeProvider
}

func (a *Agent) Limits() Limits {
	if a == nil {
		return DefaultLimits()
	}
	return a.limits.Normalize()
}

// SetToolProviders sets the tool providers after construction.
// This allows breaking dependency cycles in the DI graph.
func (a *Agent) SetToolProviders(providers []tools.ToolProvider) {
	a.toolProviders = providers
}

// Stream runs the agent in streaming mode, emitting events to the returned channel.
func (a *Agent) Stream(ctx context.Context, cfg RunConfig) <-chan StreamEvent {
	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		a.runStream(ctx, cfg, ch)
	}()
	return ch
}

// Generate runs the agent in non-streaming mode, returning the complete result.
func (a *Agent) Generate(ctx context.Context, cfg RunConfig) (*GenerateResult, error) {
	return a.runGenerate(ctx, cfg)
}

func (a *Agent) ExecuteTool(ctx context.Context, cfg RunConfig, call sdk.ToolCall) (sdk.ToolResultPart, error) {
	sdkTools, _, _, _, err := a.assembleTools(ctx, cfg, nil, false)
	if err != nil {
		return sdk.ToolResultPart{}, fmt.Errorf("assemble tools: %w", err)
	}
	sdkTools, _ = decorateReadMediaTools(cfg.Model, sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, a.Limits().ToolOutputLimit())
	for i := range sdkTools {
		tool := sdkTools[i]
		if tool.Name != call.ToolName {
			continue
		}
		if tool.Execute == nil {
			return sdk.ToolResultPart{}, fmt.Errorf("tool %q has no execute handler", call.ToolName)
		}
		execCtx := &sdk.ToolExecContext{
			Context:    ctx,
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
		}
		output, err := tool.Execute(execCtx, call.Input)
		if err != nil {
			limitedErr := tools.LimitToolError(err, "tool result ("+call.ToolName+")", a.Limits().ToolOutputLimit())
			return sdk.ToolResultPart{
				ToolCallID: call.ToolCallID,
				ToolName:   call.ToolName,
				Result:     limitedErr.Error(),
				IsError:    true,
			}, nil
		}
		return sdk.ToolResultPart{
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			Result:     publicReadMediaToolResult(output),
		}, nil
	}
	return sdk.ToolResultPart{}, fmt.Errorf("tool %q not found", call.ToolName)
}

// sendEvent sends an event to the stream channel. It returns false if the
// context was cancelled (consumer stopped reading), allowing the caller to
// abort cleanly instead of leaking the goroutine on a blocked channel send.
func sendEvent(ctx context.Context, ch chan<- StreamEvent, evt StreamEvent) bool {
	select {
	case ch <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *Agent) runStream(ctx context.Context, cfg RunConfig, ch chan<- StreamEvent) {
	if cfg.ContextLifecycle == nil {
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
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

	// Stream emitter: tools targeting the current conversation push
	// side-effect events (attachments, reactions, speech) directly here.
	// Uses sendEvent to avoid goroutine leaks when the consumer stops reading.
	streamEmitter := tools.StreamEmitter(func(evt tools.ToolStreamEvent) {
		sendEvent(ctx, ch, toolStreamEventToAgentEvent(evt))
	})
	if cfg.ForkContext == nil {
		cfg.ForkContext = tools.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	}

	var sdkTools []sdk.Tool
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
			// Must run before buildGenerateOptions so prompt caching and
			// background task summaries see the usage-augmented text.
			cfg.System = appendToolUsageToSystem(cfg.System, toolUsage)
			cfg.ContextToolUsage = toolUsage
			cfg.ContextToolUsageFrags = toolUsageFrags
		}
	}
	limit := a.Limits().ToolOutputLimit()
	sdkTools, readMediaState := decorateReadMediaTools(cfg.Model, sdkTools)
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, true)
	var contextViewErr error
	cfg, contextViewErr = a.applyContextView(streamCtx, cfg)
	if contextViewErr != nil {
		publicError := contextViewStreamError(contextViewErr)
		turnError = publicError.Error
		a.logger.Warn("context view preflight failed", slog.Any("error", contextViewErr))
		sendEvent(ctx, ch, publicError)
		return
	}
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	approvalTools := append([]sdk.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(ctx, cfg, sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(func(call sdk.ToolCall, metadata map[string]any) {
		sendEvent(ctx, ch, StreamEvent{
			Type:       EventToolCallMetadata,
			ToolName:   call.ToolName,
			ToolCallID: call.ToolCallID,
			Input:      call.Input,
			Metadata:   metadata,
		})
	})
	cfg.ToolApprovalHandler = toolExecutionMetadata.wrap(cfg.ToolApprovalHandler)

	// Loop detection setup
	var textLoopGuard *TextLoopGuard
	var textLoopProbeBuffer *TextLoopProbeBuffer
	var toolLoopGuard *ToolLoopGuard
	toolLoopAbortCallIDs := newToolAbortRegistry()
	if cfg.LoopDetection.Enabled {
		textLoopGuard = NewTextLoopGuard(LoopDetectedStreakThreshold, LoopDetectedMinNewGramsPerChunk, SentialOptions{})
		textLoopProbeBuffer = NewTextLoopProbeBuffer(LoopDetectedProbeChars, func(text string) {
			result := textLoopGuard.Inspect(text)
			if result.Abort {
				a.logger.Warn("text loop detected, will abort")
				aborted = true
				cancel(ErrTextLoopDetected)
			}
		})
		toolLoopGuard = NewToolLoopGuard(ToolLoopRepeatThreshold, ToolLoopWarningsBeforeAbort)
	}

	// Wrap tools with loop detection
	if toolLoopGuard != nil {
		sdkTools = wrapToolsWithLoopGuard(sdkTools, toolLoopGuard, toolLoopAbortCallIDs)
	}

	var prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams
	if readMediaState != nil {
		prepareStep = readMediaState.prepareStep
	}

	initialMsgCount := len(cfg.Messages)

	if cfg.InjectCh != nil {
		basePrepare := prepareStep
		prepareStep = func(p *sdk.GenerateParams) *sdk.GenerateParams {
			if basePrepare != nil {
				if override := basePrepare(p); override != nil {
					p = override
				}
			}
			for {
				select {
				case injected, ok := <-cfg.InjectCh:
					if !ok {
						break
					}
					text := strings.TrimSpace(injected.HeaderifiedText)
					if text == "" {
						text = strings.TrimSpace(injected.Text)
					}
					if text != "" || (cfg.SupportsImageInput && len(injected.ImageParts) > 0) {
						insertAfter := len(p.Messages) - initialMsgCount
						var extra []sdk.MessagePart
						if cfg.SupportsImageInput {
							for _, img := range injected.ImageParts {
								if strings.TrimSpace(img.Image) != "" {
									extra = append(extra, img)
								}
							}
						}
						p.Messages = append(p.Messages, sdk.UserMessage(text, extra...))
						if cfg.InjectedRecorder != nil {
							cfg.InjectedRecorder(text, insertAfter)
						}
						a.logger.Info("injected user message into agent stream",
							slog.String("bot_id", cfg.Identity.BotID),
							slog.Int("insert_after", insertAfter),
							slog.Int("image_parts", len(extra)),
						)
					}
					continue
				default:
				}
				break
			}
			return p
		}
	}

	prepareStep, committedStepMessages := capturePreparedStepMessages(prepareStep)
	prepareStep = a.wrapPrepareStepWithModelHook(streamCtx, cfg, prepareStep)
	var err error
	cfg, err = a.applyBeforeModelCallHook(streamCtx, cfg, 0)
	if err != nil {
		turnError = err.Error()
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}
	opts := a.buildGenerateOptions(cfg, sdkTools, approvalTools, prepareStep)
	modelStepIndex := 0
	opts = append(opts, sdk.WithOnStep(func(step *sdk.StepResult) *sdk.GenerateParams {
		a.runAfterModelCallHook(streamCtx, cfg, step, modelStepIndex)
		modelStepIndex++
		return nil
	}))
	var nextDurableStep int
	var onStepCommitted func(context.Context, int, *sdk.StepResult) error
	if cfg.OnStepCommitted != nil {
		onStepCommitted = func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
			if err := cfg.OnStepCommitted(ctx, stepIndex, committedStepMessages.decorate(stepIndex, step, toolExecutionMetadata)); err != nil {
				return err
			}
			nextDurableStep = stepIndex + 1
			return nil
		}
		opts = append(opts, sdk.WithOnStepCommitted(onStepCommitted))
	}

	retryCfg := cfg.Retry
	if retryCfg.MaxAttempts <= 0 {
		retryCfg = DefaultRetryConfig()
	}

	var streamResult *sdk.StreamResult
	for attempt := 0; attempt < retryCfg.MaxAttempts; attempt++ {
		var err error
		streamResult, err = a.client.StreamText(streamCtx, opts...)
		if err == nil {
			break
		}
		if !isRetryableStreamError(err) {
			turnError = fmt.Sprintf("stream start: %v", err)
			sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
			return
		}
		a.logger.Warn("stream start failed, retrying",
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", retryCfg.MaxAttempts),
			slog.String("error", err.Error()),
		)
		if !sendEvent(ctx, ch, StreamEvent{
			Type:       EventRetry,
			Attempt:    attempt + 1,
			MaxAttempt: retryCfg.MaxAttempts,
			RetryError: err.Error(),
		}) {
			return
		}
		if attempt+1 >= retryCfg.MaxAttempts {
			turnError = fmt.Sprintf("stream start: all %d attempts failed (last: %v)", retryCfg.MaxAttempts, err)
			sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
			return
		}
		delay := retryDelay(attempt, retryCfg)
		if delay > 0 {
			if err := sleepWithContext(streamCtx, delay); err != nil {
				turnError = fmt.Sprintf("stream start: context cancelled during retry: %v", err)
				sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
				return
			}
		}
	}

	sendEvent(ctx, ch, StreamEvent{Type: EventAgentStart})

	var allText strings.Builder
	var interruptedStep interruptedStepCapture
	var interruptedMessages []sdk.Message
	stepNumber := 0

	streamClosed := false
	for !aborted && !streamClosed {
		var part sdk.StreamPart
		select {
		case <-streamCtx.Done():
			aborted = true
			continue
		case next, ok := <-streamResult.Stream:
			if !ok {
				streamClosed = true
				continue
			}
			part = next
		}
		interruptedStep.observe(part)

		switch p := part.(type) {
		case *sdk.StartPart:
			_ = p // stream start already emitted

		case *sdk.TextStartPart:
			if !sendEvent(ctx, ch, StreamEvent{Type: EventTextStart}) {
				aborted = true
			}

		case *sdk.TextDeltaPart:
			if p.Text != "" {
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Push(p.Text)
				}
				if !sendEvent(ctx, ch, StreamEvent{Type: EventTextDelta, Delta: p.Text}) {
					aborted = true
				}
				allText.WriteString(p.Text)
			}

		case *sdk.TextEndPart:
			if textLoopProbeBuffer != nil {
				textLoopProbeBuffer.Flush()
			}
			stepNumber++
			if !sendEvent(ctx, ch, StreamEvent{Type: EventTextEnd}) ||
				!sendEvent(ctx, ch, StreamEvent{
					Type:           EventProgress,
					StepNumber:     stepNumber,
					ProgressStatus: "text",
				}) {
				aborted = true
			}

		case *sdk.ReasoningStartPart:
			if !sendEvent(ctx, ch, StreamEvent{Type: EventReasoningStart}) {
				aborted = true
			}

		case *sdk.ReasoningDeltaPart:
			if !sendEvent(ctx, ch, StreamEvent{Type: EventReasoningDelta, Delta: p.Text}) {
				aborted = true
			}

		case *sdk.ReasoningEndPart:
			if !sendEvent(ctx, ch, StreamEvent{Type: EventReasoningEnd}) {
				aborted = true
			}

		case *sdk.ToolInputStartPart:
			// ToolInputStartPart fires before tool input args have streamed.
			// We emit a lightweight tool_call_input_start (name + call ID, no
			// input) so the Web UI can render the tool block immediately while
			// arguments are still streaming. StreamToolCallPart below backfills
			// the fully-assembled Input under the same call ID. IM/Discuss
			// adapters do not map tool_call_input_start, so they keep their
			// single-start behavior and avoid duplicate "running" messages.
			if textLoopProbeBuffer != nil {
				textLoopProbeBuffer.Flush()
			}
			if !sendEvent(ctx, ch, StreamEvent{
				Type:       EventToolCallInputStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ID,
			}) {
				aborted = true
			}

		case *sdk.StreamToolCallPart:
			if textLoopProbeBuffer != nil {
				textLoopProbeBuffer.Flush()
			}
			if !sendEvent(ctx, ch, StreamEvent{
				Type:       EventToolCallStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Input:      p.Input,
			}) {
				aborted = true
			}

		case *sdk.ToolProgressPart:
			if !sendEvent(ctx, ch, StreamEvent{
				Type:       EventToolCallProgress,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Progress:   p.Content,
			}) {
				aborted = true
			}

		case *sdk.ToolApprovalRequestPart:
			eventType := EventToolApprovalRequest
			var userInputID string
			var approvalID string
			if isUserInputMetadata(p.Metadata) {
				eventType = EventUserInputRequest
				userInputID = p.ApprovalID
			} else {
				approvalID = p.ApprovalID
			}
			if !sendEvent(ctx, ch, StreamEvent{
				Type:        eventType,
				ToolName:    p.ToolName,
				ToolCallID:  p.ToolCallID,
				ApprovalID:  approvalID,
				UserInputID: userInputID,
				ShortID:     approvalShortID(p.Metadata),
				Status:      "pending",
				Input:       p.Input,
				Metadata:    p.Metadata,
			}) {
				aborted = true
			}

		case *sdk.StreamToolResultPart:
			shouldAbort := toolLoopAbortCallIDs.Take(p.ToolCallID)
			stepNumber++
			if !sendEvent(ctx, ch, StreamEvent{
				Type:       EventToolCallEnd,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Input:      p.Input,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Result:     p.Output,
			}) || !sendEvent(ctx, ch, StreamEvent{
				Type:           EventProgress,
				StepNumber:     stepNumber,
				ToolName:       p.ToolName,
				ProgressStatus: "tool_result",
			}) {
				aborted = true
			}
			if shouldAbort {
				a.logger.Warn("tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
				cancel(ErrToolLoopDetected)
				aborted = true
			}

		case *sdk.StreamToolErrorPart:
			// Take before errors.Is so registry IDs from the loop guard are always cleared.
			tookLoopAbort := toolLoopAbortCallIDs.Take(p.ToolCallID)
			shouldAbort := errors.Is(p.Error, ErrToolLoopDetected) || tookLoopAbort
			if !sendEvent(ctx, ch, StreamEvent{
				Type:       EventToolCallEnd,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Error:      p.Error.Error(),
			}) {
				aborted = true
			}
			if shouldAbort {
				a.logger.Warn("tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
				cancel(ErrToolLoopDetected)
				aborted = true
			}

		case *sdk.StreamFilePart:
			mediaType := p.File.MediaType
			if mediaType == "" {
				mediaType = "image/png"
			}
			if !sendEvent(ctx, ch, StreamEvent{
				Type: EventAttachment,
				Attachments: []FileAttachment{{
					Type: "image",
					URL:  fmt.Sprintf("data:%s;base64,%s", mediaType, p.File.Data),
					Mime: mediaType,
				}},
			}) {
				aborted = true
			}

		case *sdk.ErrorPart:
			errMsg := p.Error.Error()
			if isAskUserArgumentParseError(errMsg) {
				continue
			}
			turnError = errMsg
			sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: errMsg})

			// Mid-stream retry: if the error is retryable, attempt to continue
			// the agent run from the accumulated state. This also handles
			// errors at step 0 (e.g. timeout awaiting response headers) since
			// no work has been completed yet and retrying from the start is safe.
			if isRetryableStreamError(p.Error) {
				streamResult, aborted = a.runMidStreamRetry(
					ctx, streamCtx, cancel, toolLoopAbortCallIDs,
					ch, cfg, sdkTools, approvalTools, prepareStep, streamResult,
					committedStepMessages, onStepCommitted, &interruptedStep,
					stepNumber, errMsg, &allText, textLoopProbeBuffer,
				)
				if !aborted {
					turnError = ""
				}
			} else {
				aborted = true
			}

		case *sdk.AbortPart:
			aborted = true

		case *sdk.FinishPart:
			// handled after loop
		}

		if aborted {
			break
		}
	}
	if ctx.Err() != nil {
		aborted = true
	}

	if aborted && !streamClosed {
		// A provider is expected to close its stream when the context is
		// cancelled, but run termination must not depend on that cooperation.
		// Preserve the final snapshot when it arrives promptly, then stop
		// waiting so the caller can fence and finalize the run as aborted.
		cancel(context.Canceled)
		streamClosed = drainStreamUntilClosed(streamResult.Stream, streamCancelDrainGrace, interruptedStep.observe)
	}

	// Only external cancellation can represent a user/session abort. Provider
	// errors and loop guards keep their existing failure semantics.
	//
	// A closed stream is what makes this write safe, so it is required rather
	// than merely convenient. It means the SDK's step goroutine has returned:
	// no further complete step can commit after this checkpoint, and the
	// prepared-message capture read below is published by that goroutine's exit
	// instead of racing its next PrepareStep. When a provider refuses to close
	// within the drain grace, the checkpoint is dropped rather than risk
	// duplicating an answer the SDK is still about to commit.
	if aborted && streamClosed && ctx.Err() != nil && cfg.OnStepInterrupted != nil {
		stepIndex := nextDurableStep
		if step := interruptedStep.snapshot(stepIndex); step != nil {
			step = committedStepMessages.decorate(stepIndex, step, toolExecutionMetadata)
			if err := cfg.OnStepInterrupted(context.WithoutCancel(streamCtx), stepIndex, step); err != nil {
				// An owner that lost its lease, or a run another writer already
				// finalized, is an expected outcome of racing an abort.
				a.logger.Warn("persist interrupted model step failed", slog.Any("error", err))
			} else {
				interruptedMessages = step.Messages
			}
		}
	}

	if textLoopProbeBuffer != nil {
		textLoopProbeBuffer.Flush()
	}

	var finalMessages []sdk.Message
	var totalUsage sdk.Usage
	if streamClosed {
		finalMessages = streamResult.Messages
		if readMediaState != nil {
			finalMessages = readMediaState.mergeMessages(streamResult.Steps, finalMessages)
		}
		if streamResult.DeferredToolApproval != nil {
			finalMessages = annotateDeferredApproval(finalMessages, *streamResult.DeferredToolApproval)
		}
		finalMessages = toolExecutionMetadata.annotate(finalMessages)
		for _, step := range streamResult.Steps {
			totalUsage.InputTokens += step.Usage.InputTokens
			totalUsage.OutputTokens += step.Usage.OutputTokens
			totalUsage.TotalTokens += step.Usage.TotalTokens
			totalUsage.ReasoningTokens += step.Usage.ReasoningTokens
			totalUsage.CachedInputTokens += step.Usage.CachedInputTokens
			totalUsage.InputTokenDetails.NoCacheTokens += step.Usage.InputTokenDetails.NoCacheTokens
			totalUsage.InputTokenDetails.CacheReadTokens += step.Usage.InputTokenDetails.CacheReadTokens
			totalUsage.InputTokenDetails.CacheWriteTokens += step.Usage.InputTokenDetails.CacheWriteTokens
			totalUsage.OutputTokenDetails.TextTokens += step.Usage.OutputTokenDetails.TextTokens
			totalUsage.OutputTokenDetails.ReasoningTokens += step.Usage.OutputTokenDetails.ReasoningTokens
		}
	}
	finalMessages = append(finalMessages, interruptedMessages...)
	usageJSON, _ := json.Marshal(totalUsage)

	termEvent := StreamEvent{
		Messages: mustMarshal(finalMessages),
		Usage:    usageJSON,
	}
	if streamClosed && streamResult.DeferredToolApproval != nil {
		termEvent.ApprovalID = streamResult.DeferredToolApproval.ApprovalID
		if isUserInputMetadata(streamResult.DeferredToolApproval.Metadata) {
			termEvent.UserInputID = streamResult.DeferredToolApproval.ApprovalID
		}
		termEvent.ShortID = approvalShortID(streamResult.DeferredToolApproval.Metadata)
		termEvent.Status = "pending"
		termEvent.Metadata = streamResult.DeferredToolApproval.Metadata
		if toolName, ok := streamResult.DeferredToolApproval.Metadata["tool_name"].(string); ok {
			termEvent.ToolName = toolName
		}
		if toolCallID, ok := streamResult.DeferredToolApproval.Metadata["tool_call_id"].(string); ok {
			termEvent.ToolCallID = toolCallID
		}
	}
	if aborted {
		termEvent.Type = EventAgentAbort
	} else {
		termEvent.Type = EventAgentEnd
		// Warn if LLM produced no text and no tool calls — likely a context overflow.
		if allText.Len() == 0 && stepNumber == 0 {
			a.logger.Warn("agent produced empty response (no text, no tool calls)",
				slog.String("bot_id", cfg.Identity.BotID),
				slog.Int("input_messages", len(cfg.Messages)),
				slog.Int("input_tokens", totalUsage.InputTokens),
			)
		}
	}
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

// drainStreamUntilClosed consumes what the provider still has buffered after
// cancellation. observe sees those parts too: a finish-step or tool call left in
// the buffer is state this run reached, so an interrupted checkpoint must be
// judged against it rather than against the prefix the event loop happened to
// read before the abort.
func drainStreamUntilClosed(stream <-chan sdk.StreamPart, grace time.Duration, observe func(sdk.StreamPart)) bool {
	if stream == nil {
		return true
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		select {
		case part, ok := <-stream:
			if !ok {
				return true
			}
			if observe != nil {
				observe(part)
			}
		case <-timer.C:
			return false
		}
	}
}

func (a *Agent) runGenerate(ctx context.Context, cfg RunConfig) (result *GenerateResult, retErr error) {
	if cfg.ContextLifecycle == nil {
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	genCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer func() {
		event := hooks.EventTurnEnd
		errMsg := ""
		if retErr != nil {
			event = hooks.EventTurnError
			errMsg = retErr.Error()
		}
		a.runTurnHook(context.WithoutCancel(ctx), cfg, event, errMsg)
	}()
	loopAbort := newLoopAbortState()

	// Collecting emitter: tools push side-effect events here during generation.
	collected := newToolEventCollector()
	defer collected.Close()
	collectEmitter := tools.StreamEmitter(func(evt tools.ToolStreamEvent) {
		collected.Add(evt)
	})
	if cfg.ForkContext == nil {
		cfg.ForkContext = tools.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	}

	var sdkTools []sdk.Tool
	cfg.ContextToolDefsResolved = true
	if cfg.SupportsToolCall {
		var toolUsage string
		var toolUsageFrags []contextfrag.ContextFrag
		var toolDefs []contextfrag.ToolDefAccounting
		var err error
		sdkTools, toolUsage, toolUsageFrags, toolDefs, err = a.assembleTools(genCtx, cfg, collectEmitter, false)
		if err != nil {
			return nil, fmt.Errorf("assemble tools: %w", err)
		}
		cfg.ContextToolDefs = toolDefs
		if toolUsage != "" {
			// Must run before buildGenerateOptions so prompt caching and
			// background task summaries see the usage-augmented text.
			cfg.System = appendToolUsageToSystem(cfg.System, toolUsage)
			cfg.ContextToolUsage = toolUsage
			cfg.ContextToolUsageFrags = toolUsageFrags
		}
	}
	limit := a.Limits().ToolOutputLimit()
	sdkTools, readMediaState := decorateReadMediaTools(cfg.Model, sdkTools)
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, false)
	var contextViewErr error
	cfg, contextViewErr = a.applyContextView(genCtx, cfg)
	if contextViewErr != nil {
		return nil, contextViewErr
	}
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	approvalTools := append([]sdk.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(ctx, cfg, sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(nil)
	cfg.ToolApprovalHandler = toolExecutionMetadata.wrap(cfg.ToolApprovalHandler)

	var toolLoopGuard *ToolLoopGuard
	var textLoopGuard *TextLoopGuard
	toolLoopAbortCallIDs := newToolAbortRegistry()
	if cfg.LoopDetection.Enabled {
		toolLoopGuard = NewToolLoopGuard(ToolLoopRepeatThreshold, ToolLoopWarningsBeforeAbort)
		textLoopGuard = NewTextLoopGuard(LoopDetectedStreakThreshold, LoopDetectedMinNewGramsPerChunk, SentialOptions{})
	}

	if toolLoopGuard != nil {
		sdkTools = wrapToolsWithLoopGuard(sdkTools, toolLoopGuard, toolLoopAbortCallIDs)
	}

	var prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams
	if readMediaState != nil {
		prepareStep = readMediaState.prepareStep
	}

	prepareStep, committedStepMessages := capturePreparedStepMessages(prepareStep)
	prepareStep = a.wrapPrepareStepWithModelHook(genCtx, cfg, prepareStep)
	cfg, err := a.applyBeforeModelCallHook(genCtx, cfg, 0)
	if err != nil {
		return nil, err
	}
	opts := a.buildGenerateOptions(cfg, sdkTools, approvalTools, prepareStep)
	modelStepIndex := 0
	opts = append(opts,
		sdk.WithOnStep(func(step *sdk.StepResult) *sdk.GenerateParams {
			a.runAfterModelCallHook(genCtx, cfg, step, modelStepIndex)
			modelStepIndex++
			if cfg.LoopDetection.Enabled {
				if toolLoopAbortCallIDs.Any() {
					loopAbort.Set(ErrToolLoopDetected)
					cancel(ErrToolLoopDetected)
					return nil
				}
				if textLoopGuard != nil && isNonEmptyString(step.Text) {
					result := textLoopGuard.Inspect(step.Text)
					if result.Abort {
						loopAbort.Set(ErrTextLoopDetected)
						cancel(ErrTextLoopDetected)
						return nil
					}
				}
			}
			return nil
		}),
	)
	if cfg.OnStepCommitted != nil {
		opts = append(opts, sdk.WithOnStepCommitted(func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
			return cfg.OnStepCommitted(ctx, stepIndex, committedStepMessages.decorate(stepIndex, step, toolExecutionMetadata))
		}))
	}

	genResult, err := a.client.GenerateTextResult(genCtx, opts...)
	if err != nil {
		if loopErr := detectGenerateLoopAbort(genCtx, err); loopErr != nil {
			return nil, loopErr
		}
		return nil, fmt.Errorf("generate: %w", err)
	}
	if loopErr := loopAbort.Err(); loopErr != nil {
		return nil, loopErr
	}

	// Drain collected tool-emitted side effects into the result.
	collectedEvents := collected.CloseAndSnapshot()
	var attachments []FileAttachment
	var reactions []ReactionItem
	var speeches []SpeechItem
	for _, evt := range collectedEvents {
		switch evt.Type {
		case tools.StreamEventAttachment:
			for _, a := range evt.Attachments {
				attachments = append(attachments, fileAttachmentFromToolAttachment(a))
			}
		case tools.StreamEventReaction:
			for _, r := range evt.Reactions {
				reactions = append(reactions, ReactionItem{Emoji: r.Emoji})
			}
		case tools.StreamEventSpeech:
			for _, s := range evt.Speeches {
				speeches = append(speeches, SpeechItem{Text: s.Text})
			}
		}
	}

	finalMessages := genResult.Messages
	if readMediaState != nil {
		finalMessages = readMediaState.mergeMessages(genResult.Steps, finalMessages)
	}
	finalMessages = toolExecutionMetadata.annotate(finalMessages)
	return &GenerateResult{
		Messages:    finalMessages,
		Text:        genResult.Text,
		Attachments: attachments,
		Reactions:   reactions,
		Speeches:    speeches,
		Usage:       &genResult.Usage,
	}, nil
}

func (a *Agent) buildGenerateOptions(cfg RunConfig, tools []sdk.Tool, approvalTools []sdk.Tool, prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams) []sdk.GenerateOption {
	system, messages, tools := models.ApplyPromptCache(
		cfg.Model, cfg.PromptCacheTTL, cfg.System, cfg.Messages, tools,
	)
	finalHash, _ := contextfrag.ProviderPayloadHashAndBytes(system, messages, tools)
	cfg.ContextMutations.SetFinalInputHash(finalHash)
	if cfg.ForkContext != nil {
		_ = cfg.ForkContext.Store(messages)
	}
	if cfg.BackgroundManager != nil {
		basePrepare := prepareStep
		baseSystem := captureBackgroundSystem(system, messages)
		logger := slog.Default()
		if a != nil && a.logger != nil {
			logger = a.logger
		}
		prepareStep = func(p *sdk.GenerateParams) *sdk.GenerateParams {
			if basePrepare != nil {
				if override := basePrepare(p); override != nil {
					p = override
				}
			}
			return injectBackgroundTaskSummary(p, cfg.BackgroundManager, baseSystem, cfg.Identity.BotID, cfg.Identity.SessionID, logger)
		}
	}
	opts := []sdk.GenerateOption{
		sdk.WithModel(cfg.Model),
		sdk.WithMessages(messages),
		sdk.WithSystem(system),
		sdk.WithMaxSteps(-1),
	}
	if len(tools) > 0 && cfg.SupportsToolCall {
		opts = append(opts, sdk.WithTools(tools))
	}
	approvalHandler := cfg.ToolApprovalHandler
	if a != nil && a.hookService != nil {
		approvalHandler = a.wrapApprovalHandlerWithHooks(cfg, approvalTools, approvalHandler)
	}
	if approvalHandler != nil {
		opts = append(opts, sdk.WithApprovalHandler(approvalHandler))
	}

	prepareStep = wrapPrepareStepWithForkSnapshot(prepareStep, cfg.ForkContext)
	prepareStep = wrapPrepareStepWithFinalInputHash(prepareStep, cfg.ContextMutations)
	if prepareStep != nil {
		opts = append(opts, sdk.WithPrepareStep(prepareStep))
	}

	opts = append(opts, models.BuildReasoningOptions(models.SDKModelConfig{
		ClientType:            models.ResolveClientType(cfg.Model),
		ChatCompletionsCompat: cfg.ChatCompletionsCompat,
		ReasoningConfig:       cfg.ReasoningConfig,
	})...)
	return opts
}

// assembleTools collects tools from all registered ToolProviders, along with
// the group-level usage guidance contributed by providers that also implement
// tools.ToolUsage. Usage guidance is gathered only from providers that actually
// returned tools for this session, so it stays in lockstep with registration
// (see tools.ToolUsage). emitter is injected into the session context so that
// tools targeting the current conversation can push side-effect events
// (attachments, reactions, speech) directly into the agent stream.
func (a *Agent) assembleTools(
	ctx context.Context,
	cfg RunConfig,
	emitter tools.StreamEmitter,
	liveStream bool,
) ([]sdk.Tool, string, []contextfrag.ContextFrag, []contextfrag.ToolDefAccounting, error) {
	if len(a.toolProviders) == 0 {
		return nil, "", nil, nil, nil
	}
	skillsMap := make(map[string]tools.SkillDetail, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillsMap[s.Name] = tools.SkillDetail{
			Description: s.Description,
			Content:     s.Content,
			Path:        s.Path,
		}
	}
	session := tools.SessionContext{
		BotID:                     cfg.Identity.BotID,
		ChatID:                    cfg.Identity.ChatID,
		SessionID:                 cfg.Identity.SessionID,
		SessionType:               cfg.SessionType,
		UserID:                    cfg.Identity.UserID,
		ChannelIdentityID:         cfg.Identity.ChannelIdentityID,
		SessionToken:              cfg.Identity.SessionToken,
		WorkspaceTargetID:         cfg.Identity.WorkspaceTargetID,
		WorkspaceTargetKind:       cfg.Identity.WorkspaceTargetKind,
		WorkspaceTargetName:       cfg.Identity.WorkspaceTargetName,
		WorkdirPath:               cfg.Identity.WorkdirPath,
		CurrentPlatform:           cfg.Identity.CurrentPlatform,
		ReplyTarget:               cfg.Identity.ReplyTarget,
		ConversationType:          cfg.Identity.ConversationType,
		CanRequestUserInput:       cfg.CanRequestUserInput,
		SupportsImageInput:        cfg.SupportsImageInput,
		SupportsFileInput:         cfg.SupportsFileInput,
		IsSubagent:                cfg.Identity.IsSubagent,
		CurrentModelUUID:          cfg.CurrentModelUUID,
		CurrentModelID:            cfg.CurrentModelID,
		CurrentModelProvider:      cfg.CurrentModelProvider,
		ReasoningStoredEffort:     cfg.ReasoningStoredEffort,
		ReasoningRequestedEffort:  cfg.ReasoningRequestedEffort,
		ForkContext:               cfg.ForkContext,
		Skills:                    skillsMap,
		TimezoneLocation:          cfg.Identity.TimezoneLocation,
		Emitter:                   emitter,
		LiveStream:                liveStream,
		ContextBudgetMaxTokens:    cfg.ContextBudgetMaxTokens,
		ContextToolExchangePolicy: cfg.ContextToolExchangePolicy,
	}

	var allTools []sdk.Tool
	var toolDefs []contextfrag.ToolDefAccounting
	type usageRegistration struct {
		provider   tools.ToolUsage
		capability string
	}
	var usageRegistrations []usageRegistration
	seenToolNames := make(map[string]struct{})
	for _, provider := range a.toolProviders {
		providerTools, err := provider.Tools(ctx, session)
		if err != nil {
			a.logger.Warn("tool provider failed", slog.Any("error", err))
			continue
		}
		if session.IsSubagent {
			providerTools = tools.FilterSubagentTools(providerTools)
		}
		uniqueTools := make([]sdk.Tool, 0, len(providerTools))
		for _, tool := range providerTools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			if _, exists := seenToolNames[name]; exists {
				a.logger.Warn("duplicate tool name skipped", slog.String("tool", name))
				continue
			}
			seenToolNames[name] = struct{}{}
			tool.Name = name
			uniqueTools = append(uniqueTools, tool)
		}
		providerTools = uniqueTools
		if len(providerTools) == 0 {
			continue
		}
		label := "native"
		if labeler, ok := provider.(tools.ProviderLabeler); ok {
			if providerLabel := strings.TrimSpace(labeler.ProviderLabel()); providerLabel != "" {
				label = providerLabel
			}
		}
		for _, tool := range providerTools {
			toolDefs = append(toolDefs, contextfrag.ToolDefAccountingFor(label, tool))
		}
		allTools = append(allTools, providerTools...)
		// Collect group-level usage guidance only from providers that actually
		// contributed tools this session, so guidance and registration share
		// one gating decision and cannot drift apart.
		if usageProvider, ok := provider.(tools.ToolUsage); ok {
			usageRegistrations = append(usageRegistrations, usageRegistration{
				provider:   usageProvider,
				capability: firstToolName(providerTools),
			})
		}
	}
	if cfg.ToolApprovalHandler != nil || a.hookService != nil {
		allTools = markApprovalTools(allTools)
	}
	availableTools := tools.NewAvailableTools(allTools)
	var usageSections []toolUsageSection
	for _, registration := range usageRegistrations {
		if text := strings.TrimSpace(registration.provider.Usage(ctx, session, availableTools)); text != "" {
			usageSections = append(usageSections, toolUsageSection{
				capability: registration.capability,
				text:       text,
			})
		}
	}
	usage := ""
	if len(usageSections) > 0 {
		texts := make([]string, 0, len(usageSections))
		for _, section := range usageSections {
			texts = append(texts, section.text)
		}
		usage = "## Tool usage\n\n" + strings.Join(texts, "\n\n")
	}
	return allTools, usage, structuredToolUsage(usageSections, cfg.ContextScope), toolDefs, nil
}

func appendToolUsageToSystem(system, toolUsage string) string {
	system = strings.TrimSpace(system)
	toolUsage = strings.TrimSpace(toolUsage)
	if toolUsage == "" {
		return system
	}
	if system == "" {
		return toolUsage
	}
	const workspaceAnchor = "\n## Workspace instruction files"
	if idx := strings.Index(system, workspaceAnchor); idx >= 0 {
		return strings.TrimSpace(system[:idx]) + "\n\n" + toolUsage + "\n" + system[idx:]
	}
	return strings.TrimSpace(system + "\n\n" + toolUsage)
}

func markApprovalTools(sdkTools []sdk.Tool) []sdk.Tool {
	for i := range sdkTools {
		switch sdkTools[i].Name {
		case tools.ToolRead().String(), tools.ToolList().String(), tools.ToolWrite().String(), tools.ToolEdit().String(), tools.ToolApplyPatch().String(), tools.ToolExec().String():
			sdkTools[i].RequireApproval = true
		}
	}
	return sdkTools
}

func approvalShortID(metadata map[string]any) int {
	if metadata == nil {
		return 0
	}
	switch v := metadata["short_id"].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func annotateDeferredApproval(messages []sdk.Message, approval sdk.ToolApprovalResult) []sdk.Message {
	if approval.ApprovalID == "" {
		return messages
	}
	toolCallID, _ := approval.Metadata["tool_call_id"].(string)
	if strings.TrimSpace(toolCallID) == "" {
		return messages
	}
	annotated := make([]sdk.Message, len(messages))
	copy(annotated, messages)
	for msgIdx := range annotated {
		if annotated[msgIdx].Role != sdk.MessageRoleAssistant {
			continue
		}
		for partIdx := range annotated[msgIdx].Content {
			call, ok := annotated[msgIdx].Content[partIdx].(sdk.ToolCallPart)
			if !ok || strings.TrimSpace(call.ToolCallID) != strings.TrimSpace(toolCallID) {
				continue
			}
			if call.ProviderMetadata == nil {
				call.ProviderMetadata = map[string]any{}
			}
			if isUserInputMetadata(approval.Metadata) {
				call.ProviderMetadata["user_input"] = map[string]any{
					"user_input_id": approval.ApprovalID,
					"short_id":      approvalShortID(approval.Metadata),
					"status":        "pending",
					"ui_payload":    approval.Metadata["ui_payload"],
				}
			} else {
				call.ProviderMetadata["approval"] = map[string]any{
					"approval_id": approval.ApprovalID,
					"short_id":    approvalShortID(approval.Metadata),
					"status":      "pending",
					"can_approve": true,
					"operation":   approval.Metadata["operation"],
				}
			}
			annotated[msgIdx].Content[partIdx] = call
			return annotated
		}
	}
	return annotated
}

func isUserInputMetadata(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	kind, _ := metadata["kind"].(string)
	return strings.TrimSpace(kind) == userinput.DeferredKind
}

func isAskUserArgumentParseError(message string) bool {
	return strings.Contains(message, `unmarshal tool call arguments for "`+tools.ToolAskUser().String()+`"`)
}

// toolStreamEventToAgentEvent converts a tool-layer ToolStreamEvent into an
// agent-layer StreamEvent suitable for the output channel.
func toolStreamEventToAgentEvent(evt tools.ToolStreamEvent) StreamEvent {
	switch evt.Type {
	case tools.StreamEventAttachment:
		atts := make([]FileAttachment, 0, len(evt.Attachments))
		for _, a := range evt.Attachments {
			atts = append(atts, fileAttachmentFromToolAttachment(a))
		}
		return StreamEvent{Type: EventAttachment, ToolCallID: evt.ToolCallID, Attachments: atts}
	case tools.StreamEventReaction:
		rs := make([]ReactionItem, 0, len(evt.Reactions))
		for _, r := range evt.Reactions {
			rs = append(rs, ReactionItem{Emoji: r.Emoji})
		}
		return StreamEvent{Type: EventReaction, Reactions: rs}
	case tools.StreamEventSpeech:
		ss := make([]SpeechItem, 0, len(evt.Speeches))
		for _, s := range evt.Speeches {
			ss = append(ss, SpeechItem{Text: s.Text})
		}
		return StreamEvent{Type: EventSpeech, Speeches: ss}
	case tools.StreamEventSpawnHeartbeat:
		return StreamEvent{Type: EventProgress, ProgressStatus: "spawn_running"}
	default:
		return StreamEvent{}
	}
}

// injectBackgroundTaskSummary refreshes the background task summary in the
// system prompt at step boundaries.
func injectBackgroundTaskSummary(
	p *sdk.GenerateParams,
	mgr *background.Manager,
	baseSystem backgroundSystem,
	botID, sessionID string,
	logger *slog.Logger,
) *sdk.GenerateParams {
	// Inject running tasks summary into system prompt so the model
	// knows about ongoing background work even after compaction.
	// Always start from baseSystem to avoid accumulating summaries across steps.
	injectBackgroundSummary(p, baseSystem, mgr.RunningTasksSummary(botID, sessionID))
	if logger != nil {
		logger.Debug("refreshed background task summary", slog.String("bot_id", botID), slog.String("session_id", sessionID))
	}
	return p
}

type backgroundSystem struct {
	system             string
	promotedSystemText string
	hasPromotedSystem  bool
}

func captureBackgroundSystem(system string, messages []sdk.Message) backgroundSystem {
	base := backgroundSystem{system: system}
	if len(messages) == 0 || messages[0].Role != sdk.MessageRoleSystem || len(messages[0].Content) == 0 {
		return base
	}
	first, ok := messages[0].Content[0].(sdk.TextPart)
	if !ok {
		return base
	}
	base.promotedSystemText = first.Text
	base.hasPromotedSystem = true
	return base
}

func injectBackgroundSummary(p *sdk.GenerateParams, baseSystem backgroundSystem, summary string) {
	summary = strings.TrimSpace(summary)
	if strings.TrimSpace(baseSystem.system) != "" {
		p.System = baseSystem.system
		if summary != "" {
			p.System += "\n\n" + summary
		}
		return
	}

	if baseSystem.hasPromotedSystem {
		text := strings.TrimSpace(baseSystem.promotedSystemText)
		if len(p.Messages) == 0 || p.Messages[0].Role != sdk.MessageRoleSystem || len(p.Messages[0].Content) == 0 {
			p.System = text
			if summary != "" {
				p.System = strings.TrimSpace(p.System + "\n\n" + summary)
			}
			return
		}
		first, ok := p.Messages[0].Content[0].(sdk.TextPart)
		if !ok {
			p.System = text
			if summary != "" {
				p.System = strings.TrimSpace(p.System + "\n\n" + summary)
			}
			return
		}
		first.Text = text
		p.Messages[0].Content[0] = first
		p.Messages = removeGeneratedBackgroundSystemMessages(p.Messages)
		if summary != "" {
			next := make([]sdk.Message, 0, len(p.Messages)+1)
			next = append(next, p.Messages[0])
			next = append(next, backgroundSummarySystemMessage(summary))
			next = append(next, p.Messages[1:]...)
			p.Messages = next
		}
		p.System = ""
		return
	}

	if summary != "" {
		p.System = summary
		return
	}
	p.System = ""
}

func backgroundSummarySystemMessage(summary string) sdk.Message {
	return sdk.SystemMessage(summary)
}

func removeGeneratedBackgroundSystemMessages(messages []sdk.Message) []sdk.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]sdk.Message, 0, len(messages))
	for _, msg := range messages {
		if !isBackgroundSummarySystemMessage(msg) {
			out = append(out, msg)
		}
	}
	return out
}

func isBackgroundSummarySystemMessage(msg sdk.Message) bool {
	if msg.Role != sdk.MessageRoleSystem || len(msg.Content) != 1 {
		return false
	}
	part, ok := msg.Content[0].(sdk.TextPart)
	return ok &&
		part.CacheControl == nil &&
		part.ProviderMetadata == nil &&
		strings.HasPrefix(strings.TrimSpace(part.Text), "Currently running background tasks:")
}

func wrapToolsWithLoopGuard(tools []sdk.Tool, guard *ToolLoopGuard, abortCallIDs *toolAbortRegistry) []sdk.Tool {
	wrapped := make([]sdk.Tool, len(tools))
	for i, tool := range tools {
		originalExecute := tool.Execute
		toolName := tool.Name
		wrapped[i] = tool
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			warn, abort := guard.Guard(toolName, input)
			if abort {
				abortCallIDs.Add(ctx.ToolCallID)
				return map[string]any{
					"isError": true,
					"content": []map[string]any{{
						"type": "text",
						"text": ToolLoopDetectedAbortMessage,
					}},
				}, ErrToolLoopDetected
			}
			if warn {
				return map[string]any{
					ToolLoopWarningKey: true,
					"content": []map[string]any{{
						"type": "text",
						"text": ToolLoopWarningText,
					}},
				}, nil
			}
			return originalExecute(ctx, input)
		}
	}
	return wrapped
}

func wrapPrepareStepWithForkSnapshot(
	prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams,
	forkContext *tools.MessageSnapshot,
) func(*sdk.GenerateParams) *sdk.GenerateParams {
	if forkContext == nil {
		return prepareStep
	}
	return func(p *sdk.GenerateParams) *sdk.GenerateParams {
		if prepareStep != nil {
			if override := prepareStep(p); override != nil {
				p = override
			}
		}
		_ = forkContext.Store(p.Messages)
		return p
	}
}

func wrapPrepareStepWithFinalInputHash(
	prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams,
	ledger *contextfrag.MutationLedger,
) func(*sdk.GenerateParams) *sdk.GenerateParams {
	if ledger == nil {
		return prepareStep
	}
	return func(p *sdk.GenerateParams) *sdk.GenerateParams {
		var override *sdk.GenerateParams
		if prepareStep != nil {
			override = prepareStep(p)
			if override != nil {
				p = override
			}
		}
		if p != nil {
			hash, _ := contextfrag.ProviderPayloadHashAndBytes(p.System, p.Messages, p.Tools)
			ledger.SetFinalInputHash(hash)
		}
		return override
	}
}

// runMidStreamRetry attempts to continue the agent stream after a retryable
// mid-stream error. It re-invokes StreamText with the accumulated messages
// and drains the new stream into the same output channel.
//
// sendCtx is used for sendEvent so consumer disconnect (parent ctx) still
// controls channel back-pressure; streamCtx is passed to the SDK for the same
// cancellation semantics as the main stream (including loop-detect cancel).
func (a *Agent) runMidStreamRetry(
	sendCtx context.Context,
	streamCtx context.Context,
	cancel context.CancelCauseFunc,
	toolLoopAbortCallIDs *toolAbortRegistry,
	ch chan<- StreamEvent,
	cfg RunConfig,
	sdkTools []sdk.Tool,
	approvalTools []sdk.Tool,
	prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams,
	prevResult *sdk.StreamResult,
	committedStepMessages *stepMessageCapture,
	onStepCommitted func(context.Context, int, *sdk.StepResult) error,
	interruptedStep *interruptedStepCapture,
	stepNumber int,
	errMsg string,
	allText *strings.Builder,
	textLoopProbeBuffer *TextLoopProbeBuffer,
) (*sdk.StreamResult, bool) {
	// Drain the previous stream before reading prevResult.Messages.
	// This avoids racing with the SDK's final StreamResult write.
	if prevResult.Stream != nil {
		for range prevResult.Stream {
		}
	}
	stepOffset := len(prevResult.Steps)
	// The failed attempt's partial output is regenerated from the last
	// committed boundary, so it must not survive as a checkpoint. Retried
	// steps are numbered from the offset the commit barrier already uses.
	interruptedStep.rebase(stepOffset)

	retryCfg := DefaultRetryConfig()
	for attempt := 0; attempt < retryCfg.MaxAttempts; attempt++ {
		a.logger.Warn("mid-stream error, retrying",
			slog.Int("step", stepNumber),
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", retryCfg.MaxAttempts),
			slog.String("error", errMsg),
		)
		if !sendEvent(sendCtx, ch, StreamEvent{
			Type:       EventRetry,
			Attempt:    attempt + 1,
			MaxAttempt: retryCfg.MaxAttempts,
			RetryError: errMsg,
		}) {
			return prevResult, true
		}

		delay := retryDelay(attempt, retryCfg)
		if delay > 0 {
			if err := sleepWithContext(streamCtx, delay); err != nil {
				return prevResult, true // aborted
			}
		}

		// Re-invoke StreamText with accumulated messages.
		// Use buildGenerateOptions so retry benefits from mid-task pruning,
		// media resolution, and other prepare-step logic — same as initial stream.
		retryCfgCopy := cfg
		retryCfgCopy.Messages = append(append([]sdk.Message(nil), prevResult.Messages...), committedStepMessages.messages(stepOffset)...)
		retryCfgCopy = retryCfgCopy.RefreshContextFrag()
		retryOpts := a.buildGenerateOptions(retryCfgCopy, sdkTools, approvalTools, prepareStep)
		if onStepCommitted != nil {
			retryOpts = append(retryOpts, sdk.WithOnStepCommitted(func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
				return onStepCommitted(ctx, stepOffset+stepIndex, step)
			}))
		}

		retryResult, retryErr := a.client.StreamText(streamCtx, retryOpts...)
		if retryErr != nil {
			a.logger.Warn("mid-stream retry failed to start",
				slog.Int("attempt", attempt+1),
				slog.String("error", retryErr.Error()),
			)
			// Update errMsg so the next retry event shows the latest error.
			errMsg = retryErr.Error()
			continue
		}

		// Drain the retry stream into the main event loop
		aborted := false
		for retryPart := range retryResult.Stream {
			if streamCtx.Err() != nil {
				aborted = true
				break
			}
			interruptedStep.observe(retryPart)
			switch rp := retryPart.(type) {
			case *sdk.TextStartPart:
				if !sendEvent(sendCtx, ch, StreamEvent{Type: EventTextStart}) {
					aborted = true
				}
			case *sdk.TextDeltaPart:
				if rp.Text != "" {
					if textLoopProbeBuffer != nil {
						textLoopProbeBuffer.Push(rp.Text)
					}
					if !sendEvent(sendCtx, ch, StreamEvent{Type: EventTextDelta, Delta: rp.Text}) {
						aborted = true
					}
					allText.WriteString(rp.Text)
				}
			case *sdk.TextEndPart:
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				stepNumber++
				if !sendEvent(sendCtx, ch, StreamEvent{Type: EventTextEnd}) {
					aborted = true
				}
			case *sdk.ToolInputStartPart:
				// See ToolInputStartPart note above: emit a lightweight
				// tool_call_input_start so the Web UI shows the tool block while
				// arguments stream; StreamToolCallPart backfills the Input.
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				if !sendEvent(sendCtx, ch, StreamEvent{
					Type:       EventToolCallInputStart,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ID,
				}) {
					aborted = true
				}
			case *sdk.StreamToolCallPart:
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				if !sendEvent(sendCtx, ch, StreamEvent{
					Type:       EventToolCallStart,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Input:      rp.Input,
				}) {
					aborted = true
				}
			case *sdk.StreamToolResultPart:
				shouldAbort := toolLoopAbortCallIDs.Take(rp.ToolCallID)
				stepNumber++
				if !sendEvent(sendCtx, ch, StreamEvent{
					Type:       EventToolCallEnd,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Input:      rp.Input,
					Result:     rp.Output,
				}) || !sendEvent(sendCtx, ch, StreamEvent{
					Type:           EventProgress,
					StepNumber:     stepNumber,
					ToolName:       rp.ToolName,
					ProgressStatus: "tool_result",
				}) {
					aborted = true
				}
				if shouldAbort {
					a.logger.Warn("tool loop abort triggered", slog.String("tool_call_id", rp.ToolCallID))
					cancel(ErrToolLoopDetected)
					aborted = true
				}
			case *sdk.StreamToolErrorPart:
				tookLoopAbort := toolLoopAbortCallIDs.Take(rp.ToolCallID)
				shouldAbort := errors.Is(rp.Error, ErrToolLoopDetected) || tookLoopAbort
				if !sendEvent(sendCtx, ch, StreamEvent{
					Type:       EventToolCallEnd,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Error:      rp.Error.Error(),
				}) {
					aborted = true
				}
				if shouldAbort {
					a.logger.Warn("tool loop abort triggered", slog.String("tool_call_id", rp.ToolCallID))
					cancel(ErrToolLoopDetected)
					aborted = true
				}
			case *sdk.ErrorPart:
				errMsg := rp.Error.Error()
				if isAskUserArgumentParseError(errMsg) {
					continue
				}
				sendEvent(sendCtx, ch, StreamEvent{Type: EventError, Error: errMsg})
				aborted = true
			case *sdk.AbortPart:
				aborted = true
			case *sdk.FinishPart:
				// handled after loop
			}
			if aborted {
				break
			}
		}
		if aborted {
			for retryPart := range retryResult.Stream {
				interruptedStep.observe(retryPart)
			}
		}
		// Merge prev messages into retryResult so the caller sees the full
		// accumulated history (initial run + retry continuation). The SDK's
		// StreamResult.Messages only contains messages produced within that
		// StreamText call, so without this merge the original steps before
		// the mid-stream error would be lost when the retry result becomes
		// the new streamResult.
		if len(prevResult.Messages) > 0 {
			merged := make([]sdk.Message, 0, len(prevResult.Messages)+len(retryResult.Messages))
			merged = append(merged, prevResult.Messages...)
			merged = append(merged, retryResult.Messages...)
			retryResult.Messages = merged
		}
		if len(prevResult.Steps) > 0 {
			retryResult.Steps = append(append([]sdk.StepResult(nil), prevResult.Steps...), retryResult.Steps...)
		}
		return retryResult, aborted || detectGenerateLoopAbort(streamCtx, streamCtx.Err()) != nil
	}
	// All retry attempts failed to even start a new stream — return the
	// previous (already drained) result so its accumulated messages are
	// preserved as the final partial state. Publish the giving-up error: every
	// EventRetry retracts the failure it retried, so without this last event a
	// consumer would see the run end with nothing to explain why it stopped.
	sendEvent(sendCtx, ch, StreamEvent{
		Type:  EventError,
		Error: fmt.Sprintf("mid-stream retry: all %d attempts failed (last: %s)", retryCfg.MaxAttempts, errMsg),
	})
	return prevResult, true
}

// sleepWithContext sleeps for the given duration or returns context error.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func detectGenerateLoopAbort(ctx context.Context, err error) error {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return nil
	}

	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ErrToolLoopDetected):
		return ErrToolLoopDetected
	case errors.Is(cause, ErrTextLoopDetected):
		return ErrTextLoopDetected
	default:
		return nil
	}
}

type loopAbortState struct {
	mu  sync.Mutex
	err error
}

func newLoopAbortState() *loopAbortState {
	return &loopAbortState{}
}

func (s *loopAbortState) Set(err error) {
	if s == nil || err == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *loopAbortState) Err() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

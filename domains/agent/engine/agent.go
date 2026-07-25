package engine

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

	agentdomain "github.com/memohai/memoh/domains/agent"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine/background"
	"github.com/memohai/memoh/domains/agent/extension/hooks"
	"github.com/memohai/memoh/domains/agent/tool"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

// Agent is the core agent that handles LLM interactions.
type Agent struct {
	client         *sdk.Client
	toolProviders  []tool.ToolProvider
	bridgeProvider bridge.Provider
	hookService    *hooks.Service
	logger         *slog.Logger
	limits         Limits
}

// New creates a new Agent with the given dependencies.
func New(deps Deps) *Agent {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{
		client:         sdk.NewClient(),
		bridgeProvider: deps.BridgeProvider,
		hookService:    deps.HookService,
		logger:         logger.With(slog.String("service", "agent/runtime/native")),
		limits:         deps.Limits.Normalize(),
	}
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
func (a *Agent) SetToolProviders(providers []tool.ToolProvider) {
	a.toolProviders = providers
}

// Stream runs the agent in streaming mode, emitting events to the returned channel.
func (a *Agent) Stream(ctx context.Context, cfg RunConfig) <-chan agentdomain.StreamEvent {
	ch := make(chan agentdomain.StreamEvent)
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
	sdkTools, _, err := a.assembleTools(ctx, cfg, nil, false)
	if err != nil {
		return sdk.ToolResultPart{}, fmt.Errorf("assemble tools: %w", err)
	}
	sdkTools, _ = decorateReadMediaTools(cfg.Model, sdkTools)
	sdkTools = tool.WrapToolOutputLimits(sdkTools, a.Limits().ToolOutputLimit())
	for i := range sdkTools {
		toolProvider := sdkTools[i]
		if toolProvider.Name != call.ToolName {
			continue
		}
		if toolProvider.Execute == nil {
			return sdk.ToolResultPart{}, fmt.Errorf("tool %q has no execute handler", call.ToolName)
		}
		execCtx := &sdk.ToolExecContext{
			Context:    ctx,
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
		}
		output, err := toolProvider.Execute(execCtx, call.Input)
		if err != nil {
			limitedErr := tool.LimitToolError(err, "tool result ("+call.ToolName+")", a.Limits().ToolOutputLimit())
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
func sendEvent(ctx context.Context, ch chan<- agentdomain.StreamEvent, evt agentdomain.StreamEvent) bool {
	select {
	case ch <- evt:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *Agent) runStream(ctx context.Context, cfg RunConfig, ch chan<- agentdomain.StreamEvent) {
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
	streamEmitter := tool.StreamEmitter(func(evt tool.ToolStreamEvent) {
		sendEvent(ctx, ch, toolStreamEventToAgentEvent(evt))
	})
	if cfg.ForkContext == nil {
		cfg.ForkContext = tool.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	}

	var sdkTools []sdk.Tool
	if cfg.SupportsToolCall {
		var toolUsage string
		var err error
		sdkTools, toolUsage, err = a.assembleTools(streamCtx, cfg, streamEmitter, cfg.LiveToolStream)
		if err != nil {
			turnError = fmt.Sprintf("assemble tools: %v", err)
			sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: turnError})
			return
		}
		if toolUsage != "" {
			// Must run before buildGenerateOptions so prompt caching and
			// background task summaries see the usage-augmented text.
			cfg.System = appendToolUsageToSystem(cfg.System, toolUsage)
			cfg.ContextToolUsage = toolUsage
		}
	}
	limit := a.Limits().ToolOutputLimit()
	sdkTools, readMediaState := decorateReadMediaTools(cfg.Model, sdkTools)
	cfg = cfg.RefreshContextFragWithDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, true)
	sdkTools = tool.WrapToolOutputLimits(sdkTools, limit)
	approvalTools := append([]sdk.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(ctx, cfg, sdkTools)
	sdkTools = tool.WrapToolOutputLimits(sdkTools, limit)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(func(call sdk.ToolCall, metadata map[string]any) {
		sendEvent(ctx, ch, agentdomain.StreamEvent{
			Type:       agentdomain.ToolCallMetadata,
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
				a.logger.WarnContext(ctx, "text loop detected, will abort")
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
						a.logger.InfoContext(ctx, "injected user message into agent stream",
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

	prepareStep = a.wrapPrepareStepWithModelHook(streamCtx, cfg, prepareStep)
	var err error
	cfg, err = a.applyBeforeModelCallHook(streamCtx, cfg, 0)
	if err != nil {
		turnError = err.Error()
		sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: turnError})
		return
	}
	cfg = cfg.RefreshContextFragWithDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, true)
	opts := a.buildGenerateOptions(cfg, sdkTools, approvalTools, prepareStep)
	modelStepIndex := 0
	opts = append(opts, sdk.WithOnStep(func(step *sdk.StepResult) *sdk.GenerateParams {
		a.runAfterModelCallHook(streamCtx, cfg, step, modelStepIndex)
		modelStepIndex++
		return nil
	}))

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
			sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: turnError})
			return
		}
		a.logger.WarnContext(ctx, "stream start failed, retrying",
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", retryCfg.MaxAttempts),
			slog.String("error", err.Error()),
		)
		if !sendEvent(ctx, ch, agentdomain.StreamEvent{
			Type:       agentdomain.Retry,
			Attempt:    attempt + 1,
			MaxAttempt: retryCfg.MaxAttempts,
			RetryError: err.Error(),
		}) {
			return
		}
		if attempt+1 >= retryCfg.MaxAttempts {
			turnError = fmt.Sprintf("stream start: all %d attempts failed (last: %v)", retryCfg.MaxAttempts, err)
			sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: turnError})
			return
		}
		delay := retryDelay(attempt, retryCfg)
		if delay > 0 {
			if err := sleepWithContext(streamCtx, delay); err != nil {
				turnError = fmt.Sprintf("stream start: context cancelled during retry: %v", err)
				sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: turnError})
				return
			}
		}
	}

	sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.AgentStart})

	var allText strings.Builder
	stepNumber := 0

	for part := range streamResult.Stream {
		if streamCtx.Err() != nil {
			aborted = true
			break
		}

		switch p := part.(type) {
		case *sdk.StartPart:
			_ = p // stream start already emitted

		case *sdk.TextStartPart:
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.TextStart}) {
				aborted = true
			}

		case *sdk.TextDeltaPart:
			if p.Text != "" {
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Push(p.Text)
				}
				if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.TextDelta, Delta: p.Text}) {
					aborted = true
				}
				allText.WriteString(p.Text)
			}

		case *sdk.TextEndPart:
			if textLoopProbeBuffer != nil {
				textLoopProbeBuffer.Flush()
			}
			stepNumber++
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.TextEnd}) ||
				!sendEvent(ctx, ch, agentdomain.StreamEvent{
					Type:           agentdomain.Progress,
					StepNumber:     stepNumber,
					ProgressStatus: "text",
				}) {
				aborted = true
			}

		case *sdk.ReasoningStartPart:
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.ReasoningStart}) {
				aborted = true
			}

		case *sdk.ReasoningDeltaPart:
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.ReasoningDelta, Delta: p.Text}) {
				aborted = true
			}

		case *sdk.ReasoningEndPart:
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.ReasoningEnd}) {
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
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:       agentdomain.ToolCallInputStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ID,
			}) {
				aborted = true
			}

		case *sdk.StreamToolCallPart:
			if textLoopProbeBuffer != nil {
				textLoopProbeBuffer.Flush()
			}
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:       agentdomain.ToolCallStart,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Input:      p.Input,
			}) {
				aborted = true
			}

		case *sdk.ToolProgressPart:
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:       agentdomain.ToolCallProgress,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Progress:   p.Content,
			}) {
				aborted = true
			}

		case *sdk.ToolApprovalRequestPart:
			eventType := agentdomain.ToolApprovalRequest
			var userInputID string
			var approvalID string
			if isUserInputMetadata(p.Metadata) {
				eventType = agentdomain.UserInputRequest
				userInputID = p.ApprovalID
			} else {
				approvalID = p.ApprovalID
			}
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
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
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:       agentdomain.ToolCallEnd,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Input:      p.Input,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Result:     p.Output,
			}) || !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:           agentdomain.Progress,
				StepNumber:     stepNumber,
				ToolName:       p.ToolName,
				ProgressStatus: "tool_result",
			}) {
				aborted = true
			}
			if shouldAbort {
				a.logger.WarnContext(ctx, "tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
				cancel(ErrToolLoopDetected)
				aborted = true
			}

		case *sdk.StreamToolErrorPart:
			// Take before errors.Is so registry IDs from the loop guard are always cleared.
			tookLoopAbort := toolLoopAbortCallIDs.Take(p.ToolCallID)
			shouldAbort := errors.Is(p.Error, ErrToolLoopDetected) || tookLoopAbort
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type:       agentdomain.ToolCallEnd,
				ToolName:   p.ToolName,
				ToolCallID: p.ToolCallID,
				Metadata:   toolExecutionMetadata.metadata(p.ToolCallID),
				Error:      p.Error.Error(),
			}) {
				aborted = true
			}
			if shouldAbort {
				a.logger.WarnContext(ctx, "tool loop abort triggered", slog.String("tool_call_id", p.ToolCallID))
				cancel(ErrToolLoopDetected)
				aborted = true
			}

		case *sdk.StreamFilePart:
			mediaType := p.File.MediaType
			if mediaType == "" {
				mediaType = "image/png"
			}
			if !sendEvent(ctx, ch, agentdomain.StreamEvent{
				Type: agentdomain.AttachmentDelta,
				Attachments: []agentdomain.FileAttachment{{
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
			sendEvent(ctx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: errMsg})

			// Mid-stream retry: if the error is retryable, attempt to continue
			// the agent run from the accumulated state. This also handles
			// errors at step 0 (e.g. timeout awaiting response headers) since
			// no work has been completed yet and retrying from the start is safe.
			if isRetryableStreamError(p.Error) {
				streamResult, aborted = a.runMidStreamRetry(
					ctx, streamCtx, cancel, toolLoopAbortCallIDs,
					ch, cfg, sdkTools, approvalTools, prepareStep, streamResult,
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

	if aborted {
		for range streamResult.Stream {
		}
	}

	if textLoopProbeBuffer != nil {
		textLoopProbeBuffer.Flush()
	}

	finalMessages := streamResult.Messages
	if readMediaState != nil {
		finalMessages = readMediaState.mergeMessages(streamResult.Steps, finalMessages)
	}
	if streamResult.DeferredToolApproval != nil {
		finalMessages = annotateDeferredApproval(finalMessages, *streamResult.DeferredToolApproval)
	}
	finalMessages = toolExecutionMetadata.annotate(finalMessages)
	var totalUsage sdk.Usage
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
	usageJSON, _ := json.Marshal(totalUsage)

	termEvent := agentdomain.StreamEvent{
		Messages: mustMarshal(finalMessages),
		Usage:    usageJSON,
	}
	if streamResult.DeferredToolApproval != nil {
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
		termEvent.Type = agentdomain.AgentAbort
	} else {
		termEvent.Type = agentdomain.AgentEnd
		// Warn if LLM produced no text and no tool calls — likely a context overflow.
		if allText.Len() == 0 && stepNumber == 0 {
			a.logger.WarnContext(ctx, "agent produced empty response (no text, no tool calls)",
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

func (a *Agent) runGenerate(ctx context.Context, cfg RunConfig) (result *GenerateResult, retErr error) {
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
	collectEmitter := tool.StreamEmitter(func(evt tool.ToolStreamEvent) {
		collected.Add(evt)
	})
	if cfg.ForkContext == nil {
		cfg.ForkContext = tool.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	}

	var sdkTools []sdk.Tool
	if cfg.SupportsToolCall {
		var toolUsage string
		var err error
		sdkTools, toolUsage, err = a.assembleTools(genCtx, cfg, collectEmitter, false)
		if err != nil {
			return nil, fmt.Errorf("assemble tools: %w", err)
		}
		if toolUsage != "" {
			// Must run before buildGenerateOptions so prompt caching and
			// background task summaries see the usage-augmented text.
			cfg.System = appendToolUsageToSystem(cfg.System, toolUsage)
			cfg.ContextToolUsage = toolUsage
		}
	}
	limit := a.Limits().ToolOutputLimit()
	sdkTools, readMediaState := decorateReadMediaTools(cfg.Model, sdkTools)
	cfg = cfg.RefreshContextFragWithDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, false)
	sdkTools = tool.WrapToolOutputLimits(sdkTools, limit)
	approvalTools := append([]sdk.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(ctx, cfg, sdkTools)
	sdkTools = tool.WrapToolOutputLimits(sdkTools, limit)
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

	prepareStep = a.wrapPrepareStepWithModelHook(genCtx, cfg, prepareStep)
	cfg, err := a.applyBeforeModelCallHook(genCtx, cfg, 0)
	if err != nil {
		return nil, err
	}
	cfg = cfg.RefreshContextFragWithDynamicMutators(readMediaState != nil, a != nil && a.hookService != nil, false)
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
	var attachments []agentdomain.FileAttachment
	var reactions []agentdomain.ReactionItem
	var speeches []agentdomain.SpeechItem
	for _, evt := range collectedEvents {
		switch evt.Type {
		case tool.StreamEventAttachment:
			for _, a := range evt.Attachments {
				attachments = append(attachments, fileAttachmentFromToolAttachment(a))
			}
		case tool.StreamEventReaction:
			for _, r := range evt.Reactions {
				reactions = append(reactions, agentdomain.ReactionItem{Emoji: r.Emoji})
			}
		case tool.StreamEventSpeech:
			for _, s := range evt.Speeches {
				speeches = append(speeches, agentdomain.SpeechItem{Text: s.Text})
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
	system, messages, tools := modelexecution.ApplyPromptCache(
		cfg.Model, cfg.PromptCacheTTL, cfg.System, cfg.Messages, tools,
	)
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

	// Wrap the existing prepareStep (if any) with mid-task context pruning.
	// When the message array grows large during multi-tool runs, this prunes
	// older tool results to keep the context window manageable.
	basePrepare := prepareStep
	keepSteps := cfg.MidTaskPruneKeepSteps
	if keepSteps <= 0 {
		keepSteps = MidTaskPruneKeepStepsDefault
	}
	threshold := cfg.MidTaskPruneThreshold
	if threshold <= 0 {
		threshold = MidTaskPruneThresholdDefault
	}
	midTaskPrune := func(p *sdk.GenerateParams) *sdk.GenerateParams {
		if basePrepare != nil {
			if override := basePrepare(p); override != nil {
				p = override
			}
		}
		p = pruneOldToolResults(p, keepSteps, threshold)
		if cfg.ForkContext != nil {
			_ = cfg.ForkContext.Store(p.Messages)
		}
		return p
	}
	opts = append(opts, sdk.WithPrepareStep(midTaskPrune))

	opts = append(opts, modelexecution.BuildReasoningOptions(modelexecution.SDKModelConfig{
		ClientType:            modelexecution.ResolveClientType(cfg.Model),
		ChatCompletionsCompat: cfg.ChatCompletionsCompat,
		ReasoningConfig: &modelexecution.ReasoningConfig{
			Active:    cfg.ReasoningActive,
			Disabled:  cfg.ReasoningDisabled,
			Adaptive:  cfg.ReasoningAdaptive,
			Effort:    cfg.ReasoningEffort,
			OffEffort: cfg.ReasoningOffEffort,
		},
	})...)
	return opts
}

// assembleTools collects tools from all registered ToolProviders, along with
// the group-level usage guidance contributed by providers that also implement
// tool.ToolUsage. Usage guidance is gathered only from providers that actually
// returned tools for this session, so it stays in lockstep with registration
// (see tool.ToolUsage). emitter is injected into the session context so that
// tools targeting the current conversation can push side-effect events
// (attachments, reactions, speech) directly into the agent stream.
func (a *Agent) assembleTools(ctx context.Context, cfg RunConfig, emitter tool.StreamEmitter, liveStream bool) ([]sdk.Tool, string, error) {
	if len(a.toolProviders) == 0 {
		return nil, "", nil
	}
	skillsMap := make(map[string]tool.SkillDetail, len(cfg.Skills))
	for _, s := range cfg.Skills {
		skillsMap[s.Name] = tool.SkillDetail{
			Description: s.Description,
			Content:     s.Content,
			Path:        s.Path,
		}
	}
	session := tool.SessionContext{
		BotID:                cfg.Identity.BotID,
		ChatID:               cfg.Identity.ChatID,
		SessionID:            cfg.Identity.SessionID,
		SessionType:          cfg.SessionType,
		UserID:               cfg.Identity.UserID,
		ChannelIdentityID:    cfg.Identity.ChannelIdentityID,
		SessionToken:         cfg.Identity.SessionToken,
		WorkspaceTargetID:    cfg.Identity.WorkspaceTargetID,
		WorkspaceTargetKind:  cfg.Identity.WorkspaceTargetKind,
		WorkspaceTargetName:  cfg.Identity.WorkspaceTargetName,
		CurrentPlatform:      cfg.Identity.CurrentPlatform,
		ReplyTarget:          cfg.Identity.ReplyTarget,
		ConversationType:     cfg.Identity.ConversationType,
		CanRequestUserInput:  cfg.CanRequestUserInput,
		SupportsImageInput:   cfg.SupportsImageInput,
		IsSubagent:           cfg.Identity.IsSubagent,
		CurrentModelUUID:     cfg.CurrentModelUUID,
		CurrentModelID:       cfg.CurrentModelID,
		CurrentModelProvider: cfg.CurrentModelProvider,
		ForkContext:          cfg.ForkContext,
		Skills:               skillsMap,
		TimezoneLocation:     cfg.Identity.TimezoneLocation,
		Emitter:              emitter,
		LiveStream:           liveStream,
	}

	var allTools []sdk.Tool
	type usageRegistration struct {
		provider tool.ToolUsage
	}
	var usageRegistrations []usageRegistration
	var usageSections []string
	for _, provider := range a.toolProviders {
		providerTools, err := provider.Tools(ctx, session)
		if err != nil {
			a.logger.WarnContext(ctx, "tool provider failed", slog.Any("error", err))
			continue
		}
		if session.IsSubagent {
			providerTools = tool.FilterSubagentTools(providerTools)
		}
		if len(providerTools) == 0 {
			continue
		}
		allTools = append(allTools, providerTools...)
		// Collect group-level usage guidance only from providers that actually
		// contributed tools this session, so guidance and registration share
		// one gating decision and cannot drift apart.
		if usageProvider, ok := provider.(tool.ToolUsage); ok {
			usageRegistrations = append(usageRegistrations, usageRegistration{provider: usageProvider})
		}
	}
	if cfg.ToolApprovalHandler != nil || a.hookService != nil {
		allTools = markApprovalTools(allTools)
	}
	availableTools := tool.NewAvailableTools(allTools)
	for _, registration := range usageRegistrations {
		if text := strings.TrimSpace(registration.provider.Usage(ctx, session, availableTools)); text != "" {
			usageSections = append(usageSections, text)
		}
	}
	usage := ""
	if len(usageSections) > 0 {
		usage = "## Tool usage\n\n" + strings.Join(usageSections, "\n\n")
	}
	return allTools, usage, nil
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
		case tool.ToolRead().String(), tool.ToolList().String(), tool.ToolWrite().String(), tool.ToolEdit().String(), tool.ToolApplyPatch().String(), tool.ToolExec().String():
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
	return strings.Contains(message, `unmarshal tool call arguments for "`+tool.ToolAskUser().String()+`"`)
}

// toolStreamEventToAgentEvent converts a tool-layer ToolStreamEvent into an
// agent-layer StreamEvent suitable for the output channel.
func toolStreamEventToAgentEvent(evt tool.ToolStreamEvent) agentdomain.StreamEvent {
	switch evt.Type {
	case tool.StreamEventAttachment:
		atts := make([]agentdomain.FileAttachment, 0, len(evt.Attachments))
		for _, a := range evt.Attachments {
			atts = append(atts, fileAttachmentFromToolAttachment(a))
		}
		return agentdomain.StreamEvent{Type: agentdomain.AttachmentDelta, ToolCallID: evt.ToolCallID, Attachments: atts}
	case tool.StreamEventReaction:
		rs := make([]agentdomain.ReactionItem, 0, len(evt.Reactions))
		for _, r := range evt.Reactions {
			rs = append(rs, agentdomain.ReactionItem{Emoji: r.Emoji})
		}
		return agentdomain.StreamEvent{Type: agentdomain.Reaction, Reactions: rs}
	case tool.StreamEventSpeech:
		ss := make([]agentdomain.SpeechItem, 0, len(evt.Speeches))
		for _, s := range evt.Speeches {
			ss = append(ss, agentdomain.SpeechItem{Text: s.Text})
		}
		return agentdomain.StreamEvent{Type: agentdomain.Speech, Speeches: ss}
	case tool.StreamEventSpawnHeartbeat:
		return agentdomain.StreamEvent{Type: agentdomain.Progress, ProgressStatus: "spawn_running"}
	default:
		return agentdomain.StreamEvent{}
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

const (
	// MidTaskPruneKeepStepsDefault is the number of recent tool-call steps to keep
	// intact when pruning older tool results during a multi-step agent run.
	MidTaskPruneKeepStepsDefault = 4
	// MidTaskPruneThresholdDefault is the minimum number of messages before pruning activates.
	MidTaskPruneThresholdDefault = 20
)

// pruneOldToolResults prunes older tool result messages in the SDK params to
// keep the context window manageable during long multi-tool agent runs. It
// keeps the most recent keepSteps tool-call cycles intact and replaces older
// tool results with size summaries.
func pruneOldToolResults(p *sdk.GenerateParams, keepSteps, threshold int) *sdk.GenerateParams {
	msgs := p.Messages
	if len(msgs) < threshold {
		return p
	}

	// Count complete tool-call cycles (tool-result pair) from the end to find the cutoff.
	toolResultCount := 0
	cutoffIdx := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.MessageRoleTool {
			// Check that the preceding assistant message contains the matching tool call
			// to ensure we count complete cycles, not orphaned results.
			hasMatchingCall := false
			for j := i - 1; j >= 0; j-- {
				if msgs[j].Role == sdk.MessageRoleAssistant {
					// If there's another tool result between this and the assistant msg,
					// it means this assistant message belongs to a different cycle.
					if j+1 < i && msgs[j+1].Role == sdk.MessageRoleTool {
						break
					}
					hasMatchingCall = true
					break
				}
				if msgs[j].Role == sdk.MessageRoleUser {
					break
				}
			}
			if hasMatchingCall {
				toolResultCount++
				if toolResultCount > keepSteps {
					cutoffIdx = i
					break
				}
			}
		}
	}
	if cutoffIdx >= len(msgs) {
		return p // not enough tool messages to prune
	}

	// Build a new slice so the original messages can be GC'd.
	pruned := make([]sdk.Message, 0, len(msgs))
	pruned = append(pruned, msgs[:cutoffIdx]...)
	for i := cutoffIdx; i < len(msgs); i++ {
		if msgs[i].Role != sdk.MessageRoleTool {
			pruned = append(pruned, msgs[i])
			continue
		}
		// Measure content size from ToolResultPart entries.
		contentSize := 0
		for _, part := range msgs[i].Content {
			if tr, ok := part.(sdk.ToolResultPart); ok {
				contentSize += len(fmt.Sprintf("%v", tr.Result))
			}
		}
		if contentSize > 512 { // only prune if content is large enough
			// Build replacement parts preserving ToolResultPart type so that
			// provider serializers that validate part types per role stay happy.
			replacementParts := make([]sdk.MessagePart, 0, len(msgs[i].Content))
			for _, part := range msgs[i].Content {
				if tr, ok := part.(sdk.ToolResultPart); ok {
					replacementParts = append(replacementParts, sdk.ToolResultPart{
						ToolCallID: tr.ToolCallID,
						ToolName:   tr.ToolName,
						Result:     fmt.Sprintf("[tool result pruned: %d bytes]", contentSize),
					})
				} else {
					replacementParts = append(replacementParts, part)
				}
			}
			pruned = append(pruned, sdk.Message{
				Role:    msgs[i].Role,
				Content: replacementParts,
			})
		} else {
			pruned = append(pruned, msgs[i])
		}
	}

	p.Messages = pruned
	return p
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
	ch chan<- agentdomain.StreamEvent,
	cfg RunConfig,
	sdkTools []sdk.Tool,
	approvalTools []sdk.Tool,
	prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams,
	prevResult *sdk.StreamResult,
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

	retryCfg := DefaultRetryConfig()
	for attempt := 0; attempt < retryCfg.MaxAttempts; attempt++ {
		a.logger.WarnContext(streamCtx, "mid-stream error, retrying",
			slog.Int("step", stepNumber),
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", retryCfg.MaxAttempts),
			slog.String("error", errMsg),
		)
		if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
			Type:       agentdomain.Retry,
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
		retryCfgCopy.Messages = prevResult.Messages
		retryCfgCopy = retryCfgCopy.RefreshContextFrag()
		retryOpts := a.buildGenerateOptions(retryCfgCopy, sdkTools, approvalTools, prepareStep)

		retryResult, retryErr := a.client.StreamText(streamCtx, retryOpts...)
		if retryErr != nil {
			a.logger.WarnContext(streamCtx, "mid-stream retry failed to start",
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
			switch rp := retryPart.(type) {
			case *sdk.TextStartPart:
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{Type: agentdomain.TextStart}) {
					aborted = true
				}
			case *sdk.TextDeltaPart:
				if rp.Text != "" {
					if textLoopProbeBuffer != nil {
						textLoopProbeBuffer.Push(rp.Text)
					}
					if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{Type: agentdomain.TextDelta, Delta: rp.Text}) {
						aborted = true
					}
					allText.WriteString(rp.Text)
				}
			case *sdk.TextEndPart:
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				stepNumber++
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{Type: agentdomain.TextEnd}) {
					aborted = true
				}
			case *sdk.ToolInputStartPart:
				// See ToolInputStartPart note above: emit a lightweight
				// tool_call_input_start so the Web UI shows the tool block while
				// arguments stream; StreamToolCallPart backfills the Input.
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
					Type:       agentdomain.ToolCallInputStart,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ID,
				}) {
					aborted = true
				}
			case *sdk.StreamToolCallPart:
				if textLoopProbeBuffer != nil {
					textLoopProbeBuffer.Flush()
				}
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
					Type:       agentdomain.ToolCallStart,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Input:      rp.Input,
				}) {
					aborted = true
				}
			case *sdk.StreamToolResultPart:
				shouldAbort := toolLoopAbortCallIDs.Take(rp.ToolCallID)
				stepNumber++
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
					Type:       agentdomain.ToolCallEnd,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Input:      rp.Input,
					Result:     rp.Output,
				}) || !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
					Type:           agentdomain.Progress,
					StepNumber:     stepNumber,
					ToolName:       rp.ToolName,
					ProgressStatus: "tool_result",
				}) {
					aborted = true
				}
				if shouldAbort {
					a.logger.WarnContext(streamCtx, "tool loop abort triggered", slog.String("tool_call_id", rp.ToolCallID))
					cancel(ErrToolLoopDetected)
					aborted = true
				}
			case *sdk.StreamToolErrorPart:
				tookLoopAbort := toolLoopAbortCallIDs.Take(rp.ToolCallID)
				shouldAbort := errors.Is(rp.Error, ErrToolLoopDetected) || tookLoopAbort
				if !sendEvent(sendCtx, ch, agentdomain.StreamEvent{
					Type:       agentdomain.ToolCallEnd,
					ToolName:   rp.ToolName,
					ToolCallID: rp.ToolCallID,
					Error:      rp.Error.Error(),
				}) {
					aborted = true
				}
				if shouldAbort {
					a.logger.WarnContext(streamCtx, "tool loop abort triggered", slog.String("tool_call_id", rp.ToolCallID))
					cancel(ErrToolLoopDetected)
					aborted = true
				}
			case *sdk.ErrorPart:
				errMsg := rp.Error.Error()
				if isAskUserArgumentParseError(errMsg) {
					continue
				}
				sendEvent(sendCtx, ch, agentdomain.StreamEvent{Type: agentdomain.Error, Error: errMsg})
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
			for range retryResult.Stream {
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
		return retryResult, aborted || detectGenerateLoopAbort(streamCtx, streamCtx.Err()) != nil
	}
	// All retry attempts failed to even start a new stream — return the
	// previous (already drained) result so its accumulated messages are
	// preserved as the final partial state.
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

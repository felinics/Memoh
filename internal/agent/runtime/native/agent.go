package native

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/workspace/bridge"
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
	loopReselectMode   LoopReselectMode
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
		loopReselectMode:   deps.LoopReselectMode.Normalize(),
	}
}

// LoopReselectMode returns the normalized rollout mode for in-loop context
// reselection. A nil Agent defaults to active.
func (a *Agent) LoopReselectMode() LoopReselectMode {
	if a == nil {
		return LoopReselectActive
	}
	return a.loopReselectMode.Normalize()
}

// captureProviderAttemptPrefix freezes the applier-rendered boundary before
// step-local hooks and other dynamic messages are appended.
func captureProviderAttemptPrefix(cfg RunConfig) RunConfig {
	cfg.initialProviderMessageCount = len(cfg.Messages)
	cfg.initialProviderPrefixSet = true
	if cfg.providerAttemptState == nil {
		cfg.providerAttemptState = &providerAttemptState{}
	}
	return cfg
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

func installContextStepFailureHandler(cfg *RunConfig, cancel context.CancelCauseFunc) {
	if cfg == nil {
		return
	}
	var once sync.Once
	cfg.contextStepFailure = func(err error) {
		if err == nil {
			return
		}
		once.Do(func() {
			reason := "budget_unsatisfied"
			if errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
				reason = "protected_context_overflow"
			}
			cfg.ContextMutations.Record(contextfrag.MutationContextBudgetFailure, reason)
			cancel(err)
		})
	}
}

func contextStepBudgetError(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, contextfrag.ErrProtectedContextOverflow):
		return contextfrag.ErrProtectedContextOverflow
	case errors.Is(cause, contextfrag.ErrBudgetUnsatisfied):
		return contextfrag.ErrBudgetUnsatisfied
	default:
		return nil
	}
}

func providerAttemptDispatchAllowed(ctx context.Context) bool {
	return contextStepBudgetError(ctx) == nil && ctx.Err() == nil
}

type contextBudgetGuardProvider struct {
	sdk.Provider
	handoff *providerAttemptHandoff
}

func (p contextBudgetGuardProvider) DoGenerate(ctx context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	if err := contextStepBudgetError(ctx); err != nil {
		if p.handoff != nil {
			p.handoff.reject()
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if p.handoff != nil {
			p.handoff.reject()
		}
		return nil, err
	}
	if p.handoff != nil {
		if err := p.handoff.publish(params); err != nil {
			return nil, err
		}
	}
	return p.Provider.DoGenerate(ctx, params)
}

func (p contextBudgetGuardProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	if err := contextStepBudgetError(ctx); err != nil {
		if p.handoff != nil {
			p.handoff.reject()
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		if p.handoff != nil {
			p.handoff.reject()
		}
		return nil, err
	}
	if p.handoff != nil {
		if err := p.handoff.publish(params); err != nil {
			return nil, err
		}
	}
	return p.Provider.DoStream(ctx, params)
}

func contextBudgetGuardedModel(model *sdk.Model, handoff *providerAttemptHandoff) *sdk.Model {
	if model == nil || model.Provider == nil {
		return model
	}
	guarded := *model
	guarded.Provider = contextBudgetGuardProvider{Provider: model.Provider, handoff: handoff}
	return &guarded
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
	cfg.Model = modelWithProviderStreamEventObserver(cfg.Model, cfg.OnProviderStreamEventObserved)
	if cfg.ContextLifecycle == nil {
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	streamCtx, cancel := context.WithCancelCause(ctx)
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

	// Stream emitter: tools targeting the current conversation push
	// side-effect events (attachments, reactions, speech) directly here.
	// Uses sendEvent to avoid goroutine leaks when the consumer stops reading.
	streamEmitter := tools.StreamEmitter(eventGate.emit)
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
	cfg = captureProviderAttemptPrefix(cfg)
	if readMediaState != nil {
		readMediaState.ledger = cfg.ContextMutations
	}
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	approvalTools := append([]sdk.Tool(nil), sdkTools...)
	sdkTools = a.wrapToolsWithHooks(ctx, cfg, sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, limit)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(func(call sdk.ToolCall, metadata map[string]any) {
		eventGate.emitAgentEvent(StreamEvent{
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

	var injectedMessages *injectedMessageState
	if cfg.InjectCh != nil {
		injectedMessages = &injectedMessageState{}
		basePrepare := prepareStep
		prepareStep = func(p *sdk.GenerateParams) *sdk.GenerateParams {
			step := injectedMessages.nextStep()
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
						messageIndex := len(p.Messages)
						p.Messages = append(p.Messages, sdk.UserMessage(text, extra...))
						cfg.ContextMutations.Record(contextfrag.MutationInjectedMessage, fmt.Sprintf("bytes=%d", len(text)))
						injectedMessages.record(step, messageIndex, text)
						a.logger.Info("injected user message into agent stream",
							slog.String("bot_id", cfg.Identity.BotID),
							slog.Int("after_step", step-1),
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
	if readMediaState != nil {
		committedStepMessages.addAdmissionObserver(readMediaState.reconcilePreparedMessages)
	}
	if injectedMessages != nil {
		committedStepMessages.addAdmissionObserver(injectedMessages.reconcilePreparedMessages)
	}
	cfg.preparedStepMessages = committedStepMessages
	prepareStep = a.wrapPrepareStepWithModelHook(streamCtx, cfg, prepareStep)
	var err error
	cfg, err = a.applyBeforeModelCallHook(streamCtx, cfg, 0)
	if err != nil {
		turnError = err.Error()
		sendEvent(ctx, ch, StreamEvent{Type: EventError, Error: turnError})
		return
	}
	installContextStepFailureHandler(&cfg, cancel)
	opts := a.buildGenerateOptions(streamCtx, cfg, sdkTools, approvalTools, prepareStep)
	if stepErr := contextStepBudgetError(streamCtx); stepErr != nil {
		publicError := contextViewStreamError(stepErr)
		turnError = publicError.Error
		aborted = true
		sendEvent(ctx, ch, publicError)
		return
	}
	opts = append(opts, a.onStepOption(streamCtx, cfg, nil))
	var nextDurableStep int
	onStepCommitted := func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
		if cfg.OnStepCommitted != nil {
			if err := cfg.OnStepCommitted(ctx, stepIndex, committedStepMessages.decorate(stepIndex, step, toolExecutionMetadata)); err != nil {
				return err
			}
		}
		nextDurableStep = stepIndex + 1
		return nil
	}
	opts = append(opts, sdk.WithOnStepCommitted(onStepCommitted))

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
	interruptedDurableStep := -1
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
			if contextStepBudgetError(streamCtx) != nil {
				aborted = true
				break
			}
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

	if stepErr := contextStepBudgetError(streamCtx); stepErr != nil {
		publicError := contextViewStreamError(stepErr)
		turnError = publicError.Error
		sendEvent(ctx, ch, publicError)
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
				interruptedDurableStep = stepIndex
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
			finalMessages = readMediaState.mergeMessages(streamResult.Steps, finalMessages, interruptedDurableStep)
		}
		if streamResult.DeferredToolApproval != nil {
			finalMessages = annotateDeferredApproval(finalMessages, *streamResult.DeferredToolApproval)
		}
		finalMessages = toolExecutionMetadata.annotate(finalMessages)
		totalUsage = aggregateStepUsage(streamResult.Steps)
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
	// The legacy recorder is append-only durability state. Flush only messages
	// admitted by the final provider handoff whose target provider step also
	// completed, so provider-start failures and retry-revoked PrepareStep
	// additions cannot be persisted as durable input.
	// StreamResult is written by the SDK goroutine, so read it only after the
	// stream has closed and published its final Steps slice.
	if streamClosed && streamResult != nil {
		injectedMessages.flush(
			streamResult.Steps,
			readMediaState.durableInjections(len(streamResult.Steps), interruptedDurableStep),
			cfg.InjectedRecorder,
		)
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
	cfg = captureProviderAttemptPrefix(cfg)
	if readMediaState != nil {
		readMediaState.ledger = cfg.ContextMutations
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
	if readMediaState != nil {
		committedStepMessages.addAdmissionObserver(readMediaState.reconcilePreparedMessages)
	}
	cfg.preparedStepMessages = committedStepMessages
	prepareStep = a.wrapPrepareStepWithModelHook(genCtx, cfg, prepareStep)
	cfg, err := a.applyBeforeModelCallHook(genCtx, cfg, 0)
	if err != nil {
		return nil, err
	}
	installContextStepFailureHandler(&cfg, cancel)
	opts := a.buildGenerateOptions(genCtx, cfg, sdkTools, approvalTools, prepareStep)
	if stepErr := contextStepBudgetError(genCtx); stepErr != nil {
		return nil, stepErr
	}
	opts = append(opts,
		a.onStepOption(genCtx, cfg, func(step *sdk.StepResult) *sdk.GenerateParams {
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
	if stepErr := contextStepBudgetError(genCtx); stepErr != nil {
		return nil, stepErr
	}
	if err != nil {
		if loopErr := detectGenerateLoopAbort(genCtx, err); loopErr != nil {
			return nil, loopErr
		}
		return nil, fmt.Errorf("generate: %w", err)
	}
	if loopErr := loopAbort.Err(); loopErr != nil {
		return nil, loopErr
	}
	if len(genResult.Steps) > 0 {
		genResult.Usage = aggregateStepUsage(genResult.Steps)
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
		finalMessages = readMediaState.mergeMessages(genResult.Steps, finalMessages, -1)
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

func (a *Agent) buildGenerateOptions(ctx context.Context, cfg RunConfig, tools []sdk.Tool, approvalTools []sdk.Tool, prepareStep func(*sdk.GenerateParams) *sdk.GenerateParams) []sdk.GenerateOption {
	handoff := newProviderAttemptHandoff(cfg)
	tools = canonicalizeProviderToolSchemas(tools)
	cfg.ContextMutations.SetModelInfo(modelID(cfg.Model), models.ResolveClientType(cfg.Model))
	loopReselectMode := a.LoopReselectMode()
	if loopReselectMode == LoopReselectOff {
		cfg.ContextStepReselector = nil
	}
	switch {
	case cfg.ContextStepReselector == nil:
		cfg.ContextMutations.SetLoopSelectionMode(contextfrag.LoopSelectionLegacyPrune)
	case loopReselectMode == LoopReselectShadow:
		cfg.ContextMutations.SetLoopSelectionMode(contextfrag.LoopSelectionSuffixOnlyShadow)
	default:
		cfg.ContextMutations.SetLoopSelectionMode(contextfrag.LoopSelectionSuffixOnly)
	}

	plan := cfg.ContextCachePlan
	providerAttemptPrefixCount := len(cfg.Messages)
	if cfg.initialProviderPrefixSet {
		providerAttemptPrefixCount = cfg.initialProviderMessageCount
	}
	system, messages, tools, systemPrepended, actualStableCount := models.ApplyPromptCacheWithPlan(
		cfg.Model, cfg.PromptCacheTTL, plan, cfg.System, cfg.Messages, tools,
	)
	providerProvenance := clonePreparedMessageProvenance(cfg.providerMessageProvenance)
	if systemPrepended {
		providerProvenance = providerProvenance.prependSynthetic()
	}
	plan.StableMessageCount = actualStableCount
	if systemPrepended {
		providerAttemptPrefixCount++
	}
	initialProviderMessageCount := clampStableMessageCount(providerAttemptPrefixCount, len(messages))
	publishContextCachePlan(cfg, plan)

	var providerTools []sdk.Tool
	if len(tools) > 0 && cfg.SupportsToolCall {
		providerTools = tools
	}
	initialParams := prepareProviderAttempt(ctx, cfg, handoff, loopReselectMode, systemPrepended, initialProviderMessageCount, 0, providerProvenance, &sdk.GenerateParams{
		System: system, Messages: messages, Tools: providerTools,
	})
	system = initialParams.System
	messages = initialParams.Messages
	providerTools = initialParams.Tools
	if cfg.BackgroundManager != nil {
		basePrepare := prepareStep
		prepareStep = func(p *sdk.GenerateParams) *sdk.GenerateParams {
			if p == nil {
				return nil
			}
			p.Messages = removeBackgroundSummaryMessages(p.Messages, initialProviderMessageCount)
			if basePrepare != nil {
				if override := basePrepare(p); override != nil {
					p = override
				}
			}
			if summary := strings.TrimSpace(cfg.BackgroundManager.RunningTasksSummary(cfg.Identity.BotID, cfg.Identity.SessionID)); summary != "" {
				cfg.ContextMutations.Record(contextfrag.MutationBackgroundSummary, fmt.Sprintf("bytes=%d", len(summary)))
				p.Messages = append(p.Messages, backgroundSummaryMessage(summary))
			}
			return p
		}
	}
	opts := []sdk.GenerateOption{
		sdk.WithModel(contextBudgetGuardedModel(cfg.Model, handoff)),
		sdk.WithMessages(messages),
		sdk.WithSystem(system),
		sdk.WithMaxSteps(-1),
	}
	if limits := cfg.GenerationLimits(); limits.Requested {
		opts = append(opts, sdk.WithMaxTokens(limits.MaxOutputTokens))
	}
	if len(providerTools) > 0 {
		opts = append(opts, sdk.WithTools(providerTools))
	}
	approvalHandler := cfg.ToolApprovalHandler
	if a != nil && a.hookService != nil {
		approvalHandler = a.wrapApprovalHandlerWithHooks(cfg, approvalTools, approvalHandler)
	}
	if approvalHandler != nil {
		opts = append(opts, sdk.WithApprovalHandler(approvalHandler))
	}

	prepareIndex := 0
	basePrepare := prepareStep
	stepPrepare := func(p *sdk.GenerateParams) *sdk.GenerateParams {
		if basePrepare != nil {
			if override := basePrepare(p); override != nil {
				p = override
			}
		}
		if p == nil {
			return nil
		}
		defer func() { prepareIndex++ }()
		stepProvenance := cfg.preparedStepMessages.latestProvenance(p.Messages)
		return prepareProviderAttempt(ctx, cfg, handoff, loopReselectMode, systemPrepended, initialProviderMessageCount, prepareIndex+1, stepProvenance, p)
	}
	opts = append(opts, sdk.WithPrepareStep(stepPrepare))

	opts = append(opts, models.BuildReasoningOptions(models.SDKModelConfig{
		ClientType:            models.ResolveClientType(cfg.Model),
		ChatCompletionsCompat: cfg.ChatCompletionsCompat,
		ReasoningConfig:       cfg.ReasoningConfig,
	})...)
	return opts
}

func clampStableMessageCount(count, total int) int {
	if count < 0 {
		return 0
	}
	if count > total {
		return total
	}
	return count
}

func prepareProviderAttempt(
	ctx context.Context,
	cfg RunConfig,
	handoff *providerAttemptHandoff,
	mode LoopReselectMode,
	systemPrepended bool,
	prefixCount,
	stepIndex int,
	provenance preparedMessageProvenance,
	params *sdk.GenerateParams,
) *sdk.GenerateParams {
	if params == nil {
		return nil
	}
	prefixCount = clampStableMessageCount(prefixCount, len(params.Messages))
	snapshot := contextfrag.StepSnapshot{StepIndex: stepIndex}
	reselector := cfg.ContextStepReselector
	mode = mode.Normalize()
	if mode == LoopReselectOff {
		reselector = nil
	}
	inputAllowance := stepReselectionAllowance(cfg)
	reselectionDetail := ""
	if reselector != nil && prefixCount < len(params.Messages) {
		beforeMessages := append([]sdk.Message(nil), params.Messages...)
		selection := reselector(ctx, ContextStepSelectionInput{
			Scope: cfg.ContextScope, InitialMessageCount: prefixCount, Messages: params.Messages,
			BudgetMaxTokens:              remainingStepBudget(inputAllowance, params, prefixCount),
			ProviderSystem:               params.System,
			ProviderTools:                params.Tools,
			ProviderInputAllowanceTokens: inputAllowance,
			RecentProtectTokens:          cfg.ContextRecentProtectTokens, KeepRecentToolResults: stepReselectKeepRecentToolResults,
			MinMessages: stepReselectMinMessages,
		})
		switch {
		case mode == LoopReselectShadow:
			snapshot.Dropped = selection.Dropped
			snapshot.Truncated = selection.Truncated
			snapshot.DropReasons = copyDropReasons(selection.DropReasons)
			switch {
			case selection.FatalError != nil:
				snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeWouldFail
			case selection.Messages != nil || selection.Dropped > 0 || selection.Truncated > 0:
				snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeWouldApply
			default:
				snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeUnchanged
			}
		case selection.FatalError != nil:
			return failPreparedProviderAttempt(cfg, handoff, params, snapshot, prefixCount, provenance, selection.FatalError)
		case selection.Messages != nil && stepSelectionPreservesPrefix(beforeMessages, selection.Messages, prefixCount):
			sourceIndexes, valid := selectionMessageSourceIndexes(beforeMessages, selection.Messages, prefixCount, selection)
			if !valid {
				return failPreparedProviderAttempt(cfg, handoff, params, snapshot, prefixCount, provenance, fmt.Errorf(
					"%w: invalid provider step message provenance",
					contextfrag.ErrBudgetUnsatisfied,
				))
			}
			composed, composedOK := composePreparedMessageProvenance(provenance, len(beforeMessages), sourceIndexes)
			if !composedOK {
				composed = failClosedPreparedMessageProvenance(provenance, len(selection.Messages), prefixCount)
			}
			params.Messages = selection.Messages
			provenance = composed
			snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeApplied
			snapshot.ReselectionApplied = true
			snapshot.Dropped = selection.Dropped
			snapshot.Truncated = selection.Truncated
			snapshot.DropReasons = copyDropReasons(selection.DropReasons)
			if selection.Dropped > 0 || selection.Truncated > 0 {
				reselectionDetail = contextStepSelectionDetail(selection)
			}
		default:
			snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeUnchanged
		}
	}
	if overflow := providerAttemptEnvelopeOverflow(params, inputAllowance); overflow > 0 {
		return failPreparedProviderAttempt(cfg, handoff, params, snapshot, prefixCount, provenance, fmt.Errorf(
			"%w: input_overflow=%d allowance=%d",
			contextfrag.ErrBudgetUnsatisfied,
			overflow,
			inputAllowance,
		))
	}
	stagePreparedProviderAttempt(ctx, handoff, snapshot, systemPrepended, reselectionDetail, provenance)
	return params
}

func stagePreparedProviderAttempt(
	ctx context.Context,
	handoff *providerAttemptHandoff,
	snapshot contextfrag.StepSnapshot,
	systemPrepended bool,
	reselectionDetail string,
	provenance preparedMessageProvenance,
) {
	if !providerAttemptDispatchAllowed(ctx) {
		handoff.reject(provenance)
		return
	}
	handoff.stage(snapshot, systemPrepended, reselectionDetail, provenance)
}

func providerAttemptEnvelopeOverflow(params *sdk.GenerateParams, allowance int) int {
	if params == nil || allowance <= 0 {
		return 0
	}
	return contextfrag.ProviderEnvelopeTokens(params.System, params.Messages, params.Tools) - allowance
}

func failPreparedProviderAttempt(
	cfg RunConfig,
	handoff *providerAttemptHandoff,
	params *sdk.GenerateParams,
	snapshot contextfrag.StepSnapshot,
	prefixCount int,
	provenance preparedMessageProvenance,
	err error,
) *sdk.GenerateParams {
	switch snapshot.ReselectionOutcome {
	case contextfrag.ReselectionOutcomeWouldApply, contextfrag.ReselectionOutcomeWouldFail:
	default:
		snapshot.ReselectionOutcome = contextfrag.ReselectionOutcomeFailed
	}
	snapshot.ReselectionApplied = false
	params.Messages = append([]sdk.Message(nil), params.Messages[:prefixCount]...)
	handoff.reject(provenance)
	cfg.ContextMutations.AppendStepSnapshot(snapshot)
	if cfg.contextStepFailure != nil {
		cfg.contextStepFailure(err)
	}
	return params
}

func copyDropReasons(reasons map[string]int) map[string]int {
	if len(reasons) == 0 {
		return nil
	}
	out := make(map[string]int, len(reasons))
	for reason, count := range reasons {
		out[reason] = count
	}
	return out
}

// stepReselectionAllowance is the input allowance every provider dispatch is
// checked against: the window minus the output reserve, whether the plan
// recorded it or the run assembled its context without a plan.
func stepReselectionAllowance(cfg RunConfig) int {
	window, reserve := cfg.ContextBudgetMaxTokens, 0
	if plan := cfg.ContextManifest.BudgetPlan; plan != nil {
		window, reserve = plan.Window, plan.OutputReserve
	} else if window > 0 {
		reserve = cfg.GenerationLimits().MaxOutputTokens
	}
	if window <= 0 {
		return 0
	}
	return max(window-reserve, 1)
}

func remainingStepBudget(maxTokens int, params *sdk.GenerateParams, prefixCount int) int {
	if maxTokens <= 0 || params == nil {
		return 0
	}
	prefixCount = clampStableMessageCount(prefixCount, len(params.Messages))
	remaining := maxTokens - contextfrag.ProviderEnvelopeTokens(params.System, params.Messages[:prefixCount], params.Tools)
	if remaining < 1 {
		return 1
	}
	return remaining
}

func stepSelectionPreservesPrefix(before, after []sdk.Message, count int) bool {
	if count < 0 || count > len(before) || count > len(after) {
		return false
	}
	return reflect.DeepEqual(before[:count], after[:count])
}

func contextStepSelectionDetail(selection ContextStepSelectionResult) string {
	if len(selection.DropReasons) == 0 {
		return fmt.Sprintf("dropped=%d truncated=%d", selection.Dropped, selection.Truncated)
	}
	reasons := make([]string, 0, len(selection.DropReasons))
	for reason := range selection.DropReasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s:%d", reason, selection.DropReasons[reason]))
	}
	return fmt.Sprintf("dropped=%d truncated=%d reasons=%s", selection.Dropped, selection.Truncated, strings.Join(parts, ","))
}

func publishContextCachePlan(cfg RunConfig, plan contextfrag.CachePlan) {
	if cfg.ContextManifest.CachePlan != nil {
		*cfg.ContextManifest.CachePlan = plan
	} else {
		cfg.ContextManifest.CachePlan = &plan
	}
	if cfg.ContextLifecycle != nil {
		cfg.ContextLifecycle.SetManifest(cfg.ContextManifest)
	}
}

func (a *Agent) onStepOption(ctx context.Context, cfg RunConfig, after func(*sdk.StepResult) *sdk.GenerateParams) sdk.GenerateOption {
	modelStepIndex := 0
	return sdk.WithOnStep(func(step *sdk.StepResult) *sdk.GenerateParams {
		recordContextCacheUsage(cfg.ContextMutations, modelStepIndex, step)
		a.runAfterModelCallHook(ctx, cfg, step, modelStepIndex)
		modelStepIndex++
		if after != nil {
			return after(step)
		}
		return nil
	})
}

func recordContextCacheUsage(ledger *contextfrag.MutationLedger, stepIndex int, step *sdk.StepResult) {
	if ledger == nil || step == nil {
		return
	}
	detail := step.Usage.InputTokenDetails
	if step.Usage.CachedInputTokens == 0 && detail.NoCacheTokens == 0 && detail.CacheReadTokens == 0 &&
		detail.CacheWriteTokens == 0 && detail.CacheWrite5mTokens == 0 && detail.CacheWrite1hTokens == 0 {
		return
	}
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{
		StepIndex: stepIndex, NoCacheTokens: detail.NoCacheTokens, CacheReadTokens: detail.CacheReadTokens,
		CacheWriteTokens: detail.CacheWriteTokens, CacheWrite5mTokens: detail.CacheWrite5mTokens,
		CacheWrite1hTokens: detail.CacheWrite1hTokens,
	})
}

func aggregateStepUsage(steps []sdk.StepResult) sdk.Usage {
	var total sdk.Usage
	for _, step := range steps {
		total.InputTokens += step.Usage.InputTokens
		total.OutputTokens += step.Usage.OutputTokens
		total.TotalTokens += step.Usage.TotalTokens
		total.ReasoningTokens += step.Usage.ReasoningTokens
		total.CachedInputTokens += step.Usage.CachedInputTokens
		total.InputTokenDetails.NoCacheTokens += step.Usage.InputTokenDetails.NoCacheTokens
		total.InputTokenDetails.CacheReadTokens += step.Usage.InputTokenDetails.CacheReadTokens
		total.InputTokenDetails.CacheWriteTokens += step.Usage.InputTokenDetails.CacheWriteTokens
		total.InputTokenDetails.CacheWrite5mTokens += step.Usage.InputTokenDetails.CacheWrite5mTokens
		total.InputTokenDetails.CacheWrite1hTokens += step.Usage.InputTokenDetails.CacheWrite1hTokens
		total.OutputTokenDetails.TextTokens += step.Usage.OutputTokenDetails.TextTokens
		total.OutputTokenDetails.ReasoningTokens += step.Usage.OutputTokenDetails.ReasoningTokens
	}
	return total
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
	case tools.StreamEventSpawnProgress:
		return StreamEvent{Type: EventProgress, ProgressStatus: "spawn_running"}
	default:
		return StreamEvent{}
	}
}

func backgroundSummaryMessage(summary string) sdk.Message {
	return sdk.UserMessage(contextfrag.BackgroundSummaryMessagePrefix + summary)
}

// removeBackgroundSummaryMessages strips summary carrier messages appended by
// earlier steps so each step rebuilds exactly one fresh summary. keepPrefix
// guards the compiled initial context: only loop-appended messages match.
func removeBackgroundSummaryMessages(messages []sdk.Message, keepPrefix int) []sdk.Message {
	if keepPrefix < 0 {
		keepPrefix = 0
	}
	for i := keepPrefix; i < len(messages); i++ {
		if !contextfrag.IsBackgroundSummaryCarrier(messages[i]) {
			continue
		}
		out := make([]sdk.Message, 0, len(messages)-1)
		out = append(out, messages[:i]...)
		for _, msg := range messages[i+1:] {
			if !contextfrag.IsBackgroundSummaryCarrier(msg) {
				out = append(out, msg)
			}
		}
		return out
	}
	return messages
}

// injectedMessageText prefers the headerified rendering; when it falls back to
// raw text it guards the reserved background-summary prefix so an injected
// user message can never masquerade as a summary carrier.
func injectedMessageText(injected InjectMessage) string {
	if text := strings.TrimSpace(injected.HeaderifiedText); text != "" {
		return text
	}
	text := strings.TrimSpace(injected.Text)
	if strings.HasPrefix(text, contextfrag.BackgroundSummaryMessagePrefix) {
		return "[injected]\n" + text
	}
	return text
}

const (
	stepReselectKeepRecentToolResults = 4
	stepReselectMinMessages           = 20
)

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
	ctx context.Context,
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
		if p != nil && providerAttemptDispatchAllowed(ctx) {
			_ = forkContext.Store(p.Messages)
		}
		return p
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
	_ *stepMessageCapture,
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
	retryInput := retryProviderAttemptMessages(cfg, prevResult)
	accumulatedCount := len(prevResult.Messages)

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

		// Re-invoke from the failed attempt's exact provider input plus its
		// partial output, then run the same preflight as every other call.
		retryCfgCopy := prepareMidStreamRetryConfigWithMessages(
			cfg,
			retryInput.messages,
			retryInput.provenance,
			accumulatedCount,
			errMsg,
		)
		if a == nil || a.contextViewApplier == nil {
			retryCfgCopy = retryCfgCopy.RefreshContextFrag()
		}
		retryOpts := a.buildGenerateOptions(streamCtx, retryCfgCopy, sdkTools, approvalTools, prepareStep)
		retryOpts = append(retryOpts, a.onStepOption(streamCtx, retryCfgCopy, nil))
		if onStepCommitted != nil {
			retryOpts = append(retryOpts, sdk.WithOnStepCommitted(func(ctx context.Context, stepIndex int, step *sdk.StepResult) error {
				return onStepCommitted(ctx, stepOffset+stepIndex, step)
			}))
		}
		if contextStepBudgetError(streamCtx) != nil {
			return prevResult, true
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
				if contextStepBudgetError(streamCtx) != nil {
					aborted = true
					break
				}
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

func prepareMidStreamRetryConfig(cfg RunConfig, accumulated []sdk.Message, errMsg string) RunConfig {
	merged := make([]sdk.Message, 0, len(cfg.Messages)+len(accumulated))
	merged = append(merged, cfg.Messages...)
	merged = append(merged, accumulated...)
	provenance := clonePreparedMessageProvenance(cfg.providerMessageProvenance)
	if provenance.known {
		for range accumulated {
			provenance.messageIndexes = append(provenance.messageIndexes, -1)
		}
	}
	return prepareMidStreamRetryConfigWithMessages(cfg, merged, provenance, len(accumulated), errMsg)
}

func prepareMidStreamRetryConfigWithMessages(
	cfg RunConfig,
	messages []sdk.Message,
	provenance preparedMessageProvenance,
	accumulatedCount int,
	errMsg string,
) RunConfig {
	cfg.Messages = append([]sdk.Message(nil), messages...)
	cfg.providerMessageProvenance = clonePreparedMessageProvenance(provenance)
	attempt := cfg.ContextMutations.AdvanceAttempt()
	errorHash := sha256.Sum256([]byte(strings.TrimSpace(errMsg)))
	cfg.ContextMutations.Record(contextfrag.MutationMidStreamRetry,
		fmt.Sprintf("attempt=%d accumulated=%d error_sha256=%x", attempt, accumulatedCount, errorHash))
	return cfg
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

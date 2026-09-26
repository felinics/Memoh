package native

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/models"
)

// runGenerate runs the non-streaming agent invocation with a Memoh-owned step
// loop: every step performs exactly one provider model call and executes its
// tool batch through toolexec.ExecuteTools. A final step whose commit returns
// NextInputs continues on the next inner-loop iteration of the same engine.
func (a *Agent) runGenerate(ctx context.Context, cfg RunConfig) (_ *GenerateResult, retErr error) {
	if cfg.ContextLifecycle == nil {
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	cfg.capabilityChanges = &atomic.Bool{}
	genCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// The non-streaming path runs the same loop as the streaming one and needs
	// the same per-call timing. It has no stream to observe and nothing to
	// steer, so the observer here exists for the spans alone.
	cfg.Model = modelWithProviderCallObserver(cfg.Model, nil, nil)
	defer func() {
		event := hooks.EventTurnEnd
		errMsg := ""
		if retErr != nil {
			event = hooks.EventTurnError
			errMsg = retErr.Error()
		}
		a.runTurnHook(context.WithoutCancel(ctx), cfg, event, errMsg)
	}()
	defer func() {
		a.logContextLifecycle(cfg)
	}()

	// Collecting emitter: tools push side-effect events here during generation.
	collected := newToolEventCollector()
	defer collected.Close()
	collectEmitter := tools.StreamEmitter(func(evt tools.ToolStreamEvent) {
		collected.Add(evt)
	})
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
		sdkTools, toolUsage, toolUsageFrags, toolDefs, err = a.assembleTools(genCtx, cfg, collectEmitter, false)
		if err != nil {
			return nil, fmt.Errorf("assemble tools: %w", err)
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
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMediaToolPresent(sdkTools), a != nil && a.hookService != nil, false)
	var contextViewErr error
	cfg, contextViewErr = a.applyContextView(genCtx, cfg)
	if contextViewErr != nil {
		return nil, contextViewErr
	}
	cfg = captureProviderAttemptPrefix(cfg)
	toolExecutionMetadata := newToolExecutionMetadataRegistry(nil)
	cfg.ToolApprovalHandler = toolExecutionMetadata.wrap(cfg.ToolApprovalHandler)

	var toolLoopGuard *ToolLoopGuard
	var textLoopGuard *TextLoopGuard
	toolLoopAbortCallIDs := newToolAbortRegistry()
	if cfg.LoopDetection.Enabled {
		toolLoopGuard = NewToolLoopGuard(ToolLoopRepeatThreshold, ToolLoopWarningsBeforeAbort)
		textLoopGuard = NewTextLoopGuard(LoopDetectedStreakThreshold, LoopDetectedMinNewGramsPerChunk, SentialOptions{})
	}

	// The execution chain over the assembled tools; a capability refresh
	// rebuilds the same chain over the re-assembled set.
	sdkTools, approvalTools := a.wrapExecutableTools(ctx, cfg, sdkTools, toolExecutionMetadata, toolLoopGuard, toolLoopAbortCallIDs)

	// The loop owns the dynamic input messages it appends at step boundaries
	// (read-media carriers here; the stream loop adds live injections).
	dynamic := newLoopDynamicInputs(cfg.StepIndexOffset)
	cfg.dynamicInputs = dynamic
	prepareStep := a.wrapPrepareStepWithModelHook(genCtx, cfg, nil)
	cfg, err := a.applyBeforeModelCallHook(genCtx, cfg, 0)
	if err != nil {
		return nil, err
	}
	// The generate loop surfaces context-preparation failures as returned
	// errors at the next dispatch boundary; the run context is no longer
	// cancelled with the failure cause.
	failure := &contextStepFailureCapture{}
	installContextStepFailureHandler(&cfg, failure.capture)
	dispatch, dispatchErr := a.buildGenerateDispatch(genCtx, cfg, sdkTools, approvalTools, prepareStep)
	if dispatchErr != nil {
		return nil, fmt.Errorf("generate: %w", dispatchErr)
	}
	if stepErr := failure.err(); stepErr != nil {
		return nil, stepErr
	}
	if cfg.Model == nil {
		return nil, fmt.Errorf("generate: %w", errors.New("twilightai: model is required (use WithModel)"))
	}
	if cfg.Model.Provider == nil {
		return nil, fmt.Errorf("generate: %w", fmt.Errorf("twilightai: model %q has no provider", cfg.Model.ID))
	}

	var thread stepThread
	thread.reset(dispatch)
	commitStep := func(sdkStep int, sr *step.Record) (StepDirective, error) {
		var dir StepDirective
		if cfg.OnStepCommitted != nil {
			stepIndex := sdkStep + cfg.StepIndexOffset
			decorated := decorateCommittedStep(dynamic.stepAdditions(stepIndex), sr, toolExecutionMetadata)
			var err error
			dir, err = cfg.OnStepCommitted(dynamic.withMessageOrigins(genCtx, stepIndex), stepIndex, decorated)
			if err != nil {
				return StepDirective{}, fmt.Errorf("generate: %w", tagStepCommitError(fmt.Errorf("twilightai: commit step %d: %w", sdkStep, err)))
			}
		}
		if cfg.capabilityChanges.Swap(false) && sr.Deferred == nil {
			refreshed, err := a.refreshCapabilities(genCtx, ctx, &cfg, collectEmitter, false,
				readMediaState, toolExecutionMetadata, toolLoopGuard, toolLoopAbortCallIDs)
			if err != nil {
				return StepDirective{}, fmt.Errorf("generate: refresh capabilities after step %d: %w", sdkStep, err)
			}
			thread.pendingRefresh = &refreshed
		}
		return dir, nil
	}
	// afterStep mirrors the legacy OnStep hook: cache-usage accounting and the
	// after-model-call hook run on every step, then loop detection decides
	// whether the run aborts.
	afterStep := func(sdkStep int, sr *step.Record) error {
		recordContextCacheUsage(cfg.ContextMutations, sdkStep, sr)
		a.runAfterModelCallHook(genCtx, cfg, sr, sdkStep)
		if !cfg.LoopDetection.Enabled {
			return nil
		}
		if toolLoopAbortCallIDs.Any() {
			return ErrToolLoopDetected
		}
		if textLoopGuard != nil && isNonEmptyString(sr.Result.Text) {
			if textLoopGuard.Inspect(sr.Result.Text).Abort {
				return ErrTextLoopDetected
			}
		}
		return nil
	}

	var (
		lastResult sdk.ModelResult
		// deferred is the decision that parked the last step; the returned
		// messages carry it on the parked call the same way the stream's
		// terminal messages do.
		deferred *toolexec.ToolApprovalResult
		// finalTexts holds the answer of every final step that a directive or
		// a refreshed tool set turned into another call, so the result text
		// reads as the model produced it across the continuation.
		finalTexts  []string
		allSteps    []step.Record
		allMessages []sdk.Message
	)

	for sdkStep := 0; ; sdkStep++ {
		stepParams := thread.advance(cfg, dynamic, stepBoundary{
			first:     sdkStep == 0,
			committed: len(allSteps),
			readMedia: readMediaState,
		})
		if stepErr := failure.err(); stepErr != nil {
			return nil, stepErr
		}
		// Dispatch boundary: never invoke the provider on a dead context, and
		// publish the staged provider-attempt state exactly once per call.
		if ctxErr := genCtx.Err(); ctxErr != nil {
			thread.dispatch.handoff.reject()
			if loopErr := detectGenerateLoopAbort(genCtx, ctxErr); loopErr != nil {
				return nil, loopErr
			}
			return nil, fmt.Errorf("generate: %w", ctxErr)
		}
		if err := thread.dispatch.handoff.publish(stepParams); err != nil {
			return nil, fmt.Errorf("generate: %w", err)
		}
		result, err := cfg.Model.Generate(genCtx, stepParams)
		if err != nil {
			if loopErr := detectGenerateLoopAbort(genCtx, err); loopErr != nil {
				return nil, loopErr
			}
			return nil, fmt.Errorf("generate: %w", err)
		}
		lastResult = result

		sr, kind, err := settleStep(genCtx, thread.dispatch, result, result.TextProviderMetadata, nil)
		if err != nil {
			return nil, fmt.Errorf("generate: %w", err)
		}
		dir, err := commitStep(sdkStep, &sr)
		if err != nil {
			return nil, err
		}
		allSteps = append(allSteps, sr)
		allMessages = append(allMessages, sr.Messages...)
		if loopErr := afterStep(sdkStep, &sr); loopErr != nil {
			return nil, loopErr
		}
		if kind == stepDeferred {
			deferred = sr.Deferred
			break
		}
		thread.takeDirective(dir)
		refusedOut := thread.refusedBatchEndsRun(kind)
		if kind == stepFinal {
			// A directive or a refreshed tool set gives the model another
			// call on the same thread; the step is committed either way.
			if !thread.continues() {
				break
			}
			if text := strings.TrimSpace(result.Text); text != "" {
				finalTexts = append(finalTexts, text)
			}
		}
		if refusedOut {
			// Every call of the last maxRefusedBatches steps was refused and
			// nothing new arrived; the run ends like a detected tool loop, its
			// answered steps committed.
			return nil, ErrToolLoopDetected
		}
		thread.extend(sr.Messages)
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

	finalMessages := allMessages
	finalMessages, feedbackIndexes := dynamic.mergeReadMedia(allSteps, finalMessages, -1)
	finalMessages = toolExecutionMetadata.annotate(finalMessages)
	if deferred != nil {
		finalMessages = annotateDeferredApproval(finalMessages, *deferred)
	}
	usage := aggregateStepUsage(allSteps)
	return &GenerateResult{
		InternalFeedbackIndexes: feedbackIndexes,
		Messages:                finalMessages,
		Text:                    joinFinalTexts(finalTexts, lastResult.Text),
		Attachments:             attachments,
		Reactions:               reactions,
		Speeches:                speeches,
		Usage:                   &usage,
	}, nil
}

// generateDispatch is the assembled single-call input for the generate loop:
// the initial provider request, the executable tool set, the composed per-step
// prepare chain, the provider-attempt handoff, and the approval handler.
type generateDispatch struct {
	params      sdk.Request
	execTools   []toolexec.Tool
	prepareStep func(*sdk.Request) *sdk.Request
	handoff     *providerAttemptHandoff
	approve     func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error)
	// initialMessageCount is the compiled provider prefix length (including a
	// promoted system message), the guard for background-summary stripping.
	initialMessageCount int
	// systemPrepended records that the prompt-cache plan moved the system
	// prompt into the message prefix, where a capability refresh cannot
	// replace it.
	systemPrepended bool
}

// buildGenerateDispatch assembles the single-call input for the Memoh-owned
// step loops: prompt-cache planning, provider-attempt staging, and
// prepare-step composition, yielding the concrete sdk.Request and the composed
// step-prepare function. The executable tool set stays local to the loop; the
// request carries only the provider-bound definitions.
func (a *Agent) buildGenerateDispatch(ctx context.Context, cfg RunConfig, sdkTools []toolexec.Tool, approvalTools []toolexec.Tool, prepareStep func(*sdk.Request) *sdk.Request) (generateDispatch, error) {
	handoff := newProviderAttemptHandoff(cfg)
	sdkTools = canonicalizeProviderToolSchemas(sdkTools)
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
	system, messages, planTools, systemPrepended, actualStableCount := models.ApplyPromptCacheWithPlan(
		cfg.Model, cfg.PromptCacheTTL, plan, cfg.System, cfg.Messages, sdkTools,
	)
	// A mid-stream retry re-dispatches the boundary whose dynamic messages
	// were admitted by the failed attempt; their stored positions shift with
	// the promoted system message.
	providerRefs := cloneDynamicSourceRefs(cfg.retryDynamicRefs)
	if systemPrepended {
		providerRefs = shiftDynamicSourceRefs(providerRefs, 1)
	}
	plan.StableMessageCount = actualStableCount
	if systemPrepended {
		providerAttemptPrefixCount++
	}
	initialProviderMessageCount := clampStableMessageCount(providerAttemptPrefixCount, len(messages))
	publishContextCachePlan(cfg, plan)

	var executableTools []toolexec.Tool
	if len(planTools) > 0 && cfg.SupportsToolCall {
		executableTools = planTools
	}
	toolDefs, toolDefErr := toolexec.ToolDefinitionsFromTools(executableTools)
	if toolDefErr != nil {
		return generateDispatch{}, toolDefErr
	}
	initialParams := prepareProviderAttempt(ctx, cfg, handoff, loopReselectMode, systemPrepended, initialProviderMessageCount, 0, providerRefs, &sdk.Request{
		System: system, Messages: messages, Tools: toolDefs,
	})
	system = initialParams.System
	messages = initialParams.Messages
	toolDefs = initialParams.Tools
	if cfg.BackgroundManager != nil {
		basePrepare := prepareStep
		prepareStep = func(p *sdk.Request) *sdk.Request {
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

	// The provider-scoped model ID is bound here so every dispatch through the
	// single-call seam carries a complete request; sdk.Model.Generate/Stream
	// re-verifies it against the bound model.
	modelIdentifier := ""
	if cfg.Model != nil {
		modelIdentifier = cfg.Model.ID
	}
	params := sdk.Request{
		Model:    modelIdentifier,
		System:   system,
		Messages: messages,
		Tools:    toolDefs,
	}
	if limits := cfg.GenerationLimits(); limits.Requested {
		maxTokens := limits.MaxOutputTokens
		params.MaxTokens = &maxTokens
	}
	approvalHandler := cfg.ToolApprovalHandler
	if a != nil && a.hookService != nil {
		approvalHandler = a.wrapApprovalHandlerWithHooks(cfg, approvalTools, approvalHandler)
	}

	prepareIndex := 0
	basePrepare := prepareStep
	stepPrepare := func(p *sdk.Request) *sdk.Request {
		if basePrepare != nil {
			if override := basePrepare(p); override != nil {
				p = override
			}
		}
		if p == nil {
			return nil
		}
		defer func() { prepareIndex++ }()
		return prepareProviderAttempt(ctx, cfg, handoff, loopReselectMode, systemPrepended, initialProviderMessageCount, prepareIndex+1, cfg.dynamicInputs.pendingRefs(), p)
	}

	if key := models.PromptCacheKey(cfg.Model, cfg.PromptCacheTTL, cfg.Identity.SessionID); key != "" {
		params.PromptCacheKey = &key
	}
	models.ApplyReasoningToRequest(&params, models.SDKModelConfig{
		ClientType:            models.ResolveClientType(cfg.Model),
		ChatCompletionsCompat: cfg.ChatCompletionsCompat,
		ReasoningConfig:       cfg.ReasoningConfig,
	})
	return generateDispatch{
		params:              params,
		execTools:           executableTools,
		prepareStep:         stepPrepare,
		handoff:             handoff,
		approve:             approvalHandler,
		initialMessageCount: initialProviderMessageCount,
		systemPrepended:     systemPrepended,
	}, nil
}

// contextStepFailureCapture records the first context-preparation failure the
// shared prepare helpers report through cfg.contextStepFailure. The generate
// loop returns it at the next dispatch boundary instead of cancelling the run
// context with the failure cause.
type contextStepFailureCapture struct {
	mu    sync.Mutex
	cause error
}

func (c *contextStepFailureCapture) capture(cause error) {
	if cause == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause == nil {
		c.cause = cause
	}
}

// err returns the captured failure, normalized to the budget sentinels the
// legacy path surfaced via contextStepBudgetError.
func (c *contextStepFailureCapture) err() error {
	c.mu.Lock()
	cause := c.cause
	c.mu.Unlock()
	switch {
	case cause == nil:
		return nil
	case errors.Is(cause, contextfrag.ErrProtectedContextOverflow):
		return contextfrag.ErrProtectedContextOverflow
	case errors.Is(cause, contextfrag.ErrBudgetUnsatisfied):
		return contextfrag.ErrBudgetUnsatisfied
	default:
		return cause
	}
}

// joinFinalTexts joins the answers of a continued run the way the segment
// join did before the loop moved in-process: one per line, blanks dropped.
func joinFinalTexts(previous []string, last string) string {
	parts := make([]string, 0, len(previous)+1)
	parts = append(parts, previous...)
	if text := strings.TrimSpace(last); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return last
	}
	return strings.Join(parts, "\n")
}

package native

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/hooks"
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
	onDemandTools      string
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
		onDemandTools:      NormalizeOnDemandTools(deps.OnDemandTools),
	}
}

// onDemandToolsEnabled reports whether rarely used tool groups load on demand
// through tool_describe / tool_call instead of riding every request.
func (a *Agent) onDemandToolsEnabled() bool {
	return a != nil && a.onDemandTools != OnDemandToolsOff
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
		prepared, err := a.contextViewApplier(ctx, cfg)
		prepared.RecoverContextBudget = nil
		return prepared, err
	}
	cfg.RecoverContextBudget = nil
	return cfg.RefreshContextFrag(), nil
}

// contextViewStreamError is the error event for a model context that could not
// be prepared. The chain keeps contextfrag's budget sentinels for the
// application to name; any other preparation failure is the runtime's own.
func contextViewStreamError(err error) StreamEvent {
	event := StreamEvent{Type: EventError, Cause: errs.WrapWithDepth(1, err, "prepare context view")}
	if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) && !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		event.Code = codeRunFailed
	}
	return event
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
	part, _, err := a.ExecuteToolWithUIMetadata(ctx, cfg, call)
	return part, err
}

// ExecuteToolWithUIMetadata runs one tool outside a stream (deferred approval,
// hook tests) and returns the UI-only payloads stripped from its output
// alongside the model-facing result, so callers can persist them on
// harness-side channels (row metadata) instead of leaking them to the model.
func (a *Agent) ExecuteToolWithUIMetadata(ctx context.Context, cfg RunConfig, call sdk.ToolCall) (sdk.ToolResultPart, map[string]any, error) {
	assembled, err := a.assembleTools(ctx, cfg, nil, false)
	if err != nil {
		return sdk.ToolResultPart{}, nil, fmt.Errorf("assemble tools: %w", err)
	}
	sdkTools, _ := decorateReadMediaTools(cfg.Model, assembled.tools)
	uiMetadata := newToolExecutionMetadataRegistry(nil)
	sdkTools = uiMetadata.wrapToolUIOutput(sdkTools)
	sdkTools = tools.WrapToolOutputLimits(sdkTools, a.Limits().ToolOutputLimit())
	sdkTools = assembled.bridge.Bind(sdkTools)
	for i := range sdkTools {
		tool := sdkTools[i]
		if tool.Name != call.ToolName {
			continue
		}
		if tool.Execute == nil {
			return sdk.ToolResultPart{}, nil, fmt.Errorf("tool %q has no execute handler", call.ToolName)
		}
		execCtx := &toolexec.ToolExecContext{
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
				Result:     toolexec.OutputFromValue(limitedErr.Error()),
				IsError:    true,
			}, nil, nil
		}
		return sdk.ToolResultPart{
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			Result:     publicReadMediaToolResult(output),
		}, uiMetadata.metadata(call.ToolCallID), nil
	}
	return sdk.ToolResultPart{}, nil, fmt.Errorf("tool %q not found", call.ToolName)
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

// runStream lives in loop_stream.go: the Memoh-owned streaming step loop
// that replaced the SDK's StreamText compatibility loop. Final-boundary
// NextInputs continue on the next inner-loop iteration.

// logContextLifecycle emits the one-line audit summary linking the context
// view manifest to the final provider input: what was selected, what the
// cache plan pinned, and which mutations ran after the view.
func (a *Agent) logContextLifecycle(cfg RunConfig) {
	if a == nil || a.logger == nil || cfg.ContextMutations == nil {
		return
	}
	cacheOutcome := ""
	if cfg.ContextLifecycle != nil {
		if snapshot, ok := cfg.ContextLifecycle.Snapshot(); ok && snapshot.CacheComparison != nil {
			cacheOutcome = snapshot.CacheComparison.Outcome
		}
	}
	a.logger.Debug("context lifecycle",
		slog.String("view", string(cfg.ContextManifest.View)),
		slog.Int("manifest_items", len(cfg.ContextManifest.Items)),
		slog.String("stable_prefix_hash", cfg.ContextCachePlan.StablePrefixHash),
		slog.Int("mutations", len(cfg.ContextMutations.Records())),
		slog.String("cache_outcome", cacheOutcome),
		slog.String("final_input_hash", cfg.ContextMutations.FinalInputHash()),
	)
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

// runGenerate lives in loop_generate.go: the Memoh-owned generate step loop
// that replaced the SDK's GenerateTextResult compatibility loop.

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
	systemPrepended bool,
	prefixCount,
	stepIndex int,
	dynamicRefs []dynamicSourceRef,
	params *sdk.Request,
) *sdk.Request {
	if params == nil {
		return nil
	}
	prefixCount = clampStableMessageCount(prefixCount, len(params.Messages))
	dynamicRefs = verifyDynamicRefs(cfg.dynamicInputs, dynamicRefs, params.Messages)
	snapshot := contextfrag.StepSnapshot{StepIndex: stepIndex}
	// The allowance is what the provider counts; an armed run measures the
	// request by its estimate, calibrated by what the provider counted for
	// the run's or the session's latest step.
	allowance := cfg.contextSwitch.calibrated(providerInputAllowance(cfg))
	// A step request only grows between compaction events. A request that
	// still does not fit after compaction ends the run without pruning.
	if cfg.contextSwitch != nil {
		switched, err := cfg.contextSwitch.fold(ctx, cfg, params, allowance)
		switch {
		case ctx.Err() != nil:
			handoff.reject()
			return params
		case err != nil:
			return failPreparedProviderAttempt(cfg, handoff, params, snapshot, prefixCount, err)
		case switched:
			dynamicRefs = verifyDynamicRefs(cfg.dynamicInputs, cfg.contextSwitch.remap(dynamicRefs), params.Messages)
		}
	}
	if overflow := providerAttemptEnvelopeOverflow(params, allowance); overflow > 0 {
		return failPreparedProviderAttempt(cfg, handoff, params, snapshot, prefixCount, fmt.Errorf(
			"%w: input_overflow=%d allowance=%d",
			contextfrag.ErrBudgetUnsatisfied,
			overflow,
			allowance,
		))
	}
	cfg.contextSwitch.staged(params)
	stagePreparedProviderAttempt(ctx, handoff, snapshot, systemPrepended, dynamicRefs)
	return params
}

func stagePreparedProviderAttempt(
	ctx context.Context,
	handoff *providerAttemptHandoff,
	snapshot contextfrag.StepSnapshot,
	systemPrepended bool,
	dynamicRefs []dynamicSourceRef,
) {
	if !providerAttemptDispatchAllowed(ctx) {
		handoff.reject()
		return
	}
	handoff.stage(snapshot, systemPrepended, dynamicRefs)
}

func providerAttemptEnvelopeOverflow(params *sdk.Request, allowance int) int {
	if params == nil || allowance <= 0 {
		return 0
	}
	return contextfrag.ProviderEnvelopeTokens(params.System, params.Messages, params.Tools) - allowance
}

func failPreparedProviderAttempt(
	cfg RunConfig,
	handoff *providerAttemptHandoff,
	params *sdk.Request,
	snapshot contextfrag.StepSnapshot,
	prefixCount int,
	err error,
) *sdk.Request {
	params.Messages = append([]sdk.Message(nil), params.Messages[:prefixCount]...)
	handoff.reject()
	cfg.ContextMutations.AppendStepSnapshot(snapshot)
	if cfg.contextStepFailure != nil {
		cfg.contextStepFailure(err)
	}
	return params
}

// providerWindow is the context window every provider dispatch of the run
// is sized for and the output it reserves, whether the plan recorded them or
// the run assembled its context without a plan.
func providerWindow(cfg RunConfig) (window, reserve int) {
	window = cfg.ContextBudgetMaxTokens
	if plan := cfg.ContextManifest.BudgetPlan; plan != nil {
		return plan.Window, plan.OutputReserve
	}
	if window > 0 {
		reserve = cfg.GenerationLimits().MaxOutputTokens
	}
	return window, reserve
}

// providerInputAllowance is the input allowance every provider dispatch is
// checked against: the window minus the output reserve, whether the plan
// recorded it or the run assembled its context without a plan.
func providerInputAllowance(cfg RunConfig) int {
	window, reserve := providerWindow(cfg)
	if window <= 0 {
		return 0
	}
	return max(window-reserve, 1)
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

func recordContextCacheUsage(ledger *contextfrag.MutationLedger, stepIndex int, record *step.Record) {
	if ledger == nil || record == nil {
		return
	}
	detail := record.Result.Usage.InputTokenDetails
	if record.Result.Usage.CachedInputTokens == 0 && detail.NoCacheTokens == 0 && detail.CacheReadTokens == 0 &&
		detail.CacheWriteTokens == 0 && detail.CacheWrite5mTokens == 0 && detail.CacheWrite1hTokens == 0 {
		return
	}
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{
		StepIndex: stepIndex, NoCacheTokens: detail.NoCacheTokens, CacheReadTokens: detail.CacheReadTokens,
		CacheWriteTokens: detail.CacheWriteTokens, CacheWrite5mTokens: detail.CacheWrite5mTokens,
		CacheWrite1hTokens: detail.CacheWrite1hTokens,
	})
}

func aggregateStepUsage(steps []step.Record) sdk.Usage {
	var total sdk.Usage
	for _, record := range steps {
		total.InputTokens += record.Result.Usage.InputTokens
		total.OutputTokens += record.Result.Usage.OutputTokens
		total.TotalTokens += record.Result.Usage.TotalTokens
		total.ReasoningTokens += record.Result.Usage.ReasoningTokens
		total.CachedInputTokens += record.Result.Usage.CachedInputTokens
		total.InputTokenDetails.NoCacheTokens += record.Result.Usage.InputTokenDetails.NoCacheTokens
		total.InputTokenDetails.CacheReadTokens += record.Result.Usage.InputTokenDetails.CacheReadTokens
		total.InputTokenDetails.CacheWriteTokens += record.Result.Usage.InputTokenDetails.CacheWriteTokens
		total.InputTokenDetails.CacheWrite5mTokens += record.Result.Usage.InputTokenDetails.CacheWrite5mTokens
		total.InputTokenDetails.CacheWrite1hTokens += record.Result.Usage.InputTokenDetails.CacheWrite1hTokens
		total.OutputTokenDetails.TextTokens += record.Result.Usage.OutputTokenDetails.TextTokens
		total.OutputTokenDetails.ReasoningTokens += record.Result.Usage.OutputTokenDetails.ReasoningTokens
	}
	return total
}

// assembledTools is one session's tool set. tools holds every executable
// tool, on-demand ones included, so the execution chain wraps them alike;
// bridge (nil when nothing loads on demand) keeps the on-demand ones out of
// the request once that chain is built. usage is the guidance the system
// prompt carries, usageFrags its fragments, and defs the definitions the
// request carries.
type assembledTools struct {
	tools      []toolexec.Tool
	usage      string
	usageFrags []contextfrag.ContextFrag
	defs       []contextfrag.ToolDefAccounting
	bridge     *tools.ToolBridge
}

// assembleTools collects tools from all registered ToolProviders, along with
// the group-level usage guidance contributed by providers that also implement
// tools.ToolUsage. Usage guidance is gathered only from providers that actually
// returned tools for this session, so it stays in lockstep with registration
// (see tools.ToolUsage). emitter is injected into the session context so that
// tools targeting the current conversation can push side-effect events
// (attachments, reactions, speech) directly into the agent stream.
//
// Rarely used tool groups load on demand (see tools.ToolBridge): their
// definitions leave the request, and the guidance of a provider whose tools
// all load on demand moves with them into tool_describe's result.
func (a *Agent) assembleTools(
	ctx context.Context,
	cfg RunConfig,
	emitter tools.StreamEmitter,
	liveStream bool,
) (assembledTools, error) {
	if len(a.toolProviders) == 0 {
		return assembledTools{}, nil
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
		CapabilitiesChanged: func() {
			if cfg.capabilityChanges != nil {
				cfg.capabilityChanges.Store(true)
			}
		},
		BotID:                    cfg.Identity.BotID,
		ChatID:                   cfg.Identity.ChatID,
		SessionID:                cfg.Identity.SessionID,
		SessionType:              cfg.SessionType,
		UserID:                   cfg.Identity.UserID,
		ChannelIdentityID:        cfg.Identity.ChannelIdentityID,
		SessionToken:             cfg.Identity.SessionToken,
		WorkspaceTargetID:        cfg.Identity.WorkspaceTargetID,
		WorkspaceTargetKind:      cfg.Identity.WorkspaceTargetKind,
		WorkspaceTargetName:      cfg.Identity.WorkspaceTargetName,
		WorkdirPath:              cfg.Identity.WorkdirPath,
		CurrentPlatform:          cfg.Identity.CurrentPlatform,
		ReplyTarget:              cfg.Identity.ReplyTarget,
		ConversationType:         cfg.Identity.ConversationType,
		CanRequestUserInput:      cfg.CanRequestUserInput,
		SupportsImageInput:       cfg.SupportsImageInput,
		SupportsFileInput:        cfg.SupportsFileInput,
		IsSubagent:               cfg.Identity.IsSubagent,
		CurrentModelUUID:         cfg.CurrentModelUUID,
		CurrentModelID:           cfg.CurrentModelID,
		CurrentModelProvider:     cfg.CurrentModelProvider,
		CurrentModelProviderID:   cfg.CurrentModelProviderID,
		ReasoningStoredEffort:    cfg.ReasoningStoredEffort,
		ReasoningRequestedEffort: cfg.ReasoningRequestedEffort,
		ForkContext:              cfg.ForkContext,
		Skills:                   skillsMap,
		TimezoneLocation:         cfg.Identity.TimezoneLocation,
		Emitter:                  emitter,
		LiveStream:               liveStream,
		ContextBudgetMaxTokens:   cfg.ContextBudgetMaxTokens,
	}

	var allTools []toolexec.Tool
	var toolDefs []contextfrag.ToolDefAccounting
	type usageRegistration struct {
		provider   tools.ToolUsage
		capability string
		toolNames  []string
	}
	var usageRegistrations []usageRegistration
	seenToolNames := make(map[string]struct{})
	for _, provider := range a.toolProviders {
		providerTools, err := provider.Tools(ctx, session)
		if err != nil {
			a.logger.WarnContext(ctx, "tool provider failed", slog.Any("error", err))
			continue
		}
		if session.IsSubagent {
			providerTools = tools.FilterSubagentTools(providerTools)
		}
		uniqueTools := make([]toolexec.Tool, 0, len(providerTools))
		for _, tool := range providerTools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			if _, exists := seenToolNames[name]; exists {
				a.logger.WarnContext(ctx, "duplicate tool name skipped", slog.String("tool", name))
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
			names := make([]string, 0, len(providerTools))
			for _, tool := range providerTools {
				names = append(names, tool.Name)
			}
			usageRegistrations = append(usageRegistrations, usageRegistration{
				provider:   usageProvider,
				capability: firstToolName(providerTools),
				toolNames:  names,
			})
		}
	}
	if cfg.ToolApprovalHandler != nil || a.hookService != nil {
		allTools = markApprovalTools(allTools)
	}
	var bridge *tools.ToolBridge
	if a.onDemandToolsEnabled() {
		bridge = tools.PlanToolBridge(allTools)
	}
	if bridge != nil {
		bridgeTools := bridge.Tools()
		allTools = append(allTools, bridgeTools...)
		wireDefs := make([]contextfrag.ToolDefAccounting, 0, len(toolDefs)+len(bridgeTools))
		for _, def := range toolDefs {
			if !bridge.Defers(def.Name) {
				wireDefs = append(wireDefs, def)
			}
		}
		for _, tool := range bridgeTools {
			wireDefs = append(wireDefs, contextfrag.ToolDefAccountingFor("native", tool))
		}
		toolDefs = wireDefs
	}
	availableTools := tools.NewAvailableTools(allTools)
	var usageSections []toolUsageSection
	for _, registration := range usageRegistrations {
		if text := strings.TrimSpace(registration.provider.Usage(ctx, session, availableTools)); text != "" {
			if bridge.DefersAll(registration.toolNames) {
				bridge.AddUsage(registration.toolNames[0], text)
				continue
			}
			usageSections = append(usageSections, toolUsageSection{
				capability: registration.capability,
				text:       text,
			})
		}
	}
	// Providers that could not register their tools still get to say so:
	// a silent gap reads as "no such tool" to the model.
	for i, provider := range a.toolProviders {
		noticer, ok := provider.(tools.ToolNotice)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(noticer.ToolNotice(ctx, session)); text != "" {
			usageSections = append(usageSections, toolUsageSection{
				id:   toolNoticeSectionID(provider, i),
				text: text,
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
	return assembledTools{
		//nolint:contextcheck // Each tool call reads its own context from its ToolExecContext, not from this assembly.
		tools:      wrapToolTracing(allTools),
		usage:      usage,
		usageFrags: structuredToolUsage(usageSections, cfg.ContextScope),
		defs:       toolDefs,
		bridge:     bridge,
	}, nil
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

func markApprovalTools(sdkTools []toolexec.Tool) []toolexec.Tool {
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

func annotateDeferredApproval(messages []sdk.Message, approval toolexec.ToolApprovalResult) []sdk.Message {
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
			if isUserInputMetadata(approval.Metadata) {
				call.ProviderMetadata = partmeta.Set(call.ProviderMetadata, partmeta.KeyUserInput, map[string]any{
					"user_input_id": approval.ApprovalID,
					"short_id":      approvalShortID(approval.Metadata),
					"status":        "pending",
					"ui_payload":    approval.Metadata["ui_payload"],
				})
			} else {
				call.ProviderMetadata = partmeta.Set(call.ProviderMetadata, partmeta.KeyApproval, map[string]any{
					"approval_id": approval.ApprovalID,
					"short_id":    approvalShortID(approval.Metadata),
					"status":      "pending",
					"can_approve": true,
					"operation":   approval.Metadata["operation"],
				})
			}
			// The message copy still shares its Content slice with the
			// caller's committed step records; write the annotation to a
			// fresh slice so those records stay as persisted.
			content := make([]sdk.MessagePart, len(annotated[msgIdx].Content))
			copy(content, annotated[msgIdx].Content)
			content[partIdx] = call
			annotated[msgIdx].Content = content
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

// toolStreamEventToAgentEvent converts a tool-layer ToolStreamEvent into an
// agent-layer StreamEvent suitable for the output channel.
func toolStreamEventToAgentEvent(evt tools.ToolStreamEvent) StreamEvent {
	switch evt.Type {
	case tools.StreamEventToolApproval:
		if evt.Approval == nil {
			return StreamEvent{}
		}
		req := evt.Approval
		return StreamEvent{Type: EventToolApprovalRequest, InlineDecision: true, ToolCallID: req.ToolCallID, ToolName: req.ToolName, Input: req.ToolInput, ApprovalID: req.ID, ShortID: req.ShortID, Status: req.Status, Metadata: map[string]any{"approval": toolapproval.RequestMetadata(*req)}}
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

func wrapToolsWithLoopGuard(tools []toolexec.Tool, guard *ToolLoopGuard, abortCallIDs *toolAbortRegistry) []toolexec.Tool {
	wrapped := make([]toolexec.Tool, len(tools))
	for i, tool := range tools {
		originalExecute := tool.Execute
		toolName := tool.Name
		wrapped[i] = tool
		wrapped[i].Execute = func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
			warn, abort := guard.Guard(toolName, toolexec.ArgumentsValue(input))
			if abort {
				abortCallIDs.Add(ctx.ToolCallID)
				return toolexec.OutputFromValue(map[string]any{
					"isError": true,
					"content": []map[string]any{{
						"type": "text",
						"text": ToolLoopDetectedAbortMessage,
					}},
				}), ErrToolLoopDetected
			}
			if warn {
				return toolexec.OutputFromValue(map[string]any{
					ToolLoopWarningKey: true,
					"content": []map[string]any{{
						"type": "text",
						"text": ToolLoopWarningText,
					}},
				}), nil
			}
			return originalExecute(ctx, input)
		}
	}
	return wrapped
}

func prepareMidStreamRetryConfigWithMessages(
	cfg RunConfig,
	messages []sdk.Message,
	dynamicRefs []dynamicSourceRef,
	accumulatedCount int,
	errMsg string,
) RunConfig {
	cfg.Messages = append([]sdk.Message(nil), messages...)
	cfg.retryDynamicRefs = cloneDynamicSourceRefs(dynamicRefs)
	attempt := cfg.ContextMutations.AdvanceAttempt()
	errorHash := sha256.Sum256([]byte(strings.TrimSpace(errMsg)))
	cfg.ContextMutations.Record(contextfrag.MutationMidStreamRetry,
		fmt.Sprintf("attempt=%d accumulated=%d error_sha256=%x", attempt, accumulatedCount, errorHash))
	return cfg
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

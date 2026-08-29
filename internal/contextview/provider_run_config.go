package contextview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

const (
	sourceFragsCollectorName = "source_frags"
	toolUsageConflictKey     = "system.tool_usage"
)

func ProviderRunConfigApplier(logger *slog.Logger) agentpkg.ContextViewApplier {
	return func(ctx context.Context, cfg agentpkg.RunConfig) (agentpkg.RunConfig, error) {
		return applyProviderRunConfig(ctx, logger, cfg)
	}
}

// CollectProviderSourceFrags derives typed fragments from already-materialized
// provider fields. It is the legacy fallback; application code normally
// supplies typed system sections directly.
func CollectProviderSourceFrags(ctx context.Context, cfg agentpkg.RunConfig) []contextfrag.ContextFrag {
	system, err := (&SystemPromptCollector{}).Collect(ctx, CollectRequest{
		Scope:  cfg.ContextScope,
		Intent: contextfrag.IntentRunConfigPreProvider,
		Config: SystemPromptConfig{System: cfg.System, ToolUsage: cfg.ContextToolUsage, SplitWorkspace: true},
	})
	if err != nil {
		return nil
	}
	nonSystem := CollectNonSystemProviderSourceFrags(ctx, cfg)
	if nonSystem == nil {
		return nil
	}
	frags := make([]contextfrag.ContextFrag, 0, len(system)+len(nonSystem))
	frags = append(frags, system...)
	frags = append(frags, nonSystem...)
	return frags
}

// CollectNonSystemProviderSourceFrags preserves the exact SDK message order.
// A marked current message keeps its original index even though it receives a
// current-user slot, and raw Query/InlineImages are collected only when the
// caller has not materialized them yet.
func CollectNonSystemProviderSourceFrags(ctx context.Context, cfg agentpkg.RunConfig) []contextfrag.ContextFrag {
	historyConfig := HistoryMessagesConfig{
		Messages:                cfg.Messages,
		CurrentUserMessageIndex: cfg.ContextCurrentUserMessageIndex,
		MemoryMessageIndex:      cfg.ContextMemoryMessageIndex,
		TokenEstimates:          cfg.ContextHistoryTokenEstimates,
		TrimmablePrefix:         cfg.ContextTrimmableMessages,
		RepairToolClosures:      true,
	}
	history, err := (&HistoryMessagesCollector{}).Collect(ctx, CollectRequest{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider, Config: historyConfig,
	})
	if err != nil {
		return nil
	}
	current, err := (&materializedCurrentUserCollector{}).Collect(ctx, CollectRequest{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider, Config: historyConfig,
	})
	if err != nil {
		return nil
	}
	hook, err := (&HookContextCollector{}).Collect(ctx, CollectRequest{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider,
		Config: HookContextConfig{Text: cfg.ContextHookText},
	})
	if err != nil {
		return nil
	}
	var memory []contextfrag.ContextFrag
	if index, ok := markedMemoryMessageIndex(cfg.Messages, cfg.ContextMemoryMessageIndex); ok {
		message := cfg.Messages[index]
		memory, err = (&MemoryContextCollector{}).Collect(ctx, CollectRequest{
			Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider,
			Config: MemoryContextConfig{Message: &message, Index: index},
		})
		if err != nil {
			return nil
		}
	}
	messages := make([]contextfrag.ContextFrag, 0, len(history)+len(memory)+len(current))
	messages = append(messages, history...)
	messages = append(messages, memory...)
	messages = append(messages, current...)
	messages = preserveExplicitHistoryMetadata(messages, cfg)
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].Provenance.Index < messages[j].Provenance.Index
	})
	messages = insertContextFragsBeforeCurrent(messages, hook)

	if cfg.ContextQueryMaterialized {
		return messages
	}
	rawCurrent, err := (&CurrentUserCollector{}).Collect(ctx, CollectRequest{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider, Config: CurrentUserConfig{Query: cfg.Query},
	})
	if err != nil {
		return nil
	}
	images, err := (&InlineImageCollector{}).Collect(ctx, CollectRequest{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider, Config: InlineImageConfig{Images: cfg.InlineImages},
	})
	if err != nil {
		return nil
	}
	messages = append(messages, rawCurrent...)
	messages = append(messages, images...)
	return messages
}

func insertContextFragsBeforeCurrent(frags, dynamic []contextfrag.ContextFrag) []contextfrag.ContextFrag {
	if len(dynamic) == 0 {
		return frags
	}
	history := make([]contextfrag.ContextFrag, 0, len(frags))
	current := make([]contextfrag.ContextFrag, 0, 1)
	for _, frag := range frags {
		if frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			current = append(current, frag)
			continue
		}
		history = append(history, frag)
	}
	out := make([]contextfrag.ContextFrag, 0, len(frags)+len(dynamic))
	out = append(out, history...)
	out = append(out, dynamic...)
	out = append(out, current...)
	return out
}

func preserveExplicitHistoryMetadata(collected []contextfrag.ContextFrag, cfg agentpkg.RunConfig) []contextfrag.ContextFrag {
	if len(collected) == 0 || len(cfg.ContextFrags) == 0 || len(cfg.Messages) == 0 {
		return collected
	}
	memoryIndex, hasMemory := markedMemoryMessageIndex(cfg.Messages, cfg.ContextMemoryMessageIndex)
	currentIndex, hasCurrent := markedCurrentUserMessageIndex(cfg.Messages, cfg.ContextCurrentUserMessageIndex, optionalIndex(memoryIndex, hasMemory)...)
	overrides := make(map[int]contextfrag.ContextFrag)
	for _, candidate := range cfg.ContextFrags {
		index := candidate.Provenance.Index
		if candidate.Slot != contextfrag.SlotHistory || index < 0 || index >= len(cfg.Messages) {
			continue
		}
		if hasCurrent && index == currentIndex {
			continue
		}
		if candidate.ID != messageFragmentID(index) || !hasExplicitHistoryMetadata(candidate) {
			continue
		}
		message, ok := singleSDKMessage(candidate)
		if !ok || !reflect.DeepEqual(message, cfg.Messages[index]) {
			continue
		}
		overrides[index] = candidate
	}
	if len(overrides) == 0 {
		return collected
	}
	for i, frag := range collected {
		index := frag.Provenance.Index
		if candidate, ok := overrides[index]; ok && frag.ID == messageFragmentID(index) {
			frag.Kind = contextfrag.KindConversationSummary
			frag.Ref = candidate.Ref
			frag.Coverage = candidate.Coverage
			frag.Provenance.Source = candidate.Provenance.Source
			frag.Provenance.SourceID = candidate.Provenance.SourceID
			frag.Provenance.Collector = candidate.Provenance.Collector
			collected[i] = frag
		}
	}
	return collected
}

func messageFragmentID(index int) string {
	return fmt.Sprintf("message.%03d", index)
}

func hasExplicitHistoryMetadata(frag contextfrag.ContextFrag) bool {
	return frag.Kind == contextfrag.KindConversationSummary && frag.Coverage != nil && frag.Ref.Durability == contextfrag.RefDurable
}

func singleSDKMessage(frag contextfrag.ContextFrag) (sdk.Message, bool) {
	var message *sdk.Message
	for _, part := range frag.Parts {
		if part.Type != contextfrag.PartSDKMessage {
			continue
		}
		candidate := part.SDKMessage
		if candidate == nil {
			candidate = part.Message
		}
		if candidate == nil || message != nil {
			return sdk.Message{}, false
		}
		message = candidate
	}
	if message == nil {
		return sdk.Message{}, false
	}
	return *message, true
}

func ToolUsageFrag(usage string, scope contextfrag.Scope) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "system.tool_usage", Kind: contextfrag.KindToolUsage, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: usage, Priority: 45, RetentionTier: contextfrag.RetentionPreferred,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustSystem, Scope: scope, Source: contextfrag.SourceAgentToolUsage,
		Collector: sourceFragsCollectorName, Render: contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		ConflictKey: toolUsageConflictKey,
	})
}

const DefaultRecentProtectTokens = 20000

func resolveRecentProtectTokens(override *int) int {
	if override == nil {
		return DefaultRecentProtectTokens
	}
	if *override < 0 {
		return 0
	}
	return *override
}

func providerContextBudgetPlan(ctx context.Context, cfg agentpkg.RunConfig) (*contextfrag.ContextBudgetPlan, error) {
	if cfg.ContextBudgetMaxTokens == 0 {
		return nil, nil
	}
	currentRequestCost, err := providerCurrentRequestCost(ctx, cfg)
	if err != nil {
		return nil, err
	}
	limits := cfg.GenerationLimits()
	plan, err := ComputeContextBudgetPlan(
		cfg.ContextBudgetMaxTokens,
		limits.MaxOutputTokens,
		providerToolDefsCost(cfg.ContextToolDefs),
		currentRequestCost,
	)
	if plan != nil {
		plan.OutputReserveResolution = limits.Resolution
	}
	return plan, err
}

func providerCurrentRequestCost(ctx context.Context, cfg agentpkg.RunConfig) (int, error) {
	if len(cfg.ContextSourceFrags) > 0 {
		return currentRequestFragCost(cfg.ContextSourceFrags), nil
	}
	query := cfg.Query
	inlineImages := cfg.InlineImages
	if cfg.ContextQueryMaterialized {
		query = ""
		inlineImages = nil
	}
	historyConfig := HistoryMessagesConfig{
		Messages:                cfg.Messages,
		CurrentUserMessageIndex: cfg.ContextCurrentUserMessageIndex,
		MemoryMessageIndex:      cfg.ContextMemoryMessageIndex,
		TokenEstimates:          cfg.ContextHistoryTokenEstimates,
	}
	specs := []struct {
		collector Collector
		config    any
	}{
		{&materializedCurrentUserCollector{}, historyConfig},
		{&CurrentUserCollector{}, CurrentUserConfig{Query: query}},
		{&InlineImageCollector{}, InlineImageConfig{Images: inlineImages}},
	}
	var frags []contextfrag.ContextFrag
	for _, spec := range specs {
		collected, err := spec.collector.Collect(ctx, CollectRequest{
			Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider, Config: spec.config,
		})
		if err != nil {
			return 0, err
		}
		frags = append(frags, collected...)
	}
	return currentRequestFragCost(frags), nil
}

func currentRequestFragCost(frags []contextfrag.ContextFrag) int {
	total := 0
	for _, frag := range frags {
		if frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			total += contextfrag.ResolveProviderBudgetFragTokens(frag)
		}
	}
	return total
}

// ApplyProviderRunConfig keeps the direct compiler helper source-compatible.
// Native execution uses ProviderRunConfigApplier so typed budget failures are
// returned to the runtime and fail closed before any provider call.
func ApplyProviderRunConfig(ctx context.Context, logger *slog.Logger, cfg agentpkg.RunConfig) agentpkg.RunConfig {
	out, _ := applyProviderRunConfig(ctx, logger, cfg)
	return out
}

func applyProviderRunConfig(ctx context.Context, logger *slog.Logger, cfg agentpkg.RunConfig) (agentpkg.RunConfig, error) {
	ledger := cfg.ContextMutations
	if ledger == nil {
		ledger = contextfrag.NewMutationLedger()
	}

	fragsFirst := len(cfg.ContextSourceFrags) > 0
	var providerSourceFrags []contextfrag.ContextFrag
	var frags []contextfrag.ContextFrag
	if fragsFirst {
		frags = append([]contextfrag.ContextFrag(nil), cfg.ContextSourceFrags...)
		if len(cfg.ContextToolUsageFrags) > 0 {
			filtered := frags[:0]
			for _, frag := range frags {
				if frag.Kind != contextfrag.KindToolUsage {
					filtered = append(filtered, frag)
				}
			}
			filtered = append(filtered, cfg.ContextToolUsageFrags...)
			frags = filtered
		} else if usage := strings.TrimSpace(cfg.ContextToolUsage); usage != "" {
			frags = append(frags, ToolUsageFrag(usage, cfg.ContextScope))
		}
		providerSourceFrags = frags
	} else {
		frags = CollectProviderSourceFrags(ctx, cfg)
		if frags == nil {
			return providerViewFallback(logger, cfg, ledger, nil, "collect_error", "context view collection failed", nil), nil
		}
		cfg = materializeLegacyQuery(cfg, false)
	}
	budgetPlan, budgetErr := providerContextBudgetPlan(ctx, cfg)
	if cfg.ContextBudgetMaxTokens == 0 {
		recordMutationOnce(ledger, contextfrag.MutationContextBudgetDisabled, "missing_context_window")
		warnMissingContextWindow(ctx, logger, cfg)
	}

	selector := Selector(&FragmentSelector{})
	fallbackCfg := cfg
	if fragsFirst && cfg.ContextToolDefsResolved {
		gate := newCapabilityGateSelector(selector, cfg.ContextToolDefs)
		_, gated := filterUnavailableCapabilities(providerSourceFrags, gate.available)
		if len(gated) > 0 {
			ledger.Record(contextfrag.MutationCapabilityGate, fmt.Sprintf("dropped=%d", len(gated)))
			fallbackCfg = capabilitySafeFallbackConfig(cfg, providerSourceFrags, gate.available)
		}
		selector = gate
	}
	if budgetErr != nil && !isContextBudgetError(budgetErr) {
		return providerViewFallback(logger, fallbackCfg, ledger, budgetPlan, "budget_plan_error", "context budget plan failed", budgetErr), nil
	}

	registry := NewMapCollectorRegistry(StaticCollector{CollectorName: sourceFragsCollectorName, Frags: frags})
	builder := NewBuilder(registry, selector, StablePrefixPlacer{}, NewMapRendererRegistry(&SDKMessagesRenderer{}))
	view, err := builder.Build(ctx, BuildInput{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: sourceFragsCollectorName}},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
		Budget: BudgetEnvelope{
			MaxTokens:           cfg.ContextBudgetMaxTokens,
			Plan:                budgetPlan,
			RecentProtectTokens: resolveRecentProtectTokens(cfg.ContextRecentProtectTokens),
			ToolExchange:        cfg.ContextToolExchangePolicy,
		},
		DynamicMutators: cfg.ContextDynamicMutators,
	})
	if budgetErr != nil {
		recordContextBudgetFailure(ledger, budgetErr)
		return providerBudgetAuditConfig(cfg, view, ledger, budgetPlan), budgetErr
	}
	if err != nil {
		if isContextBudgetError(err) {
			recordContextBudgetFailure(ledger, err)
			return providerBudgetAuditConfig(cfg, view, ledger, budgetPlan), err
		}
		return providerViewFallback(logger, fallbackCfg, ledger, budgetPlan, "build_error", "context view build failed", err), nil
	}
	payload, ok := view.Rendered[contextfrag.RenderSDKMessages].Data.(*SDKRenderedPayload)
	if !ok {
		return providerViewFallback(logger, fallbackCfg, ledger, budgetPlan, "unexpected_payload", "context view rendered unexpected payload", nil), nil
	}
	if err := validateProviderRenderedEnvelope(payload, cfg.ContextToolDefs, budgetPlan); err != nil {
		recordContextBudgetFailure(ledger, err)
		return providerBudgetAuditConfig(cfg, view, ledger, budgetPlan), err
	}

	plan := cachePlanFromPlacement(view.Placement)
	plan.StablePrefixTokenEstimate = stablePrefixTokenEstimate(view.Placement, view.Selected, cfg.ContextToolDefs)
	plan.MidStableMessageCount = midStableMessageCount(view.Placement, view.Selected)
	manifest := view.Manifest
	manifest.CachePlan = &plan
	manifest.Mutations = ledger
	manifest.ToolDefs = append([]contextfrag.ToolDefAccounting(nil), cfg.ContextToolDefs...)
	appendContextSourceWarnings(&manifest, cfg.ContextSourceWarnings)

	cfg.System = payload.System
	cfg.Messages = payload.Messages
	ordered, err := orderedSelectedFrags(view.Selected, view.Placement)
	if err != nil {
		return providerViewFallback(logger, fallbackCfg, ledger, budgetPlan, "placement_error", "context view placement invalid after render", err), nil
	}
	currentIndex, memoryIndex := renderedSpecialMessageIndexes(ordered)
	cfg.ContextMemoryMessageIndex = memoryIndex
	switch {
	case currentIndex != nil:
		cfg.ContextCurrentUserMessageIndex = currentIndex
	case hasCurrentUserFrag(view.Selected):
		cfg.ContextCurrentUserMessageIndex = latestUserMessageIndex(cfg.Messages, optionalPointerValue(memoryIndex)...)
	default:
		cfg.ContextCurrentUserMessageIndex = nil
	}
	cfg.ContextQueryMaterialized = true
	cfg.ContextHookText = ""
	cfg.ContextFrags = view.Selected
	cfg.ContextManifest = manifest
	cfg.ContextCachePlan = plan
	cfg.ContextMutations = ledger
	cfg.ContextStepReselector = SelectProviderStepMessages
	if cfg.ContextLifecycle != nil {
		cfg.ContextLifecycle.SetManifest(manifest)
	}
	return cfg, nil
}

func isContextBudgetError(err error) bool {
	return errors.Is(err, contextfrag.ErrProtectedContextOverflow) ||
		errors.Is(err, contextfrag.ErrBudgetUnsatisfied)
}

func recordContextBudgetFailure(ledger *contextfrag.MutationLedger, err error) {
	reason := "budget_unsatisfied"
	if errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
		reason = "protected_context_overflow"
	}
	recordMutationOnce(ledger, contextfrag.MutationContextBudgetFailure, reason)
}

func recordMutationOnce(ledger *contextfrag.MutationLedger, kind contextfrag.MutationKind, detail string) {
	for _, record := range ledger.Records() {
		if record.Kind == kind && record.Detail == detail {
			return
		}
	}
	ledger.Record(kind, detail)
}

func providerBudgetAuditConfig(
	cfg agentpkg.RunConfig,
	view *ContextView,
	ledger *contextfrag.MutationLedger,
	budgetPlan *contextfrag.ContextBudgetPlan,
) agentpkg.RunConfig {
	if view == nil {
		manifest := contextfrag.BuildManifest(nil)
		manifest.View = contextfrag.ViewRunConfigPreProvider
		manifest.DynamicMutators = normalizeDynamicMutators(cfg.ContextDynamicMutators)
		if budgetPlan != nil {
			plan := *budgetPlan
			manifest.BudgetPlan = &plan
		}
		manifest.Mutations = ledger
		appendContextSourceWarnings(&manifest, cfg.ContextSourceWarnings)
		cfg.ContextManifest = manifest
		cfg.ContextMutations = ledger
		if cfg.ContextLifecycle != nil {
			cfg.ContextLifecycle.SetManifest(manifest)
		}
		return cfg
	}
	cachePlan := cachePlanFromPlacement(view.Placement)
	cachePlan.StablePrefixTokenEstimate = stablePrefixTokenEstimate(view.Placement, view.Selected, cfg.ContextToolDefs)
	cachePlan.MidStableMessageCount = midStableMessageCount(view.Placement, view.Selected)
	manifest := view.Manifest
	manifest.CachePlan = &cachePlan
	manifest.Mutations = ledger
	manifest.ToolDefs = append([]contextfrag.ToolDefAccounting(nil), cfg.ContextToolDefs...)
	appendContextSourceWarnings(&manifest, cfg.ContextSourceWarnings)

	cfg.ContextFrags = view.Selected
	cfg.ContextManifest = manifest
	cfg.ContextCachePlan = cachePlan
	cfg.ContextMutations = ledger
	if cfg.ContextLifecycle != nil {
		cfg.ContextLifecycle.SetManifest(manifest)
	}
	return cfg
}

// providerViewFallback assembles the legacy payload when the context view
// cannot. It is an assembly path, not a budget-disabled path: the budget plan
// and step reselection stay in force so every dispatch still meets the final
// envelope check.
func providerViewFallback(
	logger *slog.Logger,
	cfg agentpkg.RunConfig,
	ledger *contextfrag.MutationLedger,
	budgetPlan *contextfrag.ContextBudgetPlan,
	reason, message string,
	err error,
) agentpkg.RunConfig {
	warnProviderContextView(logger, cfg, message, err)
	priorManifest := cfg.ContextManifest
	cfg = legacyMaterializeQuery(cfg)
	cfg.ContextFrags = nil
	cfg.ContextSourceFrags = nil
	cfg = cfg.RefreshContextFrag()
	mergeCapabilityFallbackAudit(&cfg, priorManifest)
	ledger.Record(contextfrag.MutationContextViewFallback, reason)
	plan := contextfrag.CachePlan{}
	manifest := cfg.ContextManifest
	manifest.CachePlan = &plan
	if budgetPlan != nil {
		fallbackPlan := *budgetPlan
		fallbackPlan.ActualSystemCost = 0
		fallbackPlan.HistoryBudget = 0
		manifest.BudgetPlan = &fallbackPlan
	}
	manifest.Mutations = ledger
	manifest.ToolDefs = append([]contextfrag.ToolDefAccounting(nil), cfg.ContextToolDefs...)
	appendContextSourceWarnings(&manifest, cfg.ContextSourceWarnings)
	cfg.ContextManifest = manifest
	cfg.ContextCachePlan = plan
	cfg.ContextMutations = ledger
	cfg.ContextStepReselector = SelectProviderStepMessages
	if cfg.ContextLifecycle != nil {
		cfg.ContextLifecycle.SetManifest(manifest)
	}
	return cfg
}

func appendContextSourceWarnings(
	manifest *contextfrag.Manifest,
	warnings []contextfrag.ValidationWarning,
) {
	if manifest == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		warning.Ref = finalContextSourceWarningRef(*manifest, warning.Ref)
		manifest.ValidationWarnings = append(manifest.ValidationWarnings, warning)
	}
}

func finalContextSourceWarningRef(
	manifest contextfrag.Manifest,
	ref contextfrag.ContextRef,
) contextfrag.ContextRef {
	if strings.TrimSpace(ref.ID) == "" {
		return ref
	}
	for _, item := range manifest.Items {
		if sameContextRefIdentity(item.Ref, ref) {
			return item.Ref
		}
	}
	for _, decision := range manifest.SelectionDecisions {
		if sameContextRefIdentity(decision.Ref, ref) {
			return decision.Ref
		}
	}
	return ref
}

func sameContextRefIdentity(left, right contextfrag.ContextRef) bool {
	return left.ID == right.ID &&
		(left.Namespace == "" || right.Namespace == "" || left.Namespace == right.Namespace)
}

func mergeCapabilityFallbackAudit(out *agentpkg.RunConfig, prior contextfrag.Manifest) {
	if out == nil || prior.Selection == nil ||
		prior.Selection.DropReasons[capabilityGateDropReason] == 0 {
		return
	}
	gated := make([]contextfrag.SelectionDecision, 0, prior.Selection.Dropped)
	for _, decision := range prior.SelectionDecisions {
		if decision.Decision == contextfrag.DecisionDropped &&
			decision.Reason == capabilityGateDropReason {
			gated = append(gated, decision)
		}
	}
	if len(gated) == 0 {
		return
	}
	decisions := make([]contextfrag.SelectionDecision, 0, len(out.ContextFrags)+len(gated))
	for _, frag := range out.ContextFrags {
		decisions = append(decisions, selectionDecisionForFrag(frag, contextfrag.DecisionSelected, ""))
	}
	decisions = append(decisions, gated...)
	out.ContextManifest.Selection = &contextfrag.SelectionTrace{
		Selected: len(out.ContextFrags),
		Dropped:  len(gated),
		DropReasons: map[string]int{
			capabilityGateDropReason: len(gated),
		},
	}
	out.ContextManifest.SelectionDecisions = decisions
}

func legacyMaterializeQuery(cfg agentpkg.RunConfig) agentpkg.RunConfig {
	return materializeLegacyQuery(cfg, true)
}

func materializeLegacyQuery(cfg agentpkg.RunConfig, includeHookContext bool) agentpkg.RunConfig {
	if usage := strings.TrimSpace(cfg.ContextToolUsage); usage != "" && !strings.Contains(cfg.System, usage) {
		cfg.System = appendToolUsageLegacy(cfg.System, usage)
	}
	cfg.Messages = cloneMessages(cfg.Messages)
	cfg.ForkContextSourceMessageIDs = append([]string(nil), cfg.ForkContextSourceMessageIDs...)
	if includeHookContext {
		cfg = insertFallbackHookContext(cfg)
	}
	if cfg.ContextQueryMaterialized {
		return cfg
	}
	imageParts := make([]sdk.MessagePart, 0, len(cfg.InlineImages))
	for _, image := range cfg.InlineImages {
		if strings.TrimSpace(image.Image) != "" {
			imageParts = append(imageParts, image)
		}
	}
	if cfg.Query != "" {
		cfg.Messages = append(cfg.Messages, sdk.UserMessage(cfg.Query, imageParts...))
		cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, "")
		index := len(cfg.Messages) - 1
		cfg.ContextCurrentUserMessageIndex = &index
		cfg.ContextQueryMaterialized = true
		return cfg
	}
	if len(imageParts) == 0 {
		return cfg
	}
	for i := len(cfg.Messages) - 1; i >= 0; i-- {
		if cfg.Messages[i].Role != sdk.MessageRoleUser {
			continue
		}
		cfg.Messages[i].Content = append(cfg.Messages[i].Content, imageParts...)
		if i < len(cfg.ForkContextSourceMessageIDs) {
			cfg.ForkContextSourceMessageIDs[i] = ""
		}
		cfg.ContextCurrentUserMessageIndex = intPointer(i)
		cfg.ContextQueryMaterialized = true
		return cfg
	}
	cfg.Messages = append(cfg.Messages, sdk.UserMessage("", imageParts...))
	cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, "")
	cfg.ContextCurrentUserMessageIndex = intPointer(len(cfg.Messages) - 1)
	cfg.ContextQueryMaterialized = true
	return cfg
}

func insertFallbackHookContext(cfg agentpkg.RunConfig) agentpkg.RunConfig {
	text := strings.TrimSpace(cfg.ContextHookText)
	cfg.ContextHookText = ""
	if text == "" || utf8.RuneCountInString(text) > maxHookContextChars {
		return cfg
	}

	memoryIndex, hasMemory := markedMemoryMessageIndex(cfg.Messages, cfg.ContextMemoryMessageIndex)
	currentIndex, hasCurrent := markedCurrentUserMessageIndex(
		cfg.Messages,
		cfg.ContextCurrentUserMessageIndex,
		optionalIndex(memoryIndex, hasMemory)...,
	)
	if !hasCurrent {
		currentIndex, hasCurrent = sourceCurrentUserMessageIndex(cfg, memoryIndex, hasMemory)
	}
	if !hasCurrent {
		cfg.Messages = append(cfg.Messages, sdk.UserMessage(text))
		if len(cfg.ForkContextSourceMessageIDs) == len(cfg.Messages)-1 {
			cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, "")
		}
		return cfg
	}

	messageCount := len(cfg.Messages)
	currentMessage := cfg.Messages[currentIndex]
	messages := make([]sdk.Message, 0, messageCount+1)
	messages = append(messages, cfg.Messages[:currentIndex]...)
	messages = append(messages, cfg.Messages[currentIndex+1:]...)
	messages = append(messages, sdk.UserMessage(text), currentMessage)
	cfg.Messages = messages
	if len(cfg.ForkContextSourceMessageIDs) == messageCount {
		currentSourceID := cfg.ForkContextSourceMessageIDs[currentIndex]
		sourceIDs := make([]string, 0, messageCount+1)
		sourceIDs = append(sourceIDs, cfg.ForkContextSourceMessageIDs[:currentIndex]...)
		sourceIDs = append(sourceIDs, cfg.ForkContextSourceMessageIDs[currentIndex+1:]...)
		sourceIDs = append(sourceIDs, "", currentSourceID)
		cfg.ForkContextSourceMessageIDs = sourceIDs
	}
	if len(cfg.ContextHistoryTokenEstimates) == messageCount {
		currentEstimate := cfg.ContextHistoryTokenEstimates[currentIndex]
		estimates := make([]int, 0, messageCount+1)
		estimates = append(estimates, cfg.ContextHistoryTokenEstimates[:currentIndex]...)
		estimates = append(estimates, cfg.ContextHistoryTokenEstimates[currentIndex+1:]...)
		estimates = append(estimates, 0, currentEstimate)
		cfg.ContextHistoryTokenEstimates = estimates
	}
	cfg.ContextCurrentUserMessageIndex = intPointer(messageCount)
	if hasMemory {
		if memoryIndex > currentIndex {
			memoryIndex--
		}
		cfg.ContextMemoryMessageIndex = intPointer(memoryIndex)
	}
	return cfg
}

func sourceCurrentUserMessageIndex(cfg agentpkg.RunConfig, memoryIndex int, hasMemory bool) (int, bool) {
	for i := len(cfg.ContextSourceFrags) - 1; i >= 0; i-- {
		frag := cfg.ContextSourceFrags[i]
		if frag.Kind != contextfrag.KindCurrentUserMessage && frag.Slot != contextfrag.SlotCurrentUser {
			continue
		}
		index := frag.Provenance.Index
		if index < 0 || index >= len(cfg.Messages) || cfg.Messages[index].Role != sdk.MessageRoleUser ||
			hasMemory && index == memoryIndex {
			continue
		}
		message, ok := singleSDKMessage(frag)
		if ok && reflect.DeepEqual(message, cfg.Messages[index]) {
			return index, true
		}
	}
	return 0, false
}

func appendToolUsageLegacy(system, usage string) string {
	system = strings.TrimSpace(system)
	usage = strings.TrimSpace(usage)
	if usage == "" {
		return system
	}
	if system == "" {
		return usage
	}
	if index := strings.Index(system, contextfrag.WorkspaceInstructionAnchor); index >= 0 {
		return strings.TrimSpace(system[:index]) + "\n\n" + usage + "\n" + system[index:]
	}
	return strings.TrimSpace(system + "\n\n" + usage)
}

func cloneMessages(messages []sdk.Message) []sdk.Message {
	if messages == nil {
		return nil
	}
	out := make([]sdk.Message, len(messages))
	for i, message := range messages {
		out[i] = cloneSDKMessage(message)
	}
	return out
}

func cachePlanFromPlacement(placement PlacementPlan) contextfrag.CachePlan {
	stableMessages := 0
	for i, item := range placement.Items {
		if i >= placement.FirstVolatileIndex {
			break
		}
		if item.Slot != contextfrag.SlotSystem {
			stableMessages++
		}
	}
	return contextfrag.CachePlan{StablePrefixHash: placement.StablePrefixHash, StableMessageCount: stableMessages}
}

func stablePrefixTokenEstimate(placement PlacementPlan, selected []contextfrag.ContextFrag, toolDefs []contextfrag.ToolDefAccounting) int {
	total := 0
	for _, definition := range toolDefs {
		total += definition.TokenEstimate
	}
	byID := make(map[string]contextfrag.ContextFrag, len(selected))
	for _, frag := range selected {
		byID[frag.ID] = frag
	}
	for i, item := range placement.Items {
		if i >= placement.FirstVolatileIndex {
			break
		}
		if frag, ok := byID[item.FragID]; ok {
			total += contextfrag.ResolveFragTokens(frag)
		}
	}
	return total
}

const midBreakpointMinSpanTokens = 2048

// midStableMessageCount selects the smallest leading message count holding at
// least half of a large stable span's token mass. Zero avoids a redundant or
// low-value midpoint breakpoint.
func midStableMessageCount(placement PlacementPlan, selected []contextfrag.ContextFrag) int {
	byID := make(map[string]contextfrag.ContextFrag, len(selected))
	for _, frag := range selected {
		byID[frag.ID] = frag
	}
	var perMessage []int
	total := 0
	for i, item := range placement.Items {
		if i >= placement.FirstVolatileIndex {
			break
		}
		if item.Slot == contextfrag.SlotSystem {
			continue
		}
		tokens := 0
		if frag, ok := byID[item.FragID]; ok {
			tokens = contextfrag.ResolveFragTokens(frag)
		}
		perMessage = append(perMessage, tokens)
		total += tokens
	}
	if total < midBreakpointMinSpanTokens {
		return 0
	}
	cumulative := 0
	for i, tokens := range perMessage {
		cumulative += tokens
		if cumulative*2 >= total {
			count := i + 1
			if count >= len(perMessage) {
				return 0
			}
			return count
		}
	}
	return 0
}

func latestUserMessageIndex(messages []sdk.Message, excluded ...int) *int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sdk.MessageRoleUser && !containsIndex(excluded, i) {
			return intPointer(i)
		}
	}
	return nil
}

func renderedSpecialMessageIndexes(frags []contextfrag.ContextFrag) (current, memory *int) {
	messageIndex := 0
	for _, frag := range frags {
		if frag.Slot == contextfrag.SlotSystem {
			continue
		}
		for _, part := range frag.Parts {
			if part.Type != contextfrag.PartSDKMessage || sdkMessagePart(part) == nil {
				continue
			}
			switch {
			case frag.Kind == contextfrag.KindMemoryRecall:
				memory = intPointer(messageIndex)
			case frag.Kind == contextfrag.KindCurrentUserMessage || frag.Slot == contextfrag.SlotCurrentUser:
				current = intPointer(messageIndex)
			}
			messageIndex++
		}
	}
	return current, memory
}

func optionalPointerValue(value *int) []int {
	if value == nil {
		return nil
	}
	return []int{*value}
}

func hasCurrentUserFrag(frags []contextfrag.ContextFrag) bool {
	for _, frag := range frags {
		if frag.Slot == contextfrag.SlotCurrentUser || frag.Kind == contextfrag.KindCurrentUserMessage {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }

func warnProviderContextView(logger *slog.Logger, cfg agentpkg.RunConfig, message string, err error) {
	if logger == nil {
		return
	}
	attrs := []any{slog.String("bot_id", cfg.Identity.BotID), slog.String("session_id", cfg.Identity.SessionID)}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	logger.Warn(message, attrs...) //nolint:sloglint // message names the exact fallback stage
}

func warnMissingContextWindow(ctx context.Context, logger *slog.Logger, cfg agentpkg.RunConfig) {
	if logger == nil {
		return
	}
	logger.WarnContext(ctx, "context budget disabled: missing model context window",
		slog.String("reason", "missing_context_window"),
		slog.String("bot_id", cfg.ContextScope.BotID),
		slog.String("session_id", cfg.ContextScope.SessionID),
		slog.String("model_id", cfg.CurrentModelID),
	)
}

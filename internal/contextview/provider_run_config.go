package contextview

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	agentpkg "github.com/memohai/memoh/internal/agent/runtime/native"
)

const (
	sourceFragsCollectorName = "source_frags"
	toolUsageConflictKey     = "system.tool_usage"
)

func ProviderRunConfigApplier(logger *slog.Logger) agentpkg.ContextViewApplier {
	return func(ctx context.Context, cfg agentpkg.RunConfig) agentpkg.RunConfig {
		return ApplyProviderRunConfig(ctx, logger, cfg)
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
		Slot: contextfrag.SlotSystem, Text: usage, Priority: 45, CacheClass: contextfrag.CacheStable,
		Trust: contextfrag.TrustSystem, Scope: scope, Source: contextfrag.SourceAgentToolUsage,
		Collector: sourceFragsCollectorName, Render: contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		ConflictKey: toolUsageConflictKey,
	})
}

func ApplyProviderRunConfig(ctx context.Context, logger *slog.Logger, cfg agentpkg.RunConfig) agentpkg.RunConfig {
	ledger := cfg.ContextMutations
	if ledger == nil {
		ledger = contextfrag.NewMutationLedger()
	}

	var frags []contextfrag.ContextFrag
	if len(cfg.ContextSourceFrags) > 0 {
		frags = append([]contextfrag.ContextFrag(nil), cfg.ContextSourceFrags...)
		if usage := strings.TrimSpace(cfg.ContextToolUsage); usage != "" {
			frags = append(frags, ToolUsageFrag(usage, cfg.ContextScope))
		}
	} else {
		cfg = legacyMaterializeQuery(cfg)
		frags = CollectProviderSourceFrags(ctx, cfg)
		if frags == nil {
			return providerViewFallback(logger, cfg, ledger, "collect_error", "context view collection failed", nil)
		}
	}

	registry := NewMapCollectorRegistry(StaticCollector{CollectorName: sourceFragsCollectorName, Frags: frags})
	builder := NewBuilder(registry, &FragmentSelector{}, StablePrefixPlacer{}, NewMapRendererRegistry(&SDKMessagesRenderer{}))
	view, err := builder.Build(ctx, BuildInput{
		Scope: cfg.ContextScope, Intent: contextfrag.IntentRunConfigPreProvider,
		Sources:         []SourceSpec{{Name: sourceFragsCollectorName}},
		Targets:         []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
		Budget:          BudgetEnvelope{ToolExchange: cfg.ContextToolExchangePolicy},
		DynamicMutators: cfg.ContextDynamicMutators,
	})
	if err != nil {
		return providerViewFallback(logger, cfg, ledger, "build_error", "context view build failed", err)
	}
	payload, ok := view.Rendered[contextfrag.RenderSDKMessages].Data.(*SDKRenderedPayload)
	if !ok {
		return providerViewFallback(logger, cfg, ledger, "unexpected_payload", "context view rendered unexpected payload", nil)
	}

	plan := cachePlanFromPlacement(view.Placement)
	plan.StablePrefixTokenEstimate = stablePrefixTokenEstimate(view.Placement, view.Selected, cfg.ContextToolDefs)
	manifest := view.Manifest
	manifest.CachePlan = &plan
	manifest.Mutations = ledger
	manifest.ToolDefs = append([]contextfrag.ToolDefAccounting(nil), cfg.ContextToolDefs...)

	cfg.System = payload.System
	cfg.Messages = payload.Messages
	ordered, err := orderedSelectedFrags(view.Selected, view.Placement)
	if err != nil {
		return providerViewFallback(logger, cfg, ledger, "placement_error", "context view placement invalid after render", err)
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
	cfg.ContextFrags = view.Selected
	cfg.ContextManifest = manifest
	cfg.ContextCachePlan = plan
	cfg.ContextMutations = ledger
	return cfg
}

func providerViewFallback(logger *slog.Logger, cfg agentpkg.RunConfig, ledger *contextfrag.MutationLedger, reason, message string, err error) agentpkg.RunConfig {
	warnProviderContextView(logger, cfg, message, err)
	cfg = legacyMaterializeQuery(cfg)
	cfg.ContextFrags = nil
	cfg.ContextSourceFrags = nil
	cfg = cfg.RefreshContextFrag()
	ledger.Record(contextfrag.MutationContextViewFallback, reason)
	plan := contextfrag.CachePlan{}
	manifest := cfg.ContextManifest
	manifest.CachePlan = &plan
	manifest.Mutations = ledger
	manifest.ToolDefs = append([]contextfrag.ToolDefAccounting(nil), cfg.ContextToolDefs...)
	cfg.ContextManifest = manifest
	cfg.ContextCachePlan = plan
	cfg.ContextMutations = ledger
	return cfg
}

func legacyMaterializeQuery(cfg agentpkg.RunConfig) agentpkg.RunConfig {
	if usage := strings.TrimSpace(cfg.ContextToolUsage); usage != "" && !strings.Contains(cfg.System, usage) {
		cfg.System = appendToolUsageLegacy(cfg.System, usage)
	}
	if cfg.ContextQueryMaterialized {
		return cfg
	}
	cfg.Messages = cloneMessages(cfg.Messages)
	cfg.ForkContextSourceMessageIDs = append([]string(nil), cfg.ForkContextSourceMessageIDs...)
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
		if frag.Slot == contextfrag.SlotCurrentUser {
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

package native

import (
	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/models"
)

// RefreshContextFrag rebuilds the typed context frag view from the legacy
// RunConfig fields. The SDK-facing fields remain the source of truth in phase 1.
func (cfg RunConfig) RefreshContextFrag() RunConfig {
	query := cfg.Query
	inlineImages := cfg.InlineImages
	if cfg.ContextQueryMaterialized {
		query = ""
		inlineImages = nil
	}
	assembled := contextfrag.Compile(contextfrag.CompileInput{
		Source:                  contextfrag.SourceRunConfig,
		Scope:                   cfg.ContextScope,
		System:                  cfg.System,
		Messages:                cfg.Messages,
		CurrentUserMessageIndex: cfg.ContextCurrentUserMessageIndex,
		MemoryMessageIndex:      cfg.ContextMemoryMessageIndex,
		Query:                   query,
		InlineImages:            inlineImages,
		ToolUsage:               cfg.ContextToolUsage,
		DynamicMutators:         cfg.ContextDynamicMutators,
		Existing:                cfg.ContextFrags,
	})
	cfg.ContextFrags = assembled.Frags
	manifest := assembled.Manifest
	manifest.ToolDefs = cfg.ContextToolDefs
	manifest = preserveProviderAccounting(cfg.ContextManifest, manifest)
	cfg.ContextManifest = manifest
	if cfg.ContextLifecycle != nil {
		cfg.ContextLifecycle.SetManifest(manifest)
	}
	return cfg
}

func preserveProviderAccounting(previous, next contextfrag.Manifest) contextfrag.Manifest {
	if previous.CachePlan != nil && next.CachePlan == nil {
		plan := *previous.CachePlan
		next.CachePlan = &plan
	}
	if previous.Mutations != nil && next.Mutations == nil {
		next.Mutations = previous.Mutations
	}
	if previous.Selection != nil && next.Selection == nil {
		selection := *previous.Selection
		if len(previous.Selection.DropReasons) > 0 {
			selection.DropReasons = make(map[string]int, len(previous.Selection.DropReasons))
			for reason, count := range previous.Selection.DropReasons {
				selection.DropReasons[reason] = count
			}
		}
		next.Selection = &selection
	}
	if previous.BudgetPlan != nil && next.BudgetPlan == nil {
		plan := *previous.BudgetPlan
		next.BudgetPlan = &plan
	}
	if len(previous.SelectionDecisions) > 0 && len(next.SelectionDecisions) == 0 {
		next.SelectionDecisions = append([]contextfrag.SelectionDecision(nil), previous.SelectionDecisions...)
	}
	return next
}

func (cfg RunConfig) RefreshContextFragWithDynamicMutators(readMedia bool, beforeModelCallHook bool, injectCh bool) RunConfig {
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMedia, beforeModelCallHook, injectCh)
	return cfg.RefreshContextFrag()
}

func (cfg RunConfig) contextDynamicMutators(readMedia bool, beforeModelCallHook bool, injectCh bool) []contextfrag.DynamicMutator {
	var mutators []contextfrag.DynamicMutator
	if cfg.Model != nil &&
		models.ResolveClientType(cfg.Model) == string(models.ClientTypeAnthropicMessages) &&
		models.NormalizePromptCacheTTL(cfg.PromptCacheTTL) != models.PromptCacheTTLOff {
		mutators = append(mutators, contextfrag.DynamicMutatorPromptCache)
	}
	if injectCh && cfg.InjectCh != nil {
		mutators = append(mutators, contextfrag.DynamicMutatorInjectCh)
	}
	if readMedia {
		mutators = append(mutators, contextfrag.DynamicMutatorReadMedia)
	}
	if beforeModelCallHook {
		mutators = append(mutators, contextfrag.DynamicMutatorBeforeModelCallHook)
	}
	if cfg.BackgroundManager != nil {
		mutators = append(mutators, contextfrag.DynamicMutatorBackgroundSummary)
	}
	return mutators
}

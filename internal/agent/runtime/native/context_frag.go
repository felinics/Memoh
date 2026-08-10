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
	if cfg.ContextManifest.CachePlan != nil && manifest.CachePlan == nil {
		plan := *cfg.ContextManifest.CachePlan
		manifest.CachePlan = &plan
	}
	if cfg.ContextManifest.Mutations != nil && manifest.Mutations == nil {
		manifest.Mutations = cfg.ContextManifest.Mutations
	}
	cfg.ContextManifest = manifest
	return cfg
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

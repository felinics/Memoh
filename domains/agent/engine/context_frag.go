package engine

import (
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelexecution "github.com/memohai/memoh/domains/model/execution"
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
	assembled := fragment.Compile(fragment.CompileInput{
		Source:          fragment.SourceRunConfig,
		Scope:           cfg.ContextScope,
		System:          cfg.System,
		Messages:        cfg.Messages,
		Query:           query,
		InlineImages:    inlineImages,
		ToolUsage:       cfg.ContextToolUsage,
		DynamicMutators: cfg.ContextDynamicMutators,
		Existing:        cfg.ContextFrags,
	})
	cfg.ContextFrags = assembled.Frags
	cfg.ContextManifest = assembled.Manifest
	return cfg
}

func (cfg RunConfig) RefreshContextFragWithDynamicMutators(readMedia bool, beforeModelCallHook bool, injectCh bool) RunConfig {
	cfg.ContextDynamicMutators = cfg.contextDynamicMutators(readMedia, beforeModelCallHook, injectCh)
	return cfg.RefreshContextFrag()
}

func (cfg RunConfig) contextDynamicMutators(readMedia bool, beforeModelCallHook bool, injectCh bool) []fragment.DynamicMutator {
	var mutators []fragment.DynamicMutator
	if cfg.Model != nil &&
		modelexecution.ResolveClientType(cfg.Model) == string(modeldomain.ClientTypeAnthropicMessages) &&
		modelexecution.NormalizePromptCacheTTL(cfg.PromptCacheTTL) != modelexecution.PromptCacheTTLOff {
		mutators = append(mutators, fragment.DynamicMutatorPromptCache)
	}
	if injectCh && cfg.InjectCh != nil {
		mutators = append(mutators, fragment.DynamicMutatorInjectCh)
	}
	if readMedia {
		mutators = append(mutators, fragment.DynamicMutatorReadMedia)
	}
	if beforeModelCallHook {
		mutators = append(mutators, fragment.DynamicMutatorBeforeModelCallHook)
	}
	if cfg.BackgroundManager != nil {
		mutators = append(mutators, fragment.DynamicMutatorBackgroundSummary)
	}
	mutators = append(mutators, fragment.DynamicMutatorMidTaskPrune)
	return mutators
}

package grok

import (
	sdk "github.com/felinics/twilight/sdk"
)

// usageFromMeta reads the turn's token usage. Grok reports it in the prompt
// response's _meta.usage, summed over the turn's model calls, with cache
// reads already counted inside inputTokens.
func usageFromMeta(meta map[string]any) *sdk.Usage {
	usage, ok := meta["usage"].(map[string]any)
	if !ok {
		return nil
	}
	count := func(key string) int {
		value, _ := usage[key].(float64)
		return int(value)
	}
	input, read, write, reasoning := count("inputTokens"), count("cachedReadTokens"), count("cacheCreationTokens"), count("reasoningTokens")
	return &sdk.Usage{
		InputTokens:        input,
		OutputTokens:       count("outputTokens"),
		TotalTokens:        count("totalTokens"),
		ReasoningTokens:    reasoning,
		CachedInputTokens:  read,
		InputTokenDetails:  sdk.InputTokenDetail{NoCacheTokens: input - read - write, CacheReadTokens: read, CacheWriteTokens: write},
		OutputTokenDetails: sdk.OutputTokenDetail{ReasoningTokens: reasoning},
	}
}

package execution

import (
	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

// Prompt cache TTL options accepted in Provider config.
//
// The values are vendor-neutral; ApplyPromptCache dispatches to the
// vendor-specific decoration based on the resolved client type. Today only
// Anthropic Messages implements caching, but the public API stays stable
// when other vendors gain similar support.
const (
	// PromptCacheTTL5m enables prompt caching with a short (~5 minute)
	// TTL. Recommended default.
	PromptCacheTTL5m = "5m"
	// PromptCacheTTL1h enables prompt caching with a 1-hour TTL. Some
	// vendors (e.g. Anthropic) bill 1h cache writes at a higher rate
	// than the default short TTL.
	PromptCacheTTL1h = "1h"
	// PromptCacheTTLOff disables prompt caching for the provider. Not
	// recommended: every request rebuilds the prefix without cache.
	PromptCacheTTLOff = "off"
)

// DefaultPromptCacheTTL is the value used when a provider does not
// explicitly configure a cache policy.
const DefaultPromptCacheTTL = PromptCacheTTL5m

// NormalizePromptCacheTTL coerces an arbitrary user-provided value to one
// of the accepted TTL constants. Empty or unrecognized values fall back to
// the recommended short-TTL default.
func NormalizePromptCacheTTL(s string) string {
	switch s {
	case PromptCacheTTL1h:
		return PromptCacheTTL1h
	case PromptCacheTTLOff:
		return PromptCacheTTLOff
	default:
		return DefaultPromptCacheTTL
	}
}

// ApplyPromptCache returns a request payload decorated with provider-specific
// prompt cache breakpoints. The dispatch is keyed off the resolved client
// type, so the call site does not need to know which vendor (if any) supports
// caching for the active model.
//
// For models whose vendor does not implement caching, or when the requested
// TTL is "off", the inputs are returned unchanged.
func ApplyPromptCache(
	model *sdk.Model,
	ttl string,
	system string,
	messages []sdk.Message,
	tools []sdk.Tool,
) (string, []sdk.Message, []sdk.Tool) {
	if model == nil {
		return system, messages, tools
	}
	normalized := NormalizePromptCacheTTL(ttl)
	if normalized == PromptCacheTTLOff {
		return system, messages, tools
	}
	switch ResolveClientType(model) {
	case string(modeldomain.ClientTypeAnthropicMessages):
		return applyAnthropicPromptCache(normalized, system, messages, tools)
	default:
		return system, messages, tools
	}
}

// applyAnthropicPromptCache decorates the request with Anthropic's
// cache_control breakpoints, mirroring the recommended structural cache
// layout:
//
//   - The system prompt is moved out of the dedicated `system` parameter
//     into the leading message slot as a SystemMessage with cache_control
//     on its TextPart, because Twilight's WithSystem accepts only a plain
//     string and does not propagate cache_control metadata.
//   - The final tool definition receives cache_control, which causes
//     Anthropic to cache the entire tool list up to and including that
//     tool.
func applyAnthropicPromptCache(
	ttl string,
	system string,
	messages []sdk.Message,
	tools []sdk.Tool,
) (string, []sdk.Message, []sdk.Tool) {
	cc := anthropicCacheControl(ttl)
	if cc == nil {
		return system, messages, tools
	}

	newMessages := messages
	newSystem := system
	if system != "" {
		systemMsg := sdk.Message{
			Role: sdk.MessageRoleSystem,
			Content: []sdk.MessagePart{
				sdk.TextPart{Text: system, CacheControl: cc},
			},
		}
		newMessages = make([]sdk.Message, 0, len(messages)+1)
		newMessages = append(newMessages, systemMsg)
		newMessages = append(newMessages, messages...)
		newSystem = ""
	}

	newTools := tools
	if len(tools) > 0 {
		newTools = make([]sdk.Tool, len(tools))
		copy(newTools, tools)
		newTools[len(newTools)-1].CacheControl = cc
	}

	return newSystem, newMessages, newTools
}

// anthropicCacheControl returns the SDK cache_control payload for Anthropic
// Messages requests for the given normalized TTL.
func anthropicCacheControl(ttl string) *sdk.CacheControl {
	switch ttl {
	case PromptCacheTTL1h:
		return &sdk.CacheControl{Type: "ephemeral", TTL: "1h"}
	case PromptCacheTTLOff:
		return nil
	default:
		return &sdk.CacheControl{Type: "ephemeral"}
	}
}

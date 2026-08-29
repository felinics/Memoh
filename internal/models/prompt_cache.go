package models

import (
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
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

// MinCacheablePrefixTokens is the floor below which a message-level cache
// breakpoint is withheld: Anthropic ignores breakpoints on prefixes under
// its per-model minimum (1024–4096 real tokens), and our byte-heuristic
// estimate undercounts CJK text, so the floor sits far below the provider
// minimum to never withhold a viable breakpoint — it only stops the
// bookkeeping from claiming a cached prefix the provider provably ignored.
const MinCacheablePrefixTokens = 256

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
	newSystem, newMessages, newTools, _, _ := ApplyPromptCacheWithPlan(model, ttl, contextfrag.CachePlan{}, system, messages, tools)
	return newSystem, newMessages, newTools
}

// ApplyPromptCacheWithPlan decorates the provider request using the
// placement-derived cache plan: when the plan marks a stable leading message
// span, the final message of that span receives a cache breakpoint so the
// stable prefix is cached across turns. A zero plan preserves the legacy
// system-and-tools-only layout. The 4th return value reports whether this
// call prepended a system message to messages (Anthropic's system->message
// cache promotion), so callers can tell that apart from an unrelated
// leading system-role message already present in the input. The 5th return
// value is the honest count of leading messages actually covered by an
// applied message-level breakpoint: it matches plan.StableMessageCount
// unless placement had to fall back to an earlier message (or found none),
// so callers can keep their own bookkeeping of the cached prefix truthful
// instead of trusting a claim that was never actually cached.
func ApplyPromptCacheWithPlan(
	model *sdk.Model,
	ttl string,
	plan contextfrag.CachePlan,
	system string,
	messages []sdk.Message,
	tools []sdk.Tool,
) (string, []sdk.Message, []sdk.Tool, bool, int) {
	if model == nil {
		return system, messages, tools, false, plan.StableMessageCount
	}
	normalized := NormalizePromptCacheTTL(ttl)
	if normalized == PromptCacheTTLOff {
		return system, messages, tools, false, plan.StableMessageCount
	}
	// OpenAI-family vendors identify cache-warm backends via a
	// prompt_cache_key on the request rather than explicit breakpoints;
	// wiring that through needs upstream support in the twilight-ai SDK,
	// which isn't available yet, so the default branch below is a no-op.
	switch ResolveClientType(model) {
	case string(ClientTypeAnthropicMessages):
		return applyAnthropicPromptCache(normalized, plan, system, messages, tools)
	default:
		return system, messages, tools, false, plan.StableMessageCount
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
	plan contextfrag.CachePlan,
	system string,
	messages []sdk.Message,
	tools []sdk.Tool,
) (string, []sdk.Message, []sdk.Tool, bool, int) {
	cc := anthropicCacheControl(ttl)
	if cc == nil {
		return system, messages, tools, false, plan.StableMessageCount
	}

	actualStableMessageCount := plan.StableMessageCount
	belowMinimum := plan.StablePrefixTokenEstimate > 0 && plan.StablePrefixTokenEstimate < MinCacheablePrefixTokens
	if plan.StableMessageCount > 0 && plan.StableMessageCount <= len(messages) && belowMinimum {
		actualStableMessageCount = 0
	}
	if plan.StableMessageCount > 0 && plan.StableMessageCount <= len(messages) && !belowMinimum {
		if plan.MidStableMessageCount > 0 && plan.MidStableMessageCount < plan.StableMessageCount {
			messages, _ = withMessageCacheBreakpoint(messages, plan.MidStableMessageCount-1, cc)
		}
		var breakpointIndex int
		messages, breakpointIndex = withMessageCacheBreakpoint(messages, plan.StableMessageCount-1, cc)
		if breakpointIndex >= 0 {
			actualStableMessageCount = breakpointIndex + 1
		} else {
			actualStableMessageCount = 0
		}
	}

	newMessages := messages
	newSystem := system
	systemPrepended := false
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
		systemPrepended = true
	}

	newTools := tools
	if len(tools) > 0 {
		newTools = make([]sdk.Tool, len(tools))
		copy(newTools, tools)
		newTools[len(newTools)-1].CacheControl = cc
	}

	return newSystem, newMessages, newTools, systemPrepended, actualStableMessageCount
}

// withMessageCacheBreakpoint sets a cache_control breakpoint on the last
// content part of messages[index], or — if that part's type does not carry a
// CacheControl field — scans backward for the nearest earlier message whose
// last part does. Not every SDK part type supports cache_control (TextPart,
// ImagePart and FilePart do; ReasoningPart, ToolCallPart and ToolResultPart
// do not), so a stable history message that happens to end in a tool
// call/result must not silently drop the breakpoint: that would leave the
// caller believing the declared prefix is cached when Anthropic never
// actually cached anything. Returns the updated messages and the index that
// received the breakpoint, or -1 if no message in [0, index] qualifies.
func withMessageCacheBreakpoint(messages []sdk.Message, index int, cc *sdk.CacheControl) ([]sdk.Message, int) {
	out := make([]sdk.Message, len(messages))
	copy(out, messages)
	for i := index; i >= 0; i-- {
		if setLastPartCacheControl(&out[i], cc) {
			return out, i
		}
	}
	return out, -1
}

// setLastPartCacheControl sets cc on msg's last content part in place if
// that part's type carries a CacheControl field, reporting whether it did.
func setLastPartCacheControl(msg *sdk.Message, cc *sdk.CacheControl) bool {
	if len(msg.Content) == 0 {
		return false
	}
	parts := make([]sdk.MessagePart, len(msg.Content))
	copy(parts, msg.Content)
	last := len(parts) - 1
	switch p := parts[last].(type) {
	case sdk.TextPart:
		p.CacheControl = cc
		parts[last] = p
	case sdk.ImagePart:
		p.CacheControl = cc
		parts[last] = p
	case sdk.FilePart:
		p.CacheControl = cc
		parts[last] = p
	default:
		return false
	}
	msg.Content = parts
	return true
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

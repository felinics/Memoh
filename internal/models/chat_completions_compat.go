package models

import (
	"strings"
)

// ChatCompletionsCompatDeepSeek enables DeepSeek request compatibility while
// still using the generic OpenAI Chat Completions provider.
const (
	ChatCompletionsCompatDeepSeek = "deepseek"
	ChatCompletionsCompatMiniMax  = "minimax"
	ChatCompletionsCompatKimi     = "kimi"
)

// ChatCompletionsCompatConfigKey is the provider config key holding the
// explicit Chat Completions compatibility mode.
const ChatCompletionsCompatConfigKey = "chat_completions_compat"

func normalizeChatCompletionsCompat(compat string) string {
	return strings.ToLower(strings.TrimSpace(compat))
}

func isDeepSeekChatCompletionsCompat(compat string) bool {
	return normalizeChatCompletionsCompat(compat) == ChatCompletionsCompatDeepSeek
}

func isMiniMaxChatCompletionsCompat(compat string) bool {
	return normalizeChatCompletionsCompat(compat) == ChatCompletionsCompatMiniMax
}

func isKimiChatCompletionsCompat(compat string) bool {
	return normalizeChatCompletionsCompat(compat) == ChatCompletionsCompatKimi
}

// officialCompatOrigins maps each compatibility mode to the endpoint origins
// operated by that vendor. Only origins the vendor controls belong here — the
// fallback below treats a matching origin as proof of the backend.
var officialCompatOrigins = map[string][]string{
	ChatCompletionsCompatDeepSeek: {"https://api.deepseek.com"},
	ChatCompletionsCompatMiniMax:  {"https://api.minimax.io", "https://api.minimaxi.com"},
	ChatCompletionsCompatKimi:     {"https://api.moonshot.cn", "https://api.moonshot.ai"},
}

// ResolveChatCompletionsCompat returns the compatibility mode for a provider.
// An explicit config value always wins, including one that matches no known
// mode (e.g. "none"), which disables inference. With no explicit value, the
// mode is inferred from official endpoint origins so provider rows created
// before the config existed keep their protocol adaptations. Origins match by
// exact origin or path prefix (covering /v1, /beta, ...), never by substring,
// so lookalike domains and proxies that merely embed an official hostname are
// not classified.
func ResolveChatCompletionsCompat(baseURL, compat string) string {
	if normalized := normalizeChatCompletionsCompat(compat); normalized != "" {
		return normalized
	}
	base := strings.TrimRight(strings.ToLower(strings.TrimSpace(baseURL)), "/")
	if base == "" {
		return ""
	}
	for _, mode := range []string{
		ChatCompletionsCompatDeepSeek,
		ChatCompletionsCompatMiniMax,
		ChatCompletionsCompatKimi,
	} {
		for _, origin := range officialCompatOrigins[mode] {
			if base == origin || strings.HasPrefix(base, origin+"/") {
				return mode
			}
		}
	}
	return ""
}

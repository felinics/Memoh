package execution

import (
	"strings"
)

// ChatCompletionsCompatDeepSeek enables DeepSeek request compatibility while
// still using the generic OpenAI Chat Completions provider.
const (
	ChatCompletionsCompatDeepSeek = "deepseek"
	ChatCompletionsCompatMiniMax  = "minimax"
)

func normalizeChatCompletionsCompat(compat string) string {
	return strings.ToLower(strings.TrimSpace(compat))
}

func isDeepSeekChatCompletionsCompat(compat string) bool {
	return normalizeChatCompletionsCompat(compat) == ChatCompletionsCompatDeepSeek
}

func isMiniMaxChatCompletionsCompat(compat string) bool {
	return normalizeChatCompletionsCompat(compat) == ChatCompletionsCompatMiniMax
}

// ResolveChatCompletionsCompat returns a normalized compatibility mode from
// explicit config, with fallbacks for built-in provider endpoints.
func ResolveChatCompletionsCompat(baseURL, compat string) string {
	compat = normalizeChatCompletionsCompat(compat)
	if compat != "" {
		return compat
	}
	base := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(base, "api.deepseek.com") {
		return ChatCompletionsCompatDeepSeek
	}
	if strings.Contains(base, "api.minimax.io") || strings.Contains(base, "api.minimaxi.com") {
		return ChatCompletionsCompatMiniMax
	}
	return ""
}

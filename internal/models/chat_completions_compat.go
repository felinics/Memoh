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

// ResolveChatCompletionsCompat returns the normalized explicit compatibility
// mode. Endpoint URLs are deliberately not used for provider detection: a
// proxy may serve any OpenAI-compatible backend, while a built-in provider may
// itself be reached through a custom endpoint.
func ResolveChatCompletionsCompat(compat string) string {
	return normalizeChatCompletionsCompat(compat)
}

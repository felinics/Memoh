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

// ResolveChatCompletionsCompat returns the normalized explicit compatibility
// mode. Endpoint URLs are deliberately not used for provider detection: a
// proxy may serve any OpenAI-compatible backend, while a built-in provider may
// itself be reached through a custom endpoint.
func ResolveChatCompletionsCompat(compat string) string {
	return normalizeChatCompletionsCompat(compat)
}

type legacyCompatRule struct {
	compat  string
	key     string
	sources []string
	origins []string
}

var legacyCompatRules = []legacyCompatRule{
	{
		compat:  ChatCompletionsCompatDeepSeek,
		key:     "deepseek",
		sources: []string{"deepseek.yaml", "deepseek.yml"},
		origins: []string{"https://api.deepseek.com"},
	},
	{
		compat:  ChatCompletionsCompatMiniMax,
		key:     "minimax",
		sources: []string{"minimax.yaml", "minimax.yml"},
		origins: []string{"https://api.minimax.io", "https://api.minimaxi.com"},
	},
	{
		compat:  ChatCompletionsCompatKimi,
		key:     "moonshot",
		sources: []string{"moonshot.yaml", "moonshot.yml"},
		origins: []string{"https://api.moonshot.cn", "https://api.moonshot.ai"},
	},
}

// LegacyChatCompletionsCompat returns the compatibility mode to stamp on an
// openai-completions provider that predates explicit configuration — for
// example one restored from a backup archive exported before migration 0123 —
// or "" when nothing should be stamped. It mirrors that migration: identify
// the built-in provider from template/preset metadata first, then from an
// official endpoint URL. URLs match by exact origin or path prefix, never by
// substring, so lookalike domains and proxies that merely embed an official
// hostname stay untouched.
func LegacyChatCompletionsCompat(clientType string, config, metadata map[string]any) string {
	if ClientType(strings.TrimSpace(clientType)) != ClientTypeOpenAICompletions {
		return ""
	}
	if explicit, _ := config[ChatCompletionsCompatConfigKey].(string); strings.TrimSpace(explicit) != "" {
		return ""
	}

	keys := []string{
		metadataPathString(metadata, "template", "key"),
		metadataPathString(metadata, "preset", "id"),
	}
	sources := []string{
		metadataPathString(metadata, "template", "source"),
		metadataPathString(metadata, "preset", "source"),
		metadataPathString(metadata, "registry", "source"),
	}
	rawBaseURL, _ := config["base_url"].(string)
	baseURL := strings.TrimRight(strings.ToLower(strings.TrimSpace(rawBaseURL)), "/")

	for _, rule := range legacyCompatRules {
		for _, key := range keys {
			if key == rule.key {
				return rule.compat
			}
		}
		for _, source := range sources {
			for _, ruleSource := range rule.sources {
				if source == ruleSource {
					return rule.compat
				}
			}
		}
		for _, origin := range rule.origins {
			if baseURL == origin || strings.HasPrefix(baseURL, origin+"/") {
				return rule.compat
			}
		}
	}
	return ""
}

func metadataPathString(metadata map[string]any, path ...string) string {
	var current any = metadata
	for _, key := range path {
		container, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = container[key]
	}
	value, _ := current.(string)
	return strings.ToLower(strings.TrimSpace(value))
}

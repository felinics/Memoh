package models

import (
	"net/http"
	"strings"

	anthropicmessages "github.com/memohai/twilight-ai/provider/anthropic/messages"
	googlegenerative "github.com/memohai/twilight-ai/provider/google/generativeai"
	openaicodex "github.com/memohai/twilight-ai/provider/openai/codex"
	openaicompletions "github.com/memohai/twilight-ai/provider/openai/completions"
	openairesponses "github.com/memohai/twilight-ai/provider/openai/responses"
	sdk "github.com/memohai/twilight-ai/sdk"

	memohcopilot "github.com/memohai/memoh/internal/copilot"
)

// SDKModelConfig holds provider and model information resolved from DB,
// used to construct a Twilight AI SDK Model instance.
type SDKModelConfig struct {
	ModelID        string
	ClientType     string
	APIKey         string //nolint:gosec // carries provider credential material at runtime
	CodexAccountID string
	BaseURL        string
	// ChatCompletionsCompat selects narrow compatibility behavior for
	// OpenAI-compatible /chat/completions backends.
	ChatCompletionsCompat string
	HTTPClient            *http.Client
	ReasoningConfig       *ReasoningConfig
}

// ReasoningConfig is the resolved extended-thinking decision for one call. The
// resolver makes a single decision (based on the model's thinking mode and the
// user's settings); the SDK layer mechanically translates it per provider.
// Anthropic 4.6+ (Adaptive) sends thinking{type:"adaptive"} plus an effort
// string and never a token budget; legacy Anthropic (<=4.5, non-adaptive) sends
// thinking{type:"enabled", budget_tokens:N} derived from the effort. OpenAI-style
// providers only ever receive an effort string.
type ReasoningConfig struct {
	// Active means thinking is on for this call.
	Active bool
	// Disabled means thinking was explicitly turned off (toggle=off). For
	// OpenAI-style providers without a real off switch, OffEffort approximates it.
	Disabled bool
	// Adaptive means the provider's thinking is adaptive (Anthropic 4.6+), wired
	// as thinking{type:"adaptive"} with no budget. When false on an active
	// Anthropic call, the model is treated as legacy (<=4.5) and wired as
	// thinking{type:"enabled", budget_tokens:N}.
	Adaptive bool
	// Effort is the effort tier to send when active ("" lets the SDK default).
	Effort string
	// OffEffort is the effort an OpenAI-style provider should send when disabled:
	// "none" when supported, else "minimal" when supported, else "" meaning the
	// reasoning_effort field is omitted entirely. It is never a real tier
	// (low/medium/high) because those enable thinking instead of disabling it.
	OffEffort string
}

// NewSDKChatModel builds a Twilight AI SDK Model from the resolved model config.
func NewSDKChatModel(cfg SDKModelConfig) *sdk.Model {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewProviderHTTPClient(0)
	}
	chatCompletionsCompat := ResolveChatCompletionsCompat(cfg.BaseURL, cfg.ChatCompletionsCompat)

	switch ClientType(cfg.ClientType) {
	case ClientTypeOpenAICompletions:
		opts := []openaicompletions.Option{
			openaicompletions.WithAPIKey(cfg.APIKey),
			openaicompletions.WithHTTPClient(cfg.HTTPClient),
		}
		if cfg.BaseURL != "" {
			opts = append(opts, openaicompletions.WithBaseURL(cfg.BaseURL))
		}
		opts = appendChatCompletionsCompat(opts, chatCompletionsCompat)
		p := openaicompletions.New(opts...)
		return p.ChatModel(cfg.ModelID)

	case ClientTypeOpenAIResponses:
		opts := []openairesponses.Option{
			openairesponses.WithAPIKey(cfg.APIKey),
		}
		opts = append(opts, openairesponses.WithHTTPClient(cfg.HTTPClient))
		if cfg.BaseURL != "" {
			opts = append(opts, openairesponses.WithBaseURL(cfg.BaseURL))
		}
		p := openairesponses.New(opts...)
		return p.ChatModel(cfg.ModelID)

	case ClientTypeOpenAICodex:
		opts := []openaicodex.Option{
			openaicodex.WithAccessToken(cfg.APIKey),
		}
		opts = append(opts, openaicodex.WithHTTPClient(cfg.HTTPClient))
		if cfg.CodexAccountID != "" {
			opts = append(opts, openaicodex.WithAccountID(cfg.CodexAccountID))
		}
		return openaicodex.New(opts...).ChatModel(cfg.ModelID)

	case ClientTypeGitHubCopilot:
		return memohcopilot.NewModel(cfg.APIKey, cfg.ModelID, cfg.HTTPClient)

	case ClientTypeAnthropicMessages:
		opts := []anthropicmessages.Option{
			anthropicmessages.WithAPIKey(cfg.APIKey),
		}
		opts = append(opts, anthropicmessages.WithHTTPClient(cfg.HTTPClient))
		if cfg.BaseURL != "" {
			opts = append(opts, anthropicmessages.WithBaseURL(cfg.BaseURL))
		}
		// Anthropic extended thinking has two wire shapes by model generation:
		//   - 4.6+ (Adaptive): thinking{type:"adaptive"}; effort is carried
		//     per-request via output_config.effort (see BuildReasoningOptions).
		//     budget_tokens is deprecated on 4.6 and rejected (400) on 4.7+, so it
		//     is never sent here.
		//   - <=4.5 (legacy): thinking is only enabled via
		//     thinking{type:"enabled", budget_tokens:N}; output_config.effort alone
		//     does not turn it on. The resolver flags every effort-era model as
		//     Adaptive (including cloud variants missing supports_adaptive_thinking),
		//     so a non-adaptive active config here is a legacy model.
		if rc := cfg.ReasoningConfig; rc != nil && rc.Active {
			if rc.Adaptive {
				opts = append(opts, anthropicmessages.WithThinking(anthropicmessages.ThinkingConfig{
					Type: "adaptive",
				}))
			} else {
				opts = append(opts, anthropicmessages.WithThinking(anthropicmessages.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: legacyAnthropicBudgetFor(rc.Effort),
				}))
			}
		}
		p := anthropicmessages.New(opts...)
		return p.ChatModel(cfg.ModelID)

	case ClientTypeGoogleGenerativeAI:
		opts := []googlegenerative.Option{
			googlegenerative.WithAPIKey(cfg.APIKey),
		}
		opts = append(opts, googlegenerative.WithHTTPClient(cfg.HTTPClient))
		if cfg.BaseURL != "" {
			opts = append(opts, googlegenerative.WithBaseURL(cfg.BaseURL))
		}
		p := googlegenerative.New(opts...)
		return p.ChatModel(cfg.ModelID)

	default:
		opts := []openaicompletions.Option{
			openaicompletions.WithAPIKey(cfg.APIKey),
			openaicompletions.WithHTTPClient(cfg.HTTPClient),
		}
		if cfg.BaseURL != "" {
			opts = append(opts, openaicompletions.WithBaseURL(cfg.BaseURL))
		}
		opts = appendChatCompletionsCompat(opts, chatCompletionsCompat)
		p := openaicompletions.New(opts...)
		return p.ChatModel(cfg.ModelID)
	}
}

func appendChatCompletionsCompat(
	opts []openaicompletions.Option,
	compat string,
) []openaicompletions.Option {
	switch {
	case isDeepSeekChatCompletionsCompat(compat):
		return append(opts, openaicompletions.WithDeepSeekChatCompletionsCompat())
	case isMiniMaxChatCompletionsCompat(compat):
		return append(opts, openaicompletions.WithMiniMaxChatCompletionsCompat())
	case isKimiChatCompletionsCompat(compat):
		return append(opts, openaicompletions.WithKimiChatCompletionsCompat())
	default:
		return opts
	}
}

// BuildReasoningOptions returns per-request SDK generation options for
// reasoning/thinking. It only ever sets an effort string (output_config.effort
// for Anthropic, reasoning.effort for OpenAI); the adaptive thinking flag is set
// at provider construction time in NewSDKChatModel. No token budgets are sent.
func BuildReasoningOptions(cfg SDKModelConfig) []sdk.GenerateOption {
	rc := cfg.ReasoningConfig
	if rc == nil {
		return nil
	}
	ct := ClientType(cfg.ClientType)

	// DeepSeek and MiniMax keep the generic Chat Completions transport but gate
	// thinking via a toggle rather than reasoning_effort. Their SDK compat layer
	// maps reasoning_effort "none" to thinking-off and any other effort to
	// thinking-on, so we forward "none" to disable and an explicit effort to
	// enable. Enabled-without-effort (adaptive) forwards nothing and lets the
	// provider's default thinking behavior apply.
	if ct == ClientTypeOpenAICompletions &&
		(isDeepSeekChatCompletionsCompat(cfg.ChatCompletionsCompat) || isMiniMaxChatCompletionsCompat(cfg.ChatCompletionsCompat)) {
		switch {
		case rc.Disabled:
			return []sdk.GenerateOption{sdk.WithReasoningEffort(ReasoningEffortNone)}
		case rc.Active && rc.Effort != "":
			return []sdk.GenerateOption{sdk.WithReasoningEffort(openAIWireEffort(ct, rc.Effort))}
		default:
			return nil
		}
	}

	switch ct {
	case ClientTypeAnthropicMessages:
		// Effort-era (4.6+, Adaptive) carries effort via output_config.effort;
		// thinking{adaptive} is set on the provider. Legacy (<=4.5) models enable
		// thinking via budget_tokens only and do not accept output_config.effort,
		// so send nothing for them. When disabled, send nothing too (absence of
		// thinking == off for Anthropic).
		if rc.Active && rc.Adaptive && rc.Effort != "" {
			return []sdk.GenerateOption{sdk.WithReasoningEffort(rc.Effort)}
		}
		return nil

	case ClientTypeGoogleGenerativeAI:
		// Google thinking is out of scope for the effort wire; leave untouched.
		return nil

	case ClientTypeOpenAIResponses, ClientTypeOpenAICodex, ClientTypeOpenAICompletions:
		return openAIEffortOptions(ct, rc)

	default:
		return openAIEffortOptions(ct, rc)
	}
}

// openAIEffortOptions maps a reasoning decision to OpenAI-style reasoning.effort.
// OpenAI models have no real on/off switch, so "off" is approximated by the
// lowest effort the model supports (none when available, otherwise minimal).
func openAIEffortOptions(clientType ClientType, rc *ReasoningConfig) []sdk.GenerateOption {
	switch {
	case rc.Active:
		effort := openAIWireEffort(clientType, rc.Effort)
		if effort == "" {
			effort = ReasoningEffortMedium
		}
		return []sdk.GenerateOption{sdk.WithReasoningEffort(effort)}
	case rc.Disabled:
		// Approximate "off". Only "none"/"minimal" actually reduce/disable
		// thinking; a real tier (low/medium/high) would turn thinking ON (e.g.
		// OpenRouter maps reasoning_effort:"low" to Anthropic extended thinking).
		// When the model advertises no such off-ish tier, OffEffort is empty and
		// we omit reasoning_effort entirely so the provider default (no thinking
		// for toggle/Anthropic-compat models) applies.
		off := openAIWireEffort(clientType, rc.OffEffort)
		if off == "" {
			return nil
		}
		return []sdk.GenerateOption{sdk.WithReasoningEffort(off)}
	default:
		return nil
	}
}

// openAIWireEffort retains the generic OpenAI clients' existing max-to-xhigh
// compatibility behavior. Codex accepts the catalog-advertised max value.
func openAIWireEffort(clientType ClientType, effort string) string {
	if clientType != ClientTypeOpenAICodex && effort == ReasoningEffortMax {
		return ReasoningEffortXHigh
	}
	return effort
}

// anthropicLegacyBudget maps an effort tier to the extended-thinking token
// budget for legacy (<=4.5) Claude models, which require
// thinking{type:"enabled", budget_tokens:N}. 4.6 deprecates budget_tokens and
// 4.7+ rejects it, so the resolver routes those generations through the adaptive
// path instead and this is only reached for pre-4.6 models (which advertise only
// the low/medium/high base). Values mirror the pre-adaptive defaults.
var anthropicLegacyBudget = map[string]int{
	ReasoningEffortLow:    5000,
	ReasoningEffortMedium: 16000,
	ReasoningEffortHigh:   50000,
}

// legacyAnthropicBudgetFor resolves the token budget for a legacy Anthropic
// thinking call, defaulting to the medium budget for empty or unexpected efforts.
func legacyAnthropicBudgetFor(effort string) int {
	if b, ok := anthropicLegacyBudget[effort]; ok {
		return b
	}
	return anthropicLegacyBudget[ReasoningEffortMedium]
}

// ResolveClientType infers the client type string from an SDK Model's provider name.
func ResolveClientType(model *sdk.Model) string {
	if model == nil || model.Provider == nil {
		return string(ClientTypeOpenAICompletions)
	}
	name := model.Provider.Name()
	switch {
	case strings.Contains(name, "anthropic"):
		return string(ClientTypeAnthropicMessages)
	case strings.Contains(name, "google"):
		return string(ClientTypeGoogleGenerativeAI)
	case strings.Contains(name, "github-copilot"), strings.Contains(name, "copilot"):
		return string(ClientTypeGitHubCopilot)
	case strings.Contains(name, "codex"):
		return string(ClientTypeOpenAICodex)
	case strings.Contains(name, "responses"):
		return string(ClientTypeOpenAIResponses)
	default:
		return string(ClientTypeOpenAICompletions)
	}
}

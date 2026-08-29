package models

import (
	"net/http"
	"strings"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	googlegenerative "github.com/felinics/twilight/provider/google/generativeai"
	openaicodex "github.com/felinics/twilight/provider/openai/codex"
	openaicompletions "github.com/felinics/twilight/provider/openai/completions"
	openairesponses "github.com/felinics/twilight/provider/openai/responses"
	sdk "github.com/felinics/twilight/sdk"

	memohcopilot "github.com/felinics/memoh/internal/copilot"
	"github.com/felinics/memoh/internal/reasoning"
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
	// ReasoningDialect and the budget bounds come from the model's catalog entry.
	// They say how this model spells its thinking control, which is not derivable
	// from the tiers it advertises.
	ReasoningDialect  string
	ThinkingBudgetMin *int
	ThinkingBudgetMax *int
	// ReasoningOffSupport declares whether this model accepts an explicit
	// thinking{type:"disabled"}. See anthropicAcceptsExplicitOff.
	ReasoningOffSupport string
	// ReasoningDefaultOn reports whether omitting the thinking field leaves the
	// model thinking. nil means unknown.
	ReasoningDefaultOn *bool
	// ContextWindow is the configured context window the turn budgets against;
	// legacy Anthropic thinking budgets are fitted to it. Zero means unknown.
	ContextWindow int
}

// ReasoningConfig is the resolved extended-thinking decision for one call,
// produced by internal/reasoning and translated here into each provider's wire
// shape. Anthropic 4.6+ (Adaptive) sends thinking{type:"adaptive"} plus an effort
// string and never a token budget; legacy Anthropic (<=4.5, non-adaptive) sends
// thinking{type:"enabled", budget_tokens:N} derived from the effort. OpenAI-style
// providers only ever receive an effort string.
type ReasoningConfig = reasoning.Config

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
		if baseURL := anthropicMessagesBaseURL(cfg.BaseURL); baseURL != "" {
			opts = append(opts, anthropicmessages.WithBaseURL(baseURL))
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
		//   - off: an explicit thinking{type:"disabled"} rather than an omitted
		//     field. Omission means "off" only up to 4.8; from Opus 5 and Sonnet 5
		//     on, thinking is the default and an omitted field leaves it running —
		//     billed, counted against max_tokens, and invisible to a user who
		//     believes they turned it off. Sending the explicit shape is only safe
		//     where the model accepts it, which the catalog declares.
		if rc := cfg.ReasoningConfig; rc != nil {
			switch {
			case rc.Active && rc.Adaptive:
				opts = append(opts, anthropicmessages.WithThinking(anthropicmessages.ThinkingConfig{
					Type: "adaptive",
				}))
			case rc.Active:
				opts = append(opts, anthropicmessages.WithThinking(anthropicmessages.ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: AnthropicThinkingBudget(rc.Effort, cfg.ContextWindow),
				}))
			case rc.Disabled && anthropicNeedsExplicitOff(cfg.ReasoningOffSupport, cfg.ReasoningDefaultOn):
				opts = append(opts, anthropicmessages.WithThinking(anthropicmessages.ThinkingConfig{
					Type: "disabled",
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
		if thinking, ok := googleThinkingFor(cfg); ok {
			opts = append(opts, googlegenerative.WithThinking(thinking))
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
		// Google's thinking control rides on the provider (see googleThinkingFor),
		// because budget and level are mutually exclusive and which one applies is
		// a property of the model, not of the request.
		return nil

	case ClientTypeOpenAIResponses, ClientTypeOpenAICodex, ClientTypeOpenAICompletions:
		return openAIEffortOptions(ct, rc)

	default:
		return openAIEffortOptions(ct, rc)
	}
}

// openAIEffortOptions maps a reasoning decision to OpenAI-style reasoning.effort.
// OpenAI expresses "off" as a member of the same enum ("none" from gpt-5.1 on),
// so the off state travels in the very field that carries the tiers.
func openAIEffortOptions(clientType ClientType, rc *ReasoningConfig) []sdk.GenerateOption {
	switch {
	case rc.Active:
		effort := openAIWireEffort(clientType, rc.Effort)
		if effort == "" {
			effort = ReasoningEffortMedium
		}
		return []sdk.GenerateOption{sdk.WithReasoningEffort(effort)}
	case rc.Disabled:
		// OffEffort is "none" when the model advertised that it can be turned off,
		// and "" when it cannot. Omitting the field in the latter case lets the
		// provider default stand; sending a real tier instead would turn thinking
		// ON (OpenRouter, for instance, maps reasoning_effort:"low" onto Anthropic
		// extended thinking), which is the opposite of what the user asked for.
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
//
// The resolver already filters "max" out of the selectable tiers for these
// clients, so this only fires for values that bypassed it — a stale stored effort,
// or a caller that built a ReasoningConfig by hand.
func openAIWireEffort(clientType ClientType, effort string) string {
	if clientType != ClientTypeOpenAICodex && effort == reasoning.EffortMax {
		return reasoning.EffortXHigh
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

// anthropicMessagesBaseURL normalizes a configured Anthropic base URL to the
// versioned API root the SDK joins /messages onto. Provider configs follow the
// template convention of a bare origin (https://api.anthropic.com), which
// model import already bridged by appending /v1; every other construction
// site passed the origin through verbatim, sending requests to
// {origin}/messages and failing on any endpoint. Normalizing here covers
// them all.
func anthropicMessagesBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" || strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
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

// googleThinkingFor translates a resolved reasoning decision into Gemini's
// thinking config. Which field to send is the model's declared dialect, never
// guessed from its id: 2.5 takes thinkingBudget, 3.x takes thinkingLevel, and
// sending both is a 400.
//
// The budget dialect maps a tier proportionally across the model's own range
// rather than through fixed token counts. A fixed table cannot be right for two
// models with different ceilings — 2.5 Pro allows 128..32768 while Flash allows
// 0..24576 — and the vendors deliberately publish no tier-to-token mapping,
// describing tiers as relative allowances rather than guarantees.
//
// IncludeThoughts is set whenever thinking is on, because the API emits no
// thought parts without it and the reasoning stream would stay empty.
func googleThinkingFor(cfg SDKModelConfig) (googlegenerative.ThinkingConfig, bool) {
	rc := cfg.ReasoningConfig
	if rc == nil {
		return googlegenerative.ThinkingConfig{}, false
	}

	switch cfg.ReasoningDialect {
	case reasoning.DialectBudget:
		budget, ok := googleBudgetFor(
			rc,
			cfg.ThinkingBudgetMin,
			cfg.ThinkingBudgetMax,
			cfg.ReasoningOffSupport,
		)
		if !ok {
			return googlegenerative.ThinkingConfig{}, false
		}
		thinking := googlegenerative.ThinkingConfig{ThinkingBudget: &budget}
		if budget != googlegenerative.ThinkingBudgetDisabled {
			thinking.IncludeThoughts = boolPtr(true)
		}
		return thinking, true
	case reasoning.DialectTier:
		// Tier dialect (Gemini 3.x). There is no off value on this wire — minimal
		// is the floor — so a disabled model sends nothing and lets the provider
		// default stand. OptionsFor keeps Off out of the picker for such models, so
		// reaching here with Disabled means a stale stored setting.
		if !rc.Active || rc.Effort == "" {
			return googlegenerative.ThinkingConfig{}, false
		}
		return googlegenerative.ThinkingConfig{
			ThinkingLevel:   rc.Effort,
			IncludeThoughts: boolPtr(true),
		}, true
	default:
		// Rows imported before the dialect field existed carry no trustworthy wire
		// declaration. Preserve their pre-upgrade request shape until a trusted
		// catalog re-import backfills it; guessing tier here makes old Gemini 2.5
		// rows send thinkingLevel and hard-fail.
		return googlegenerative.ThinkingConfig{}, false
	}
}

// googleBudgetFor resolves a tier into a token budget within the model's range,
// or the dynamic/disabled sentinels. It reports false when nothing should be sent.
func googleBudgetFor(rc *ReasoningConfig, minBudget, maxBudget *int, offSupport string) (int, bool) {
	switch {
	case rc.Disabled:
		// The active range and the off sentinel are separate capabilities. Flash
		// Lite, for example, has a positive active floor but still accepts 0 as an
		// explicit disable. Prefer the catalog declaration; retain the zero-floor
		// fallback for older imported rows that predate that field.
		if offSupport == reasoning.OffSupportRejected ||
			(offSupport == reasoning.OffSupportUnset && minBudget != nil && *minBudget > 0) {
			return 0, false
		}
		return googlegenerative.ThinkingBudgetDisabled, true
	case !rc.Active:
		return 0, false
	case rc.Effort == "":
		return googlegenerative.ThinkingBudgetDynamic, true
	}

	ratio, ok := reasoning.BudgetRatio(rc.Effort)
	if !ok {
		return googlegenerative.ThinkingBudgetDynamic, true
	}
	lo, hi := 0, 0
	if minBudget != nil {
		lo = *minBudget
	}
	if maxBudget != nil {
		hi = *maxBudget
	}
	if hi <= lo {
		return googlegenerative.ThinkingBudgetDynamic, true
	}
	return lo + int(ratio*float64(hi-lo)), true
}

func boolPtr(v bool) *bool { return &v }

// anthropicNeedsExplicitOff reports whether a disabled call must say so on the
// wire rather than express it by omitting the thinking field.
//
// Two independent facts decide this, which is why they are two fields. Whether the
// model *accepts* thinking{type:"disabled"} — Fable 5 and Mythos 5 answer with a
// 400 — and whether omission would have worked anyway. Omitting reads as off only
// through Opus 4.8; Opus 5 and Sonnet 5 think by default, so an omitted field
// leaves thinking running, billed as output tokens and counted against max_tokens,
// while the user believes it is off.
//
// Sending the explicit shape where omission already means off is harmless but
// noisy, so it is reserved for models that need it: accepted *and* on by default.
// An undeclared model keeps the omission behaviour, which is correct for every
// generation we currently ship.
func anthropicNeedsExplicitOff(offSupport string, defaultOn *bool) bool {
	if !reasoning.DefaultOn(defaultOn) {
		return false
	}
	switch offSupport {
	case reasoning.OffSupportAccepted, reasoning.OffSupportLowEffortOnly:
		return true
	default:
		// Thinks by default and rejects an explicit disable: off is unreachable on
		// this model. OptionsFor keeps the control out of the picker, so arriving
		// here means a stale stored setting rather than a user choice.
		return false
	}
}

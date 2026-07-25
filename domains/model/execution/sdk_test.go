package execution

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

func TestBuildReasoningOptionsDeepSeekChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *ReasoningConfig
		wantOpts int
	}{
		{
			name:     "disabled sends explicit none effort",
			config:   &ReasoningConfig{Disabled: true},
			wantOpts: 1,
		},
		{
			name:     "active with effort forwards effort",
			config:   &ReasoningConfig{Active: true, Effort: "high"},
			wantOpts: 1,
		},
		{
			name:     "active without effort leaves effort unset for model default",
			config:   &ReasoningConfig{Active: true},
			wantOpts: 0,
		},
		{
			name:     "nil config produces no options",
			config:   nil,
			wantOpts: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := BuildReasoningOptions(SDKModelConfig{
				ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
				ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
				ReasoningConfig:       tt.config,
			})
			if len(opts) != tt.wantOpts {
				t.Fatalf("expected %d options, got %d", tt.wantOpts, len(opts))
			}
		})
	}
}

func TestBuildReasoningOptionsOpenAIDisable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *ReasoningConfig
		wantOpts int
	}{
		{
			// Toggle model advertising only low/medium/high: OffEffort is empty,
			// so reasoning_effort must be omitted. Sending a real tier (e.g. low)
			// would enable thinking (OpenRouter maps it to Anthropic thinking).
			name:     "disabled with empty off effort omits reasoning_effort",
			config:   &ReasoningConfig{Disabled: true, OffEffort: ""},
			wantOpts: 0,
		},
		{
			name:     "disabled with none off effort sends none",
			config:   &ReasoningConfig{Disabled: true, OffEffort: modeldomain.ReasoningEffortNone},
			wantOpts: 1,
		},
		{
			name:     "disabled with minimal off effort sends minimal",
			config:   &ReasoningConfig{Disabled: true, OffEffort: modeldomain.ReasoningEffortMinimal},
			wantOpts: 1,
		},
		{
			name:     "active sends effort",
			config:   &ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
			wantOpts: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := BuildReasoningOptions(SDKModelConfig{
				ClientType:      string(modeldomain.ClientTypeOpenAICompletions),
				ReasoningConfig: tt.config,
			})
			if len(opts) != tt.wantOpts {
				t.Fatalf("expected %d options, got %d", tt.wantOpts, len(opts))
			}
		})
	}
}

func TestBuildReasoningOptionsMiniMaxChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *ReasoningConfig
		wantOpts int
	}{
		{
			name:     "disabled sends explicit none effort",
			config:   &ReasoningConfig{Disabled: true},
			wantOpts: 1,
		},
		{
			name:     "active without effort forwards nothing (provider default applies)",
			config:   &ReasoningConfig{Active: true},
			wantOpts: 0,
		},
		{
			name:     "active with effort forwards effort",
			config:   &ReasoningConfig{Active: true, Effort: "high"},
			wantOpts: 1,
		},
		{
			name:     "nil config produces no options",
			config:   nil,
			wantOpts: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := BuildReasoningOptions(SDKModelConfig{
				ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
				ChatCompletionsCompat: ChatCompletionsCompatMiniMax,
				ReasoningConfig:       tt.config,
			})
			if len(opts) != tt.wantOpts {
				t.Fatalf("expected %d options, got %d", tt.wantOpts, len(opts))
			}
		})
	}
}

func TestNewSDKChatModelDeepSeekChatCompletionsCompatDisablesThinking(t *testing.T) {
	t.Parallel()

	var body struct {
		ReasoningEffort *string `json:"reasoning_effort"`
		Thinking        *struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-deepseek",
			"model": "deepseek-v4-flash",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	model := NewSDKChatModel(SDKModelConfig{
		ModelID:               "deepseek-v4-flash",
		ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
		return
	}
	if model.Provider == nil || model.Provider.Name() != string(modeldomain.ClientTypeOpenAICompletions) {
		t.Fatalf("expected openai completions provider, got %+v", model.Provider)
	}

	_, err := sdk.GenerateTextResult(
		context.Background(),
		sdk.WithModel(model),
		sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
		sdk.WithReasoningEffort(modeldomain.ReasoningEffortNone),
	)
	if err != nil {
		t.Fatalf("generate text: %v", err)
	}

	if body.ReasoningEffort != nil {
		t.Fatalf("reasoning_effort should be omitted, got %q", *body.ReasoningEffort)
	}
	if body.Thinking == nil || body.Thinking.Type != "disabled" {
		t.Fatalf("thinking: got %#v, want disabled", body.Thinking)
	}
}

func TestNewSDKChatModelMiniMaxChatCompletionsCompatDisablesThinking(t *testing.T) {
	t.Parallel()

	var body struct {
		ReasoningEffort *string `json:"reasoning_effort"`
		ReasoningSplit  bool    `json:"reasoning_split"`
		Thinking        *struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-minimax",
			"model": "MiniMax-M3",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	compat := ResolveChatCompletionsCompat(srv.URL, ChatCompletionsCompatMiniMax)
	model := NewSDKChatModel(SDKModelConfig{
		ModelID:               "MiniMax-M3",
		ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: compat,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
	}

	opts := []sdk.GenerateOption{
		sdk.WithModel(model),
		sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
	}
	opts = append(opts, BuildReasoningOptions(SDKModelConfig{
		ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
		ChatCompletionsCompat: compat,
		ReasoningConfig:       &ReasoningConfig{Disabled: true},
	})...)
	_, err := sdk.GenerateTextResult(context.Background(), opts...)
	if err != nil {
		t.Fatalf("generate text: %v", err)
	}

	if !body.ReasoningSplit {
		t.Fatal("expected reasoning_split=true")
	}
	if body.ReasoningEffort != nil {
		t.Fatalf("reasoning_effort should be omitted, got %q", *body.ReasoningEffort)
	}
	if body.Thinking == nil || body.Thinking.Type != "disabled" {
		t.Fatalf("thinking: got %#v, want disabled", body.Thinking)
	}
}

func TestNewSDKChatModelMiniMaxChatCompletionsCompatEnablesThinking(t *testing.T) {
	t.Parallel()

	var body struct {
		ReasoningEffort *string `json:"reasoning_effort"`
		ReasoningSplit  bool    `json:"reasoning_split"`
		Thinking        *struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-minimax",
			"model": "MiniMax-M3",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	compat := ResolveChatCompletionsCompat(srv.URL, ChatCompletionsCompatMiniMax)
	model := NewSDKChatModel(SDKModelConfig{
		ModelID:               "MiniMax-M3",
		ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: compat,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
	}

	opts := []sdk.GenerateOption{
		sdk.WithModel(model),
		sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
	}
	opts = append(opts, BuildReasoningOptions(SDKModelConfig{
		ClientType:            string(modeldomain.ClientTypeOpenAICompletions),
		ChatCompletionsCompat: compat,
		ReasoningConfig:       &ReasoningConfig{Active: true, Effort: "high"},
	})...)
	_, err := sdk.GenerateTextResult(context.Background(), opts...)
	if err != nil {
		t.Fatalf("generate text: %v", err)
	}

	if !body.ReasoningSplit {
		t.Fatal("expected reasoning_split=true")
	}
	if body.ReasoningEffort != nil {
		t.Fatalf("reasoning_effort should be omitted, got %q", *body.ReasoningEffort)
	}
	if body.Thinking == nil || body.Thinking.Type != "adaptive" {
		t.Fatalf("thinking: got %#v, want adaptive", body.Thinking)
	}
}

func TestNewSDKChatModelOpenAIWireMapsMaxEffortToXHigh(t *testing.T) {
	t.Parallel()

	var body struct {
		ReasoningEffort *string `json:"reasoning_effort"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-openai",
			"model": "openrouter/anthropic/claude-opus-4.8",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer srv.Close()

	model := NewSDKChatModel(SDKModelConfig{
		ModelID:    "openrouter/anthropic/claude-opus-4.8",
		ClientType: string(modeldomain.ClientTypeOpenAICompletions),
		BaseURL:    srv.URL,
		APIKey:     "test-key",
	})

	opts := BuildReasoningOptions(SDKModelConfig{
		ClientType:      string(modeldomain.ClientTypeOpenAICompletions),
		ReasoningConfig: &ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortMax},
	})
	_, err := sdk.GenerateTextResult(
		context.Background(),
		append([]sdk.GenerateOption{
			sdk.WithModel(model),
			sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
		}, opts...)...,
	)
	if err != nil {
		t.Fatalf("generate text: %v", err)
	}

	if body.ReasoningEffort == nil || *body.ReasoningEffort != modeldomain.ReasoningEffortXHigh {
		t.Fatalf("reasoning_effort: got %v, want xhigh", body.ReasoningEffort)
	}
}

func TestOpenAIWireEffortPreservesMaxForCodex(t *testing.T) {
	t.Parallel()

	if got := openAIWireEffort(modeldomain.ClientTypeOpenAICodex, modeldomain.ReasoningEffortMax); got != modeldomain.ReasoningEffortMax {
		t.Fatalf("openAIWireEffort() = %q, want %q", got, modeldomain.ReasoningEffortMax)
	}
	if got := openAIWireEffort(modeldomain.ClientTypeOpenAIResponses, modeldomain.ReasoningEffortMax); got != modeldomain.ReasoningEffortXHigh {
		t.Fatalf("openAIWireEffort() = %q, want %q", got, modeldomain.ReasoningEffortXHigh)
	}
}

func TestNewSDKChatModelAnthropicThinkingWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     *ReasoningConfig
		wantType   string
		wantBudget int
	}{
		{
			// Legacy (<=4.5): non-adaptive active call must enable thinking via
			// budget_tokens (output_config.effort alone does not turn it on).
			name:       "legacy non-adaptive sends enabled with budget",
			config:     &ReasoningConfig{Active: true, Effort: modeldomain.ReasoningEffortHigh},
			wantType:   "enabled",
			wantBudget: 50000,
		},
		{
			name:       "legacy non-adaptive defaults empty effort to medium budget",
			config:     &ReasoningConfig{Active: true},
			wantType:   "enabled",
			wantBudget: 16000,
		},
		{
			// 4.6+ (adaptive): thinking{type:"adaptive"} and never a budget.
			name:       "adaptive sends adaptive without budget",
			config:     &ReasoningConfig{Active: true, Adaptive: true, Effort: modeldomain.ReasoningEffortHigh},
			wantType:   "adaptive",
			wantBudget: 0,
		},
		{
			// Disabled: no thinking field at all.
			name:     "disabled sends no thinking",
			config:   &ReasoningConfig{Disabled: true},
			wantType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body struct {
				Thinking *struct {
					Type         string `json:"type"`
					BudgetTokens int    `json:"budget_tokens"`
				} `json:"thinking"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "msg_anthropic", "type": "message", "model": "claude-test", "role": "assistant",
					"content":     []map[string]any{{"type": "text", "text": "ok"}},
					"stop_reason": "end_turn",
					"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
				})
			}))
			defer srv.Close()

			cfg := SDKModelConfig{
				ModelID:         "claude-test",
				ClientType:      string(modeldomain.ClientTypeAnthropicMessages),
				BaseURL:         srv.URL,
				APIKey:          "test-key",
				ReasoningConfig: tt.config,
			}
			model := NewSDKChatModel(cfg)
			if model == nil {
				t.Fatal("expected a model, got nil")
			}

			opts := append([]sdk.GenerateOption{
				sdk.WithModel(model),
				sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
			}, BuildReasoningOptions(cfg)...)
			if _, err := sdk.GenerateTextResult(context.Background(), opts...); err != nil {
				t.Fatalf("generate text: %v", err)
			}

			if tt.wantType == "" {
				if body.Thinking != nil {
					t.Fatalf("thinking should be omitted, got %#v", body.Thinking)
				}
				return
			}
			if body.Thinking == nil {
				t.Fatalf("thinking missing, want type %q", tt.wantType)
			}
			if body.Thinking.Type != tt.wantType {
				t.Fatalf("thinking type: got %q, want %q", body.Thinking.Type, tt.wantType)
			}
			if body.Thinking.BudgetTokens != tt.wantBudget {
				t.Fatalf("budget_tokens: got %d, want %d", body.Thinking.BudgetTokens, tt.wantBudget)
			}
		})
	}
}

func TestLegacyAnthropicBudgetFor(t *testing.T) {
	t.Parallel()

	cases := map[string]int{
		modeldomain.ReasoningEffortLow:    5000,
		modeldomain.ReasoningEffortMedium: 16000,
		modeldomain.ReasoningEffortHigh:   50000,
		"":                                16000,
		"unexpected":                      16000,
	}
	for effort, want := range cases {
		if got := legacyAnthropicBudgetFor(effort); got != want {
			t.Fatalf("legacyAnthropicBudgetFor(%q): got %d, want %d", effort, got, want)
		}
	}
}

func TestResolveChatCompletionsCompatInfersDeepSeekBaseURL(t *testing.T) {
	t.Parallel()

	got := ResolveChatCompletionsCompat("https://api.deepseek.com/v1", "")
	if got != ChatCompletionsCompatDeepSeek {
		t.Fatalf("expected deepseek compat, got %q", got)
	}
}

func TestResolveChatCompletionsCompatInfersMiniMaxBaseURL(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://api.minimax.io/v1",
		"https://api.minimaxi.com/v1",
	}
	for _, baseURL := range tests {
		t.Run(baseURL, func(t *testing.T) {
			t.Parallel()
			got := ResolveChatCompletionsCompat(baseURL, "")
			if got != ChatCompletionsCompatMiniMax {
				t.Fatalf("expected minimax compat, got %q", got)
			}
		})
	}
}

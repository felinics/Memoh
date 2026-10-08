package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestApplyReasoningToRequestDeepSeekChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     *ReasoningConfig
		wantEffort bool
	}{
		{
			name:       "disabled sends explicit none effort",
			config:     &ReasoningConfig{Disabled: true},
			wantEffort: true,
		},
		{
			name:       "active with effort forwards effort",
			config:     &ReasoningConfig{Active: true, Effort: "high"},
			wantEffort: true,
		},
		{
			name:       "active without effort leaves effort unset for model default",
			config:     &ReasoningConfig{Active: true},
			wantEffort: false,
		},
		{
			name:       "nil config leaves effort unset",
			config:     nil,
			wantEffort: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireReasoningEffort(t, SDKModelConfig{
				ClientType:            string(ClientTypeOpenAICompletions),
				ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
				ReasoningConfig:       tt.config,
			}, tt.wantEffort)
		})
	}
}

func TestApplyReasoningToRequestOpenAIDisable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     *ReasoningConfig
		wantEffort bool
	}{
		{
			// Toggle model advertising only low/medium/high: OffEffort is empty,
			// so reasoning_effort must be omitted. Sending a real tier (e.g. low)
			// would enable thinking (OpenRouter maps it to Anthropic thinking).
			name:       "disabled with empty off effort omits reasoning_effort",
			config:     &ReasoningConfig{Disabled: true, OffEffort: ""},
			wantEffort: false,
		},
		{
			name:       "disabled with none off effort sends none",
			config:     &ReasoningConfig{Disabled: true, OffEffort: ReasoningEffortNone},
			wantEffort: true,
		},
		{
			name:       "disabled with minimal off effort sends minimal",
			config:     &ReasoningConfig{Disabled: true, OffEffort: ReasoningEffortMinimal},
			wantEffort: true,
		},
		{
			name:       "active sends effort",
			config:     &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
			wantEffort: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireReasoningEffort(t, SDKModelConfig{
				ClientType:      string(ClientTypeOpenAICompletions),
				ReasoningConfig: tt.config,
			}, tt.wantEffort)
		})
	}
}

func TestApplyReasoningToRequestMiniMaxChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     *ReasoningConfig
		wantEffort bool
	}{
		{
			name:       "disabled sends explicit none effort",
			config:     &ReasoningConfig{Disabled: true},
			wantEffort: true,
		},
		{
			name:       "active without effort forwards nothing (provider default applies)",
			config:     &ReasoningConfig{Active: true},
			wantEffort: false,
		},
		{
			name:       "active with effort forwards effort",
			config:     &ReasoningConfig{Active: true, Effort: "high"},
			wantEffort: true,
		},
		{
			name:       "nil config leaves effort unset",
			config:     nil,
			wantEffort: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireReasoningEffort(t, SDKModelConfig{
				ClientType:            string(ClientTypeOpenAICompletions),
				ChatCompletionsCompat: ChatCompletionsCompatMiniMax,
				ReasoningConfig:       tt.config,
			}, tt.wantEffort)
		})
	}
}

// requireReasoningEffort applies cfg through the request path and asserts it
// agrees with ReasoningEffortParam about whether an effort is sent at all.
func requireReasoningEffort(t *testing.T, cfg SDKModelConfig, wantEffort bool) {
	t.Helper()
	req := sdk.Request{}
	ApplyReasoningToRequest(&req, cfg)
	effort, ok := ReasoningEffortParam(cfg)
	if !wantEffort {
		if req.ReasoningEffort != nil || ok {
			t.Fatalf("effort = %v (ok=%v), want unset", req.ReasoningEffort, ok)
		}
		return
	}
	if !ok || req.ReasoningEffort == nil || *req.ReasoningEffort != effort {
		t.Fatalf("effort = %v (ok=%v), want %q", req.ReasoningEffort, ok, effort)
	}
}

func TestApplyReasoningToRequestSetsReasoningEffortParam(t *testing.T) {
	t.Parallel()

	tests := []SDKModelConfig{
		{
			ClientType:            string(ClientTypeOpenAICompletions),
			ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
			ReasoningConfig:       &ReasoningConfig{Disabled: true},
		},
		{
			ClientType:            string(ClientTypeOpenAICompletions),
			ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
			ReasoningConfig:       &ReasoningConfig{Active: true},
		},
		{
			ClientType:      string(ClientTypeOpenAICompletions),
			ReasoningConfig: &ReasoningConfig{Active: true, Effort: ReasoningEffortMax},
		},
		{
			ClientType:      string(ClientTypeAnthropicMessages),
			ReasoningConfig: &ReasoningConfig{Disabled: true},
		},
	}
	for _, cfg := range tests {
		req := sdk.Request{}
		ApplyReasoningToRequest(&req, cfg)
		effort, ok := ReasoningEffortParam(cfg)
		if !ok {
			if req.ReasoningEffort != nil {
				t.Fatalf("ApplyReasoningToRequest set effort %q, want unset", *req.ReasoningEffort)
			}
			continue
		}
		if req.ReasoningEffort == nil || *req.ReasoningEffort != effort {
			t.Fatalf("ApplyReasoningToRequest effort = %v (ok=%v), want %q", req.ReasoningEffort, ok, effort)
		}
	}
	ApplyReasoningToRequest(nil, SDKModelConfig{})
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
		ClientType:            string(ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: ChatCompletionsCompatDeepSeek,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
		return
	}
	if model.Provider == nil || model.Provider.Name() != string(ClientTypeOpenAICompletions) {
		t.Fatalf("expected openai completions provider, got %+v", model.Provider)
	}

	effort := ReasoningEffortNone
	if _, err := model.Generate(context.Background(), sdk.Request{
		Messages:        []sdk.Message{sdk.UserMessage("hi")},
		ReasoningEffort: &effort,
	}); err != nil {
		t.Fatalf("generate: %v", err)
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
		ClientType:            string(ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: compat,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
	}

	cfg := SDKModelConfig{
		ClientType:            string(ClientTypeOpenAICompletions),
		ChatCompletionsCompat: compat,
		ReasoningConfig:       &ReasoningConfig{Disabled: true},
	}
	req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
	ApplyReasoningToRequest(&req, cfg)
	if _, err := model.Generate(context.Background(), req); err != nil {
		t.Fatalf("generate: %v", err)
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
		ClientType:            string(ClientTypeOpenAICompletions),
		BaseURL:               srv.URL,
		ChatCompletionsCompat: compat,
		APIKey:                "test-key",
	})
	if model == nil {
		t.Fatal("expected a model, got nil")
	}

	cfg := SDKModelConfig{
		ClientType:            string(ClientTypeOpenAICompletions),
		ChatCompletionsCompat: compat,
		ReasoningConfig:       &ReasoningConfig{Active: true, Effort: "high"},
	}
	req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
	ApplyReasoningToRequest(&req, cfg)
	if _, err := model.Generate(context.Background(), req); err != nil {
		t.Fatalf("generate: %v", err)
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
		ClientType: string(ClientTypeOpenAICompletions),
		BaseURL:    srv.URL,
		APIKey:     "test-key",
	})

	cfg := SDKModelConfig{
		ClientType:      string(ClientTypeOpenAICompletions),
		ReasoningConfig: &ReasoningConfig{Active: true, Effort: ReasoningEffortMax},
	}
	req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
	ApplyReasoningToRequest(&req, cfg)
	if _, err := model.Generate(context.Background(), req); err != nil {
		t.Fatalf("generate: %v", err)
	}

	if body.ReasoningEffort == nil || *body.ReasoningEffort != ReasoningEffortXHigh {
		t.Fatalf("reasoning_effort: got %v, want xhigh", body.ReasoningEffort)
	}
}

func TestOpenAIWireEffortPreservesMaxForCodex(t *testing.T) {
	t.Parallel()

	if got := openAIWireEffort(ClientTypeOpenAICodex, ReasoningEffortMax); got != ReasoningEffortMax {
		t.Fatalf("openAIWireEffort() = %q, want %q", got, ReasoningEffortMax)
	}
	if got := openAIWireEffort(ClientTypeOpenAIResponses, ReasoningEffortMax); got != ReasoningEffortXHigh {
		t.Fatalf("openAIWireEffort() = %q, want %q", got, ReasoningEffortXHigh)
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
			config:     &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh},
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
			config:     &ReasoningConfig{Active: true, Adaptive: true, Effort: ReasoningEffortHigh},
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
				ClientType:      string(ClientTypeAnthropicMessages),
				BaseURL:         srv.URL,
				APIKey:          "test-key",
				ReasoningConfig: tt.config,
			}
			model := NewSDKChatModel(cfg)
			if model == nil {
				t.Fatal("expected a model, got nil")
			}

			req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
			ApplyReasoningToRequest(&req, cfg)
			if _, err := model.Generate(context.Background(), req); err != nil {
				t.Fatalf("generate: %v", err)
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
		ReasoningEffortLow:    5000,
		ReasoningEffortMedium: 16000,
		ReasoningEffortHigh:   50000,
		"":                    16000,
		"unexpected":          16000,
	}
	for effort, want := range cases {
		if got := legacyAnthropicBudgetFor(effort); got != want {
			t.Fatalf("legacyAnthropicBudgetFor(%q): got %d, want %d", effort, got, want)
		}
	}
}

func TestResolveChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		compat  string
		want    string
	}{
		{name: "blank everything", want: ""},
		{name: "deepseek normalized", compat: " DeepSeek ", want: ChatCompletionsCompatDeepSeek},
		{name: "minimax normalized", compat: " MINIMAX ", want: ChatCompletionsCompatMiniMax},
		{name: "kimi normalized", compat: " KiMi ", want: ChatCompletionsCompatKimi},
		{name: "unknown remains explicit", compat: " Vendor-Specific ", want: "vendor-specific"},
		{
			name:    "explicit wins over official origin",
			baseURL: "https://api.deepseek.com/v1",
			compat:  ChatCompletionsCompatKimi,
			want:    ChatCompletionsCompatKimi,
		},
		{
			name:    "explicit none disables inference",
			baseURL: "https://api.deepseek.com/v1",
			compat:  "none",
			want:    "none",
		},
		{
			name:    "deepseek origin",
			baseURL: "https://api.deepseek.com",
			want:    ChatCompletionsCompatDeepSeek,
		},
		{
			name:    "deepseek beta path",
			baseURL: "https://api.deepseek.com/beta",
			want:    ChatCompletionsCompatDeepSeek,
		},
		{
			name:    "minimax v1 trailing slash",
			baseURL: "https://api.minimaxi.com/v1/",
			want:    ChatCompletionsCompatMiniMax,
		},
		{
			name:    "minimax io origin",
			baseURL: "https://api.minimax.io/v1",
			want:    ChatCompletionsCompatMiniMax,
		},
		{
			name:    "moonshot cn infers kimi",
			baseURL: "https://api.moonshot.cn/v1",
			want:    ChatCompletionsCompatKimi,
		},
		{
			name:    "moonshot ai infers kimi",
			baseURL: "HTTPS://API.MOONSHOT.AI/v1",
			want:    ChatCompletionsCompatKimi,
		},
		{
			name:    "lookalike domain rejected",
			baseURL: "https://api.deepseek.com.evil.example/v1",
			want:    "",
		},
		{
			name:    "official hostname embedded in proxy path rejected",
			baseURL: "https://gateway.example/https://api.moonshot.cn/v1",
			want:    "",
		},
		{
			name:    "unrelated proxy stays generic",
			baseURL: "https://proxy.example/v1",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveChatCompletionsCompat(tt.baseURL, tt.compat); got != tt.want {
				t.Fatalf("ResolveChatCompletionsCompat(%q, %q) = %q, want %q",
					tt.baseURL, tt.compat, got, tt.want)
			}
		})
	}
}

func TestNewSDKChatModelKimiCompatIsExplicitForEveryCompletionsBranch(t *testing.T) {
	t.Parallel()

	clientTypes := []string{
		string(ClientTypeOpenAICompletions),
		"unknown-openai-compatible-client",
	}
	for _, clientType := range clientTypes {
		t.Run(clientType, func(t *testing.T) {
			t.Parallel()

			var parameters map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []struct {
						Function struct {
							Parameters map[string]any `json:"parameters"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				if len(body.Tools) != 1 {
					t.Fatalf("tools length = %d, want 1", len(body.Tools))
				}
				parameters = body.Tools[0].Function.Parameters
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":    "chatcmpl-kimi",
					"model": "kimi-k2.5",
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
				ModelID:               "kimi-k2.5",
				ClientType:            clientType,
				BaseURL:               srv.URL,
				ChatCompletionsCompat: ChatCompletionsCompatKimi,
				APIKey:                "test-key",
			})
			tools, err := toolexec.ToolDefinitionsFromTools([]toolexec.Tool{{
				Name: "attach_file",
				Parameters: toolexec.SchemaFromValue(map[string]any{
					"type": "object",
					"properties": map[string]any{
						"attachment": map[string]any{
							"type": "object",
							"anyOf": []any{
								map[string]any{
									"properties": map[string]any{"path": map[string]any{"type": "string"}},
								},
								map[string]any{
									"properties": map[string]any{"url": map[string]any{"type": "string"}},
								},
							},
						},
					},
				}),
			}})
			if err != nil {
				t.Fatalf("tool definitions: %v", err)
			}
			if _, err := model.Generate(context.Background(), sdk.Request{
				Messages: []sdk.Message{sdk.UserMessage("hi")},
				Tools:    tools,
			}); err != nil {
				t.Fatalf("generate: %v", err)
			}

			properties, ok := parameters["properties"].(map[string]any)
			if !ok {
				t.Fatalf("parameters.properties = %T, want object", parameters["properties"])
			}
			attachment, ok := properties["attachment"].(map[string]any)
			if !ok {
				t.Fatalf("attachment schema = %T, want object", properties["attachment"])
			}
			if _, exists := attachment["type"]; exists {
				t.Fatalf("explicit Kimi compat left type beside anyOf: %#v", attachment)
			}
			anyOf, ok := attachment["anyOf"].([]any)
			if !ok || len(anyOf) != 2 {
				t.Fatalf("attachment.anyOf = %#v, want two branches", attachment["anyOf"])
			}
			for index, rawBranch := range anyOf {
				branch, ok := rawBranch.(map[string]any)
				if !ok || branch["type"] != "object" {
					t.Fatalf("attachment.anyOf[%d] = %#v, want type object", index, rawBranch)
				}
			}
		})
	}
}

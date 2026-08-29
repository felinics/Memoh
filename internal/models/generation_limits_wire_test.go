package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

// TestAnthropicGenerationLimitsMatchTheAdapterDefaults binds the mirrored
// constants to the SDK's real wire: with no explicit MaxTokens, the adapter
// must resolve exactly what ResolveGenerationLimits reserves, and the thinking
// budget it sends must stay below that cap.
func TestAnthropicGenerationLimitsMatchTheAdapterDefaults(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		reasoning *ReasoningConfig
		window    int
	}{
		{name: "no reasoning", window: 200_000},
		{name: "disabled", reasoning: &ReasoningConfig{Disabled: true}, window: 200_000},
		{name: "adaptive", reasoning: &ReasoningConfig{Active: true, Adaptive: true, Effort: ReasoningEffortHigh}, window: 200_000},
		{name: "legacy low", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortLow}, window: 200_000},
		{name: "legacy medium", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortMedium}, window: 200_000},
		{name: "legacy high", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}, window: 200_000},
		{name: "legacy high fitted to a 64k window", reasoning: &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}, window: 64_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var body struct {
				MaxTokens int `json:"max_tokens"`
				Thinking  *struct {
					Type         string `json:"type"`
					BudgetTokens int    `json:"budget_tokens"`
				} `json:"thinking"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
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
				ReasoningConfig: tc.reasoning,
				ContextWindow:   tc.window,
			}
			opts := append([]sdk.GenerateOption{
				sdk.WithModel(NewSDKChatModel(cfg)),
				sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
			}, BuildReasoningOptions(cfg)...)
			if _, err := sdk.GenerateTextResult(context.Background(), opts...); err != nil {
				t.Fatalf("generate text: %v", err)
			}

			limits := ResolveGenerationLimits(ClientTypeAnthropicMessages, tc.reasoning, tc.window)
			if body.MaxTokens != limits.MaxOutputTokens {
				t.Fatalf("adapter max_tokens = %d, resolver reserves %d", body.MaxTokens, limits.MaxOutputTokens)
			}
			if body.Thinking != nil && body.Thinking.Type == "enabled" {
				if body.Thinking.BudgetTokens != limits.MaxOutputTokens-anthropicDefaultMaxTokens {
					t.Fatalf("budget_tokens = %d, want %d below max_tokens", body.Thinking.BudgetTokens, limits.MaxOutputTokens-anthropicDefaultMaxTokens)
				}
			}
		})
	}
}

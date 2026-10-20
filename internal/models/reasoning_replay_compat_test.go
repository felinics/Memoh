package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

// Since twilight v0.8.0 the Chat Completions compat options no longer imply a
// reasoning replay policy. These pin that each vendor still gets the replay its
// API needs: DeepSeek and Kimi reject a thinking-mode tool call replayed without
// reasoning_content, and MiniMax reads its reasoning back from reasoning_details.
func TestChatCompletionsCompatReasoningReplay(t *testing.T) {
	t.Parallel()

	details := `[{"type":"reasoning.text","text":"Paris first.","index":0}]`
	cases := []struct {
		name        string
		compat      string
		reasoning   []sdk.ReasoningPart
		replay      *ReasoningReplay
		wantContent *string
		wantDetails bool
	}{
		{name: "deepseek pads a bare tool call", compat: ChatCompletionsCompatDeepSeek, wantContent: strPtr("")},
		// A model that only names the field DeepSeek needs keeps it required.
		{name: "deepseek stays required under a model field", compat: ChatCompletionsCompatDeepSeek, replay: &ReasoningReplay{Field: ReasoningReplayContent}, wantContent: strPtr("")},
		{name: "a model field alone is not required", compat: "none", replay: &ReasoningReplay{Field: ReasoningReplayContent}},
		{name: "a model can require the replay", compat: "none", replay: &ReasoningReplay{Field: ReasoningReplayContent, Required: true}, wantContent: strPtr("")},
		{name: "kimi pads a bare tool call", compat: ChatCompletionsCompatKimi, wantContent: strPtr("")},
		{name: "plain endpoint sends nothing", compat: "none"},
		{
			name:   "minimax replays reasoning_details only",
			compat: ChatCompletionsCompatMiniMax,
			reasoning: []sdk.ReasoningPart{{
				Text:             "Paris first.",
				Format:           sdk.ReasoningFormatOpenAIChat,
				ProviderMetadata: sdk.NewProviderMetadata("openai-chat", map[string]string{"reasoning_details": details}),
			}},
			wantDetails: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var sent []map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []map[string]json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				sent = body.Messages
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "c", "model": "m",
					"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "ok"}}},
					"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
				})
			}))
			defer srv.Close()

			model := NewSDKChatModel(SDKModelConfig{
				ModelID:               "m",
				ClientType:            string(ClientTypeOpenAICompletions),
				APIKey:                "k",
				BaseURL:               srv.URL,
				ChatCompletionsCompat: tc.compat,
				ReasoningReplay:       tc.replay,
			})
			parts := make([]sdk.MessagePart, 0, len(tc.reasoning)+1)
			for _, r := range tc.reasoning {
				parts = append(parts, r)
			}
			parts = append(parts, sdk.ToolCallPart{ToolCallID: "c1", ToolName: "lookup", Input: sdk.ParseToolArguments(`{"q":"x"}`)})
			_, err := model.Generate(context.Background(), sdk.Request{
				Messages: []sdk.Message{
					sdk.UserMessage("hi"),
					{Role: sdk.MessageRoleAssistant, Content: parts},
					sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "c1", ToolName: "lookup", Result: sdk.TextOutput("done")}),
				},
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(sent) < 2 {
				t.Fatalf("messages: got %d", len(sent))
			}
			assistant := sent[1]
			raw, hasContent := assistant["reasoning_content"]
			switch {
			case tc.wantContent == nil && hasContent:
				t.Errorf("reasoning_content: got %s, want absent", raw)
			case tc.wantContent != nil && !hasContent:
				t.Errorf("reasoning_content: absent, want %q", *tc.wantContent)
			case tc.wantContent != nil && string(raw) != `"`+*tc.wantContent+`"`:
				t.Errorf("reasoning_content: got %s, want %q", raw, *tc.wantContent)
			}
			if _, hasDetails := assistant["reasoning_details"]; hasDetails != tc.wantDetails {
				t.Errorf("reasoning_details present = %v, want %v", hasDetails, tc.wantDetails)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

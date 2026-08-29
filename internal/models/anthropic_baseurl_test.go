package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
)

func TestNewSDKChatModelAnthropicMessagesBaseURLGainsV1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL func(serverURL string) string
	}{
		{name: "bare origin", baseURL: func(u string) string { return u }},
		{name: "bare origin with trailing slash", baseURL: func(u string) string { return u + "/" }},
		{name: "versioned root", baseURL: func(u string) string { return u + "/v1" }},
		{name: "versioned root with trailing slash", baseURL: func(u string) string { return u + "/v1/" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":          "msg_test",
					"type":        "message",
					"role":        "assistant",
					"model":       "claude-opus-5",
					"content":     []map[string]any{{"type": "text", "text": "ok"}},
					"stop_reason": "end_turn",
					"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
				})
			}))
			defer srv.Close()

			model := NewSDKChatModel(SDKModelConfig{
				ModelID:    "claude-opus-5",
				ClientType: string(ClientTypeAnthropicMessages),
				BaseURL:    tt.baseURL(srv.URL),
				APIKey:     "test-key",
			})

			if _, err := sdk.GenerateTextResult(
				context.Background(),
				sdk.WithModel(model),
				sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}),
			); err != nil {
				t.Fatalf("generate text: %v", err)
			}

			if gotPath != "/v1/messages" {
				t.Fatalf("request path: got %q, want %q", gotPath, "/v1/messages")
			}
		})
	}
}

func TestNewSDKProviderAnthropicMessagesBaseURLGainsV1(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "claude-opus-5", "display_name": "Claude Opus 5"}},
		})
	}))
	defer srv.Close()

	provider := NewSDKProvider(srv.URL, "test-key", "", ClientTypeAnthropicMessages, time.Second, nil)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("list models: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("request path: got %q, want %q", gotPath, "/v1/models")
	}
}

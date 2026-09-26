package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/felinics/twilight/sdk"
)

func TestAnthropicSDKUsageIncludesCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"id":"msg_usage","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":200,"cache_creation_input_tokens":100}}`); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()
	model := NewSDKChatModel(SDKModelConfig{
		ModelID: "claude-test", ClientType: string(ClientTypeAnthropicMessages),
		APIKey: "test-key", BaseURL: server.URL,
	})
	result, err := model.Provider.DoGenerate(context.Background(), sdk.GenerateParams{
		Model: model, Messages: []sdk.Message{sdk.UserMessage("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sdk.Usage{
		InputTokens: 310, OutputTokens: 5, TotalTokens: 315, CachedInputTokens: 200,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10, CacheReadTokens: 200, CacheWriteTokens: 100},
	}
	if result.Usage != want {
		t.Fatalf("usage = %+v, want %+v; requires the normalized Twilight dependency", result.Usage, want)
	}
}

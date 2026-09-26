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
	cases := []struct {
		name  string
		usage string
		want  sdk.Usage
	}{
		{
			name:  "uncached",
			usage: `{"input_tokens":10,"output_tokens":5}`,
			want: sdk.Usage{
				InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10},
			},
		},
		{
			name:  "cache read",
			usage: `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":200}`,
			want: sdk.Usage{
				InputTokens: 210, OutputTokens: 5, TotalTokens: 215, CachedInputTokens: 200,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10, CacheReadTokens: 200},
			},
		},
		{
			name:  "cache write",
			usage: `{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":100}`,
			want: sdk.Usage{
				InputTokens: 110, OutputTokens: 5, TotalTokens: 115,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10, CacheWriteTokens: 100},
			},
		},
		{
			name:  "cache read and write",
			usage: `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":200,"cache_creation_input_tokens":100}`,
			want: sdk.Usage{
				InputTokens: 310, OutputTokens: 5, TotalTokens: 315, CachedInputTokens: 200,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10, CacheReadTokens: 200, CacheWriteTokens: 100},
			},
		},
		{
			name:  "mixed cache lifetimes",
			usage: `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":200,"cache_creation_input_tokens":556,"cache_creation":{"ephemeral_5m_input_tokens":456,"ephemeral_1h_input_tokens":100}}`,
			want: sdk.Usage{
				InputTokens: 766, OutputTokens: 5, TotalTokens: 771, CachedInputTokens: 200,
				InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 10, CacheReadTokens: 200, CacheWriteTokens: 556, CacheWrite5mTokens: 456, CacheWrite1hTokens: 100},
			},
		},
		{
			name:  "fully cached",
			usage: `{"input_tokens":0,"output_tokens":5,"cache_read_input_tokens":200}`,
			want: sdk.Usage{
				InputTokens: 200, OutputTokens: 5, TotalTokens: 205, CachedInputTokens: 200,
				InputTokenDetails: sdk.InputTokenDetail{CacheReadTokens: 200},
			},
		},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprintf(w, `{"id":"msg_usage","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":%s}`, tc.usage)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"role\":\"assistant\",\"content\":[],\"usage\":%s}}\n\n", tc.usage)
					_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}))
				defer srv.Close()
				model := NewSDKChatModel(SDKModelConfig{ModelID: "claude-test", ClientType: string(ClientTypeAnthropicMessages), APIKey: "test-key", BaseURL: srv.URL})
				provider := model.Provider
				params := sdk.GenerateParams{Model: model, Messages: []sdk.Message{sdk.UserMessage("hi")}}
				if !stream {
					result, err := provider.DoGenerate(context.Background(), params)
					if err != nil {
						t.Fatal(err)
					}
					if result.Usage != tc.want {
						t.Fatalf("usage = %+v, want %+v", result.Usage, tc.want)
					}
					return
				}
				result, err := provider.DoStream(context.Background(), params)
				if err != nil {
					t.Fatal(err)
				}
				var stepFinished, finished bool
				for part := range result.Stream {
					switch p := part.(type) {
					case *sdk.ErrorPart:
						t.Fatalf("stream error: %+v", p)
					case *sdk.FinishStepPart:
						stepFinished = true
						if p.Usage != tc.want {
							t.Errorf("step usage = %+v, want %+v", p.Usage, tc.want)
						}
					case *sdk.FinishPart:
						finished = true
						if p.TotalUsage != tc.want {
							t.Errorf("total usage = %+v, want %+v", p.TotalUsage, tc.want)
						}
					}
				}
				if !stepFinished || !finished {
					t.Fatal("missing finish events")
				}
			})
		}
	}
}

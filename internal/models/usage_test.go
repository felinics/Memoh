package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
					_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"role\":\"assistant\",\"content\":[],\"usage\":%s}}\n\n", strings.Replace(tc.usage, `"output_tokens":5`, `"output_tokens":0`, 1))
					_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}))
				defer srv.Close()
				model := NewSDKChatModel(SDKModelConfig{ModelID: "claude-test", ClientType: string(ClientTypeAnthropicMessages), APIKey: "test-key", BaseURL: srv.URL})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				want := tc.want
				want.CacheReadTokensReported = strings.Contains(tc.usage, `"cache_read_input_tokens"`)
				params := sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}
				if !stream {
					result, err := model.Generate(ctx, params)
					if err != nil {
						t.Fatal(err)
					}
					if result.Usage != want {
						t.Fatalf("usage = %+v, want %+v", result.Usage, want)
					}
					return
				}
				result, err := model.Stream(ctx, params)
				if err != nil {
					t.Fatal(err)
				}
				var steps, finishes int
				for part := range result.Parts {
					switch p := part.(type) {
					case *sdk.ErrorPart:
						t.Fatalf("stream error: %+v", p)
					case *sdk.FinishStepPart:
						steps++
						if p.Usage != want {
							t.Errorf("step usage = %+v, want %+v", p.Usage, want)
						}
					case *sdk.FinishPart:
						finishes++
						if p.TotalUsage != want {
							t.Errorf("total usage = %+v, want %+v", p.TotalUsage, want)
						}
					}
				}
				assembled, err := result.Result()
				if err != nil || assembled.Usage != want {
					t.Fatalf("assembled usage=%+v err=%v want=%+v", assembled, err, want)
				}
				if steps != 1 || finishes != 1 {
					t.Fatalf("finish events: steps=%d finishes=%d", steps, finishes)
				}
			})
		}
	}
}

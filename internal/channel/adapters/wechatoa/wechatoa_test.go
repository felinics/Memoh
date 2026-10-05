package wechatoa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestOutboundStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const secret = "wechatoa-secret-value-123456"

	cases := []struct {
		name  string
		event channel.PreparedStreamEvent
		want  []string
	}{
		{
			name:  "coded error shows the copy as it is",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
			want:  []string{"The workspace is unreachable."},
		},
		{
			name:  "uncoded error is redacted and labelled",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "request failed with token " + secret},
			want:  []string{"Error: request failed with token " + strings.Repeat("*", len(secret))},
		},
		{
			name:  "blank error sends nothing",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "  "},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewWeChatOAAdapter(nil)
			cfg := channel.ChannelConfig{ID: "cfg-1", Credentials: map[string]any{
				"appId": "test-app", "appSecret": "app-secret", "token": "webhook-token", "encryptionMode": "plain",
			}}
			client, err := adapter.clientForConfig(cfg.Credentials)
			if err != nil {
				t.Fatal(err)
			}
			redact.SetSecrets("wechatoa-stream-test", secret)
			var sent []string
			client.http = &http.Client{Transport: tokenTestTransport(func(req *http.Request) (*http.Response, error) {
				body := `{"access_token":"valid-token","expires_in":7200}`
				if strings.Contains(req.URL.Path, "/message/custom/send") {
					var payload struct {
						Text struct {
							Content string `json:"content"`
						} `json:"text"`
					}
					raw, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(raw, &payload)
					sent = append(sent, payload.Text.Content)
					body = `{"errcode":0}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}

			stream, err := adapter.OpenStream(context.Background(), cfg, "openid:user-1", channel.StreamOptions{})
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "draft"}); err != nil {
				t.Fatalf("Push delta: %v", err)
			}
			if err := stream.Push(context.Background(), tc.event); err != nil {
				t.Fatalf("Push error: %v", err)
			}
			if err := stream.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if len(sent) != len(tc.want) || strings.Join(sent, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("sent messages = %q, want %q", sent, tc.want)
			}
		})
	}
}

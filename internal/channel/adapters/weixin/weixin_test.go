package weixin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestWeixinAdapter_Type(t *testing.T) {
	adapter := NewWeixinAdapter(nil)
	if adapter.Type() != Type {
		t.Errorf("Type() = %v, want %v", adapter.Type(), Type)
	}
}

func TestWeixinAdapter_Descriptor(t *testing.T) {
	adapter := NewWeixinAdapter(nil)
	desc := adapter.Descriptor()

	if desc.Type != Type {
		t.Errorf("desc.Type = %v", desc.Type)
	}
	if desc.DisplayName != "WeChat" {
		t.Errorf("desc.DisplayName = %q", desc.DisplayName)
	}
	// The channel is owner-only (iLink ClawBot lives inside the activator's
	// own WeChat), so it must stay marked OwnerOnly.
	if !desc.OwnerOnly {
		t.Error("weixin descriptor should be OwnerOnly")
	}
	if !desc.Capabilities.Text {
		t.Error("should support text")
	}
	if !desc.Capabilities.Media {
		t.Error("should support media")
	}
	if !desc.Capabilities.Attachments {
		t.Error("should support attachments")
	}
	if len(desc.Capabilities.ChatTypes) != 1 || desc.Capabilities.ChatTypes[0] != channel.ConversationTypePrivate {
		t.Errorf("chat types = %v", desc.Capabilities.ChatTypes)
	}

	if _, ok := desc.ConfigSchema.Fields["token"]; !ok {
		t.Error("config schema should have 'token' field")
	}
	if desc.ConfigSchema.Fields["token"].Type != channel.FieldSecret {
		t.Error("token field should be secret")
	}
	if !desc.ConfigSchema.Fields["token"].Required {
		t.Error("token field should be required")
	}
}

func TestWeixinAdapter_Interfaces(_ *testing.T) {
	adapter := NewWeixinAdapter(nil)

	// Adapter
	var _ channel.Adapter = adapter
	// ConfigNormalizer
	var _ channel.ConfigNormalizer = adapter
	// TargetResolver
	var _ channel.TargetResolver = adapter
	// BindingMatcher
	var _ channel.BindingMatcher = adapter
	// Receiver
	var _ channel.Receiver = adapter
	// Sender
	var _ channel.Sender = adapter
	// AttachmentResolver
	var _ channel.AttachmentResolver = adapter
	// ProcessingStatusNotifier
	var _ channel.ProcessingStatusNotifier = adapter
}

func TestWeixinBlockStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const secret = "weixin-secret-value-123456"
	redact.SetSecrets("weixin-stream-test", secret)

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
			var (
				mu   sync.Mutex
				sent []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req SendMessageRequest
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &req)
				mu.Lock()
				for _, item := range req.Msg.ItemList {
					if item.TextItem != nil {
						sent = append(sent, item.TextItem.Text)
					}
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ret":0}`))
			}))
			t.Cleanup(server.Close)

			adapter := NewWeixinAdapter(nil)
			cfg := channel.ChannelConfig{ID: "cfg-1", Credentials: map[string]any{"token": "bot-token", "baseUrl": server.URL}}
			adapter.contextCache.Put(cfg.ID+":user-1", "context-token")
			stream, err := adapter.OpenStream(context.Background(), cfg, "user-1", channel.StreamOptions{})
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
			mu.Lock()
			defer mu.Unlock()
			if len(sent) != len(tc.want) || strings.Join(sent, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("sent messages = %q, want %q", sent, tc.want)
			}
		})
	}
}

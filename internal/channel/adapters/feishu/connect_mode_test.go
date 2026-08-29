package feishu

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestConnectWebhookModeDoesNotStartWebsocket(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	cfg := channel.ChannelConfig{
		ID:          "cfg-1",
		BotID:       "bot-1",
		ChannelType: Type,
		Credentials: map[string]any{
			"app_id":             "app",
			"app_secret":         "secret",
			"verification_token": "verify-token",
			"inbound_mode":       "webhook",
		},
	}
	conn, err := adapter.Connect(context.Background(), cfg, func(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	if !conn.Running() {
		t.Fatal("expected connection to be running")
	}
	if err := conn.Stop(context.Background()); err != nil {
		t.Fatalf("expected stop to succeed, got %v", err)
	}
}

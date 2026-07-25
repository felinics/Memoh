package line

import (
	"context"
	"testing"
)

func TestSetWebhookEndpoint(t *testing.T) {
	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	err := adapter.SetWebhookEndpoint(context.Background(), map[string]any{
		"channel_secret":       "secret",
		"channel_access_token": "token",
	}, "https://example.com/channels/line/webhook/config-id")
	if err != nil {
		t.Fatalf("SetWebhookEndpoint returned error: %v", err)
	}
	if client.webhookEndpoint != "https://example.com/channels/line/webhook/config-id" {
		t.Fatalf("webhook endpoint = %q", client.webhookEndpoint)
	}
}

//go:build integration

package line

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoverSelfIntegration(t *testing.T) {
	secret := strings.TrimSpace(os.Getenv("LINE_CHANNEL_SECRET"))
	token := strings.TrimSpace(os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"))
	if secret == "" || token == "" {
		t.Skip("set LINE_CHANNEL_SECRET and LINE_CHANNEL_ACCESS_TOKEN to run LINE integration test")
	}

	adapter := NewAdapter(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	identity, externalID, err := adapter.DiscoverSelf(ctx, map[string]any{
		configKeyChannelSecret:      secret,
		configKeyChannelAccessToken: token,
	})
	if err != nil {
		t.Fatalf("DiscoverSelf returned error: %v", err)
	}
	if strings.TrimSpace(externalID) == "" {
		t.Fatal("DiscoverSelf returned empty external ID")
	}
	if got := strings.TrimSpace(channelString(identity, "bot_user_id")); got == "" || got != externalID {
		t.Fatalf("bot_user_id = %q, externalID = %q", got, externalID)
	}
}

func channelString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return value
}

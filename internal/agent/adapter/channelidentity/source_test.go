package channelidentity

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

type fakeConfigSource struct {
	configs []channel.ChannelConfig
}

func (f fakeConfigSource) ListBotConfigs(context.Context, string) ([]channel.ChannelConfig, error) {
	return f.configs, nil
}

func TestSourceProjectsChannelConfig(t *testing.T) {
	t.Parallel()

	source := NewSource(fakeConfigSource{configs: []channel.ChannelConfig{{
		ID:               "config-1",
		ChannelType:      channel.ChannelTypeTelegram,
		ExternalIdentity: "bot-1",
		SelfIdentity:     map[string]any{"username": "memoh"},
	}}})
	got, err := source.ListPlatformIdentities(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("ListPlatformIdentities: %v", err)
	}
	if len(got) != 1 || got[0].Platform != "telegram" || got[0].ExternalIdentity != "bot-1" {
		t.Fatalf("unexpected projection: %#v", got)
	}
}

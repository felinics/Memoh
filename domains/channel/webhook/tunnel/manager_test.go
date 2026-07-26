package tunnel

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/webhook"
	"github.com/memohai/memoh/internal/config"
)

func TestNewManagerReturnsAdapterMappingStatus(t *testing.T) {
	mgr := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:          config.WebhookTunnelModeDisabled,
			PublicBaseURL: "https://Memoh.EXAMPLE.org/",
		},
	})
	if _, ok := mgr.(*manager); !ok {
		t.Fatalf("NewManager type = %T, want *manager adapter", mgr)
	}
	got := mgr.Status()
	want := webhook.Status{
		Enabled:       true,
		Mode:          config.WebhookTunnelModeDisabled,
		Status:        webhook.StatusReady,
		PublicBaseURL: "https://memoh.example.org",
	}
	if got != want {
		t.Fatalf("Status() = %+v, want %+v", got, want)
	}
	if got := mgr.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL() = %q", got)
	}
}

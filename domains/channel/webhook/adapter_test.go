package webhook

import (
	"testing"

	"github.com/memohai/memoh/internal/config"
)

func TestNewManagerReturnsAdapterMappingStatus(t *testing.T) {
	t.Parallel()

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
	want := Status{
		Enabled:       true,
		Mode:          config.WebhookTunnelModeDisabled,
		Status:        StatusReady,
		PublicBaseURL: "https://memoh.example.org",
	}
	if got != want {
		t.Fatalf("Status() = %+v, want %+v", got, want)
	}
	if got := mgr.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL() = %q", got)
	}
}

func TestNormalizeConfiguredPublicBase(t *testing.T) {
	t.Parallel()

	got, err := NormalizeConfiguredPublicBase("https://Memoh.EXAMPLE.org/")
	if err != nil {
		t.Fatalf("NormalizeConfiguredPublicBase returned error: %v", err)
	}
	if got != "https://memoh.example.org" {
		t.Fatalf("NormalizeConfiguredPublicBase = %q", got)
	}
}

func TestNormalizeConfiguredPublicBaseRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"http://memoh.example.org",
		"https://localhost",
		"https://127.0.0.1",
		"https://memoh.example.org/app",
		"https://memoh.example.org:8443",
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := NormalizeConfiguredPublicBase(raw); err == nil {
				t.Fatalf("expected %q to fail", raw)
			}
		})
	}
}

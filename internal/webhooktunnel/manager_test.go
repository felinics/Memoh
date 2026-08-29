package webhooktunnel

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/config"
)

func TestNormalizePublicBase(t *testing.T) {
	t.Parallel()

	got, err := normalizePublicBase("abc.trycloudflare.com")
	if err != nil {
		t.Fatalf("normalizePublicBase returned error: %v", err)
	}
	if got != "https://abc.trycloudflare.com" {
		t.Fatalf("normalizePublicBase = %q", got)
	}

	got, err = normalizePublicBase("https://abc.trycloudflare.com/")
	if err != nil {
		t.Fatalf("normalizePublicBase returned error: %v", err)
	}
	if got != "https://abc.trycloudflare.com" {
		t.Fatalf("normalizePublicBase trims slash = %q", got)
	}
}

func TestNormalizePublicBaseRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	if _, err := normalizePublicBase("http://abc.trycloudflare.com"); err == nil {
		t.Fatal("expected non-HTTPS URL to fail")
	}
}

func TestNormalizePublicBaseRejectsUnsafeHosts(t *testing.T) {
	t.Parallel()

	tests := []string{
		"https://127.0.0.1",
		"https://[::1]",
		"https://user:pass@abc.trycloudflare.com",
		"https://abc.trycloudflare.com/path",
		"https://abc.trycloudflare.com?x=1",
		"https://abc.trycloudflare.com#frag",
		"https://abc.trycloudflare.com:8443",
		"https://trycloudflare.com",
		"https://abc.example.com",
	}
	for _, raw := range tests {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizePublicBase(raw); err == nil {
				t.Fatalf("expected %q to fail", raw)
			}
		})
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
		"https://[2001:4860:4860::8888]",
		"https://192.168.1.10",
		"https://100.64.0.1",
		"https://192.0.2.1",
		"https://198.51.100.1",
		"https://203.0.113.1",
		"https://memoh.local",
		"https://memoh.internal",
		"https://user:pass@memoh.example.org",
		"https://memoh.example.org/app",
		"https://memoh.example.org:443",
		"https://memoh.example.org:8443",
		"https://memoh.example.org?x=1",
		"https://memoh.example.org#frag",
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

func TestErrorAndStoppedStatusClearPublicBaseURL(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	})
	m.setReady("https://abc.trycloudflare.com")
	m.setError(assertErr("boom"))
	if got := m.Status(); got.PublicBaseURL != "" || got.Status != StatusError {
		t.Fatalf("error status = %+v, want no public base", got)
	}
	m.setReady("https://abc.trycloudflare.com")
	m.markStopped()
	if got := m.Status(); got.PublicBaseURL != "" || got.Status != StatusStopped {
		t.Fatalf("stopped status = %+v, want no public base", got)
	}
}

func TestErrorStatusDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	})
	m.setError(assertErr("dial tcp 10.0.0.5:18735: connect: connection refused"))
	got := m.Status()
	if got.Error != "webhook tunnel unavailable" {
		t.Fatalf("error = %q, want sanitized message", got.Error)
	}
	if strings.Contains(got.Error, "10.0.0.5") || strings.Contains(got.Error, "18735") {
		t.Fatalf("error leaked internal detail: %q", got.Error)
	}
}

func TestPollErrorPreservesReadyPublicBaseURL(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{Mode: config.WebhookTunnelModeExternal},
	})
	m.setReady("https://abc.trycloudflare.com")
	m.setPollError(assertErr("temporary metrics failure"))
	got := m.Status()
	if got.Status != StatusReady || got.PublicBaseURL != "https://abc.trycloudflare.com" {
		t.Fatalf("status after poll error = %+v, want ready with existing public base", got)
	}
}

func TestConfiguredPublicBaseURLTakesPrecedence(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:          config.WebhookTunnelModeDisabled,
			PublicBaseURL: "https://memoh.example.org/",
		},
	})
	if got := m.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL = %q", got)
	}
	status := m.Status()
	if !status.Enabled || status.Status != StatusReady || status.PublicBaseURL != "https://memoh.example.org" {
		t.Fatalf("Status = %+v, want configured public base ready", status)
	}

	m.setReady("https://abc.trycloudflare.com")
	if got := m.PublicBaseURL(); got != "https://memoh.example.org" {
		t.Fatalf("PublicBaseURL with tunnel ready = %q, want configured base", got)
	}
}

func TestConfiguredPublicBaseURLStatusOverridesTunnelError(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:          config.WebhookTunnelModeExternal,
			PublicBaseURL: "https://memoh.example.org",
		},
	})
	m.setError(assertErr("metrics unavailable"))
	status := m.Status()
	if status.Status != StatusReady || status.Error != "" || status.PublicBaseURL != "https://memoh.example.org" {
		t.Fatalf("Status = %+v, want configured public base ready", status)
	}
}

func TestTargetURLDefaultsToWebhookTunnelListenAddr(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:       config.WebhookTunnelModeManaged,
			ListenAddr: ":18734",
		},
	})
	got, err := m.targetURL()
	if err != nil {
		t.Fatalf("targetURL returned error: %v", err)
	}
	if got != "http://127.0.0.1:18734" {
		t.Fatalf("targetURL = %q", got)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestTargetURLHonorsExplicitTarget(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, config.Config{
		WebhookTunnel: config.WebhookTunnelConfig{
			Mode:      config.WebhookTunnelModeManaged,
			TargetURL: "http://127.0.0.1:9999",
		},
	})
	got, err := m.targetURL()
	if err != nil {
		t.Fatalf("targetURL returned error: %v", err)
	}
	if got != "http://127.0.0.1:9999" {
		t.Fatalf("targetURL = %q", got)
	}
}

package webhook

import (
	"testing"
)

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

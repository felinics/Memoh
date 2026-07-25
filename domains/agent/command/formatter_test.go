package command

import (
	"strings"
	"testing"
)

func TestIsMachineToken(t *testing.T) {
	t.Parallel()
	coded := []string{
		"anthropic/claude-opus-4.7", // namespaced slug
		"/srv/data/file.txt",        // path
		"0 9 * * *",                 // cron (metachar guard)
		"create_issue",              // underscore (metachar guard)
		"a*b",                       // star (metachar guard)
		"ops@acme.com",              // email
		"9f3ec7a2-1b2c-4d5e-aaaa",   // long opaque id
		"verylongtokenname",         // >=12 chars, no space
	}
	for _, v := range coded {
		if !isMachineToken(v) {
			t.Errorf("isMachineToken(%q) = false, want true", v)
		}
	}
	plain := []string{
		"yes", "no", "on", "off", "(none)", "stdio", "http",
		"connected", "Connected", "Error", "Allowed", "member",
		"5 min", "2h ago", "3.2s", "12.4K", "10%", "42", "gpt-4o",
		"daily at 09:00", "Send the morning summary",
	}
	for _, v := range plain {
		if isMachineToken(v) {
			t.Errorf("isMachineToken(%q) = true, want false", v)
		}
	}
}

func TestRenderValue(t *testing.T) {
	t.Parallel()
	if got := renderValue("yes"); got != "yes" {
		t.Errorf("renderValue(yes) = %q, want plain", got)
	}
	if got := renderValue("anthropic/claude"); got != "`anthropic/claude`" {
		t.Errorf("renderValue(slug) = %q, want code-wrapped", got)
	}
	// Metachar values must be code-wrapped to survive the Telegram renderer.
	if got := renderValue("0 9 * * *"); !strings.HasPrefix(got, "`") {
		t.Errorf("renderValue(cron) = %q, want code-wrapped (italic-corruption guard)", got)
	}
}

func TestFormatKVTitled(t *testing.T) {
	t.Parallel()
	got := formatKVTitled("github", []kv{{"Type", "http"}})
	if !strings.HasPrefix(got, "**github**\n\n") {
		t.Errorf("expected bold title header, got: %q", got)
	}
	if !strings.Contains(got, "- Type: http") {
		t.Errorf("expected detail body under title, got: %q", got)
	}
	// Empty title degrades to a plain KV block (no stray bold markers).
	if got := formatKVTitled("", []kv{{"A", "b"}}); strings.Contains(got, "**") {
		t.Errorf("empty title should not emit bold markers, got: %q", got)
	}
}

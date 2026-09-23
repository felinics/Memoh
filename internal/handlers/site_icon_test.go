package handlers

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestDiscoverIconTheme(t *testing.T) {
	page, _ := url.Parse("https://example.com/docs/page")
	source := `<head><base href="/assets/"><link rel="icon" href="fallback.ico"><link rel="icon" media="(prefers-color-scheme: light)" href="light.svg"><link rel="icon" media="(prefers-color-scheme: dark)" href="dark.svg"></head>`
	for _, theme := range []string{"light", "dark"} {
		if got := discoverIcon(strings.NewReader(source), page, theme); got != "https://example.com/assets/"+theme+".svg" {
			t.Fatalf("%s: %s", theme, got)
		}
	}
	if got := discoverIcon(strings.NewReader(`<link rel="icon" href="javascript:alert(1)">`), page, "dark"); got != "https://example.com/favicon.ico" {
		t.Fatal(got)
	}
	if got := discoverIcon(strings.NewReader(`<link rel="icon" href="a.ico"><link rel="icon" type="image/svg+xml" href="adaptive.svg">`), page, "dark"); got != "https://example.com/docs/adaptive.svg" {
		t.Fatal(got)
	}
}

func TestIconIPRestrictions(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "0.0.0.0"} {
		if publicIconIP(net.ParseIP(raw)) {
			t.Fatal(raw)
		}
	}
	if !publicIconIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
}

func TestDiscoverGitHubScriptTheme(t *testing.T) {
	page, _ := url.Parse("https://github.com/")
	source := `<link rel="icon" type="image/svg+xml" href="https://github.githubassets.com/favicons/favicon.svg" data-base-href="https://github.githubassets.com/favicons/favicon">`
	for theme, suffix := range map[string]string{"light": ".svg", "dark": "-dark.svg"} {
		if got := discoverIcon(strings.NewReader(source), page, theme); got != "https://github.githubassets.com/favicons/favicon"+suffix {
			t.Fatal(got)
		}
	}
}

func TestIconURLRestrictions(t *testing.T) {
	for _, raw := range []string{"http://localhost/a", "http://127.0.0.1/a", "http://[::1]/a", "https://user:pass@example.com/a", "javascript:alert(1)"} {
		u, _ := url.Parse(raw)
		if validIconURL(u) {
			t.Fatal(raw)
		}
	}
}

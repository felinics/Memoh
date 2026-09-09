package weixin

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestParseConfig(t *testing.T) {
	t.Run("valid minimal", func(t *testing.T) {
		cfg, err := parseConfig(map[string]any{"token": "abc123"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Token != "abc123" {
			t.Errorf("token = %q, want %q", cfg.Token, "abc123")
		}
		if cfg.BaseURL != defaultBaseURL {
			t.Errorf("baseURL = %q, want %q", cfg.BaseURL, defaultBaseURL)
		}
		if cfg.CDNBaseURL != defaultCDNBaseURL {
			t.Errorf("cdnBaseURL = %q, want %q", cfg.CDNBaseURL, defaultCDNBaseURL)
		}
	})

	t.Run("valid full", func(t *testing.T) {
		cfg, err := parseConfig(map[string]any{
			"token":              "tok",
			"baseUrl":            "https://example.com",
			"pollTimeoutSeconds": 60,
			"enableTyping":       true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BaseURL != "https://example.com" {
			t.Errorf("baseURL = %q", cfg.BaseURL)
		}
		if cfg.CDNBaseURL != defaultCDNBaseURL {
			t.Errorf("cdnBaseURL = %q, want %q", cfg.CDNBaseURL, defaultCDNBaseURL)
		}
		if cfg.PollTimeoutSeconds != 60 {
			t.Errorf("pollTimeout = %d", cfg.PollTimeoutSeconds)
		}
		if !cfg.EnableTyping {
			t.Error("enableTyping should be true")
		}
	})

	t.Run("missing token", func(t *testing.T) {
		_, err := parseConfig(map[string]any{"baseUrl": "https://example.com"})
		if err == nil {
			t.Fatal("expected error for missing token")
		}
	})

	t.Run("snake_case keys", func(t *testing.T) {
		cfg, err := parseConfig(map[string]any{
			"token":                "tok",
			"base_url":             "https://alt.com",
			"poll_timeout_seconds": 45,
			"enable_typing":        true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.BaseURL != "https://alt.com" {
			t.Errorf("baseURL = %q", cfg.BaseURL)
		}
		if cfg.CDNBaseURL != defaultCDNBaseURL {
			t.Errorf("cdnBaseURL = %q", cfg.CDNBaseURL)
		}
		if cfg.PollTimeoutSeconds != 45 {
			t.Errorf("pollTimeout = %d", cfg.PollTimeoutSeconds)
		}
		if !cfg.EnableTyping {
			t.Error("enableTyping should be true")
		}
	})
}

func TestNormalizeConfig(t *testing.T) {
	out, err := normalizeConfig(map[string]any{
		"token":   "tok",
		"baseUrl": "https://example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["token"] != "tok" {
		t.Errorf("token = %v", out["token"])
	}
	if out["baseUrl"] != "https://example.com" {
		t.Errorf("baseUrl = %v", out["baseUrl"])
	}
	if _, has := out["cdnBaseUrl"]; has {
		t.Error("cdnBaseUrl should not be in normalized output")
	}
}

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc@im.wechat", "abc@im.wechat"},
		{"weixin:abc@im.wechat", "abc@im.wechat"},
		{" weixin: abc@im.wechat ", "abc@im.wechat"},
		{"", ""},
		{"  ", ""},
	}
	for _, tc := range tests {
		got := normalizeTarget(tc.input)
		if got != tc.want {
			t.Errorf("normalizeTarget(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeUserConfig(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		out, err := normalizeUserConfig(map[string]any{"user_id": "u1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["user_id"] != "u1" {
			t.Errorf("user_id = %v", out["user_id"])
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, err := normalizeUserConfig(map[string]any{})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestResolveTarget(t *testing.T) {
	target, err := resolveTarget(map[string]any{"user_id": "u1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "u1" {
		t.Errorf("target = %q", target)
	}
}

func TestMatchBinding(t *testing.T) {
	config := map[string]any{"user_id": "u1"}
	if !matchBinding(config, channel.BindingCriteria{SubjectID: "u1"}) {
		t.Error("should match by subject_id")
	}
	if matchBinding(config, channel.BindingCriteria{SubjectID: "u2"}) {
		t.Error("should not match different subject_id")
	}
}

func TestBuildUserConfig(t *testing.T) {
	id := channel.Identity{SubjectID: "u1"}
	out := buildUserConfig(id)
	if out["user_id"] != "u1" {
		t.Errorf("user_id = %v", out["user_id"])
	}
}

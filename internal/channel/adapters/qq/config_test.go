package qq

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	got, err := normalizeConfig(map[string]any{
		"app_id":            "1024",
		"client_secret":     "secret",
		"markdown_support":  true,
		"enable_input_hint": false,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["appId"] != "1024" {
		t.Fatalf("unexpected appId: %#v", got["appId"])
	}
	if got["clientSecret"] != "secret" {
		t.Fatalf("unexpected clientSecret: %#v", got["clientSecret"])
	}
	if got["markdownSupport"] != true {
		t.Fatalf("unexpected markdownSupport: %#v", got["markdownSupport"])
	}
	if got["enableInputHint"] != false {
		t.Fatalf("unexpected enableInputHint: %#v", got["enableInputHint"])
	}
}

func TestNormalizeConfigRequiresSecrets(t *testing.T) {
	t.Parallel()

	if _, err := normalizeConfig(map[string]any{
		"client_secret": "secret",
	}); err == nil {
		t.Fatal("expected appId validation error")
	}
	if _, err := normalizeConfig(map[string]any{
		"app_id": "1024",
	}); err == nil {
		t.Fatal("expected clientSecret validation error")
	}
}

func TestNormalizeUserConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  map[string]any
		want map[string]any
	}{
		{
			name: "explicit target",
			raw: map[string]any{
				"target_type": "group",
				"target_id":   "group-openid",
			},
			want: map[string]any{
				"target_type": "group",
				"target_id":   "group-openid",
			},
		},
		{
			name: "user openid alias",
			raw: map[string]any{
				"user_openid": "user-openid",
			},
			want: map[string]any{
				"target_type": "c2c",
				"target_id":   "user-openid",
			},
		},
		{
			name: "channel id alias",
			raw: map[string]any{
				"channel_id": "12345",
			},
			want: map[string]any{
				"target_type": "channel",
				"target_id":   "12345",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeUserConfig(tt.raw)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got["target_type"] != tt.want["target_type"] {
				t.Fatalf("unexpected target_type: %#v", got["target_type"])
			}
			if got["target_id"] != tt.want["target_id"] {
				t.Fatalf("unexpected target_id: %#v", got["target_id"])
			}
		})
	}
}

func TestNormalizeUserConfigRequiresTarget(t *testing.T) {
	t.Parallel()

	if _, err := normalizeUserConfig(map[string]any{}); err == nil {
		t.Fatal("expected target validation error")
	}
}

func TestResolveTarget(t *testing.T) {
	t.Parallel()

	target, err := resolveTarget(map[string]any{
		"target_type": "c2c",
		"target_id":   "user-openid",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if target != "c2c:user-openid" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestNormalizeTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "qq:group:abc", want: "group:abc"},
		{input: "qqbot:c2c:USER1", want: "c2c:USER1"},
		{input: "channel:123", want: "channel:123"},
		{input: "00112233445566778899AABBCCDDEEFF", want: "c2c:00112233445566778899AABBCCDDEEFF"},
	}

	for _, tt := range tests {
		if got := normalizeTarget(tt.input); got != tt.want {
			t.Fatalf("normalizeTarget(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMatchBinding(t *testing.T) {
	t.Parallel()

	config := map[string]any{
		"target_type": "c2c",
		"target_id":   "user-openid",
	}

	if !matchBinding(config, channel.BindingCriteria{
		SubjectID: "user-openid",
	}) {
		t.Fatal("expected subject match")
	}

	if !matchBinding(config, channel.BindingCriteria{
		Attributes: map[string]string{"user_openid": "user-openid"},
	}) {
		t.Fatal("expected user_openid match")
	}

	if matchBinding(config, channel.BindingCriteria{
		SubjectID: "other-user",
	}) {
		t.Fatal("unexpected mismatch")
	}
}

func TestBuildUserConfig(t *testing.T) {
	t.Parallel()

	got := buildUserConfig(channel.Identity{
		SubjectID: "user-openid",
	})

	if got["target_type"] != "c2c" {
		t.Fatalf("unexpected target_type: %#v", got["target_type"])
	}
	if got["target_id"] != "user-openid" {
		t.Fatalf("unexpected target_id: %#v", got["target_id"])
	}
}

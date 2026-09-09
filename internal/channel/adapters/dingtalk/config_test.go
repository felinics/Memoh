package dingtalk

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestNormalizeConfigSupportsCamelAndSnakeKeys(t *testing.T) {
	got, err := normalizeConfig(map[string]any{
		"app_key":    " app-key ",
		"app_secret": " secret ",
	})
	if err != nil {
		t.Fatalf("normalizeConfig error = %v", err)
	}
	if got["appKey"] != "app-key" || got["appSecret"] != "secret" {
		t.Fatalf("unexpected normalized config: %#v", got)
	}
}

func TestNormalizeConfigRequiresCredentials(t *testing.T) {
	if _, err := normalizeConfig(map[string]any{"appKey": "app"}); err == nil {
		t.Fatal("expected missing appSecret error")
	}
}

func TestNormalizeUserConfigAndResolveTarget(t *testing.T) {
	raw := map[string]any{
		"userId":             " user-1 ",
		"openConversationId": " group-1 ",
		"displayName":        " Alice ",
	}
	got, err := normalizeUserConfig(raw)
	if err != nil {
		t.Fatalf("normalizeUserConfig error = %v", err)
	}
	if got["user_id"] != "user-1" || got["open_conversation_id"] != "group-1" || got["display_name"] != "Alice" {
		t.Fatalf("unexpected normalized user config: %#v", got)
	}

	target, err := resolveTarget(raw)
	if err != nil {
		t.Fatalf("resolveTarget error = %v", err)
	}
	if target != "group:group-1" {
		t.Fatalf("target = %q, want group:group-1", target)
	}
}

func TestNormalizeUserConfigRequiresDeliveryTarget(t *testing.T) {
	if _, err := normalizeUserConfig(map[string]any{"displayName": "Alice"}); err == nil {
		t.Fatal("expected missing delivery target error")
	}
}

func TestNormalizeTarget(t *testing.T) {
	tests := map[string]string{
		" user:alice ":                    "user:alice",
		"user_id:bob":                     "user:bob",
		"group:cid-1":                     "group:cid-1",
		"open_conversation_id:cid-2":      "group:cid-2",
		"bare-user-id":                    "user:bare-user-id",
		"open_conversation_id:   cid-3  ": "group:cid-3",
	}

	for input, want := range tests {
		if got := normalizeTarget(input); got != want {
			t.Fatalf("normalizeTarget(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMatchBindingAndBuildUserConfig(t *testing.T) {
	binding := map[string]any{
		"user_id":              "staff-1",
		"open_conversation_id": "group-1",
	}
	if !matchBinding(binding, channel.BindingCriteria{Attributes: map[string]string{"staff_id": "staff-1"}}) {
		t.Fatal("expected staff_id to match user binding")
	}
	if !matchBinding(binding, channel.BindingCriteria{SubjectID: "group-1"}) {
		t.Fatal("expected subject id to match group binding")
	}
	if matchBinding(binding, channel.BindingCriteria{SubjectID: "other"}) {
		t.Fatal("did not expect unrelated subject to match")
	}

	got := buildUserConfig(channel.Identity{
		DisplayName: "Alice",
		Attributes: map[string]string{
			"staff_id":             "staff-2",
			"user_id":              "union-2",
			"open_conversation_id": "group-2",
		},
	})
	if got["user_id"] != "staff-2" || got["open_conversation_id"] != "group-2" || got["display_name"] != "Alice" {
		t.Fatalf("unexpected built user config: %#v", got)
	}
}

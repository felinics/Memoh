package telegram

import "testing"

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	got, err := normalizeConfig(map[string]any{
		"bot_token": "token-123",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["botToken"] != "token-123" {
		t.Fatalf("unexpected botToken: %#v", got["botToken"])
	}
}

func TestNormalizeConfigRequiresToken(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNormalizeUserConfig(t *testing.T) {
	t.Parallel()

	got, err := normalizeUserConfig(map[string]any{
		"username": "alice",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["username"] != "alice" {
		t.Fatalf("unexpected username: %#v", got["username"])
	}
}

func TestNormalizeUserConfigRequiresBinding(t *testing.T) {
	t.Parallel()

	_, err := normalizeUserConfig(map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestResolveTarget(t *testing.T) {
	t.Parallel()

	target, err := resolveTarget(map[string]any{
		"chat_id": "123",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if target != "123" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestResolveTargetUsername(t *testing.T) {
	t.Parallel()

	target, err := resolveTarget(map[string]any{
		"username": "alice",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if target != "@alice" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestNormalizeTarget(t *testing.T) {
	t.Parallel()

	if got := normalizeTarget("https://t.me/alice"); got != "@alice" {
		t.Fatalf("unexpected normalized target: %s", got)
	}
	if got := normalizeTarget("@alice"); got != "@alice" {
		t.Fatalf("unexpected normalized target: %s", got)
	}
	if got := normalizeTarget("123456789"); got != "123456789" {
		t.Fatalf("positive chat ID mangled: %s", got)
	}
	if got := normalizeTarget("-1002280927535"); got != "-1002280927535" {
		t.Fatalf("negative supergroup chat ID mangled: %s", got)
	}
}

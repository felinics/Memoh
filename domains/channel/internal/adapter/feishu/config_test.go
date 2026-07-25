package feishu

import "testing"

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	got, err := normalizeConfig(map[string]any{
		"app_id":             "app",
		"app_secret":         "secret",
		"encrypt_key":        "enc",
		"verification_token": "verify",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["appId"] != "app" || got["appSecret"] != "secret" {
		t.Fatalf("unexpected feishu config: %#v", got)
	}
	if got["encryptKey"] != "enc" || got["verificationToken"] != "verify" {
		t.Fatalf("unexpected feishu security config: %#v", got)
	}
	if got["region"] != regionFeishu {
		t.Fatalf("unexpected default region: %#v", got["region"])
	}
	if got["inboundMode"] != inboundModeWebsocket {
		t.Fatalf("unexpected default inbound mode: %#v", got["inboundMode"])
	}
}

func TestNormalizeConfigRequiresApp(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNormalizeConfigSupportsLarkAndWebhook(t *testing.T) {
	t.Parallel()

	got, err := normalizeConfig(map[string]any{
		"app_id":       "app",
		"app_secret":   "secret",
		"region":       "lark",
		"inbound_mode": "webhook",
		"encrypt_key":  "enc",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["region"] != regionLark {
		t.Fatalf("unexpected region: %#v", got["region"])
	}
	if got["inboundMode"] != inboundModeWebhook {
		t.Fatalf("unexpected inbound mode: %#v", got["inboundMode"])
	}
}

func TestNormalizeConfigRejectsWebhookWithoutSecrets(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(map[string]any{
		"app_id":       "app",
		"app_secret":   "secret",
		"inbound_mode": "webhook",
	})
	if err == nil {
		t.Fatal("expected webhook secret error")
	}
}

func TestNormalizeConfigRejectsInvalidRegion(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(map[string]any{
		"app_id":     "app",
		"app_secret": "secret",
		"region":     "unknown",
	})
	if err == nil {
		t.Fatal("expected invalid region error")
	}
}

func TestNormalizeConfigRejectsInvalidInboundMode(t *testing.T) {
	t.Parallel()

	_, err := normalizeConfig(map[string]any{
		"app_id":       "app",
		"app_secret":   "secret",
		"inbound_mode": "invalid",
	})
	if err == nil {
		t.Fatal("expected invalid inbound_mode error")
	}
}

func TestNormalizeUserConfig(t *testing.T) {
	t.Parallel()

	got, err := normalizeUserConfig(map[string]any{
		"open_id": "ou_123",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["open_id"] != "ou_123" {
		t.Fatalf("unexpected open_id: %#v", got["open_id"])
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
		"open_id": "ou_123",
		"user_id": "u_123",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if target != "open_id:ou_123" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestNormalizeTarget(t *testing.T) {
	t.Parallel()

	if got := normalizeTarget("ou_123"); got != "open_id:ou_123" {
		t.Fatalf("unexpected normalized target: %s", got)
	}
	if got := normalizeTarget("chat_id:oc_123"); got != "chat_id:oc_123" {
		t.Fatalf("unexpected normalized target: %s", got)
	}
}

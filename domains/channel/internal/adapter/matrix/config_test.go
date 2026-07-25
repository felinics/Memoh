package matrix

import "testing"

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"homeserverUrl":      "https://matrix.example.com/",
		"accessToken":        "tok",
		"userId":             "@memoh:example.com",
		"syncTimeoutSeconds": 15,
		"autoJoinInvites":    false,
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if cfg.HomeserverURL != "https://matrix.example.com" {
		t.Fatalf("unexpected homeserver url: %q", cfg.HomeserverURL)
	}
	if cfg.UserID != "@memoh:example.com" {
		t.Fatalf("unexpected user id: %q", cfg.UserID)
	}
	if cfg.SyncTimeoutSeconds != 15 {
		t.Fatalf("unexpected sync timeout: %d", cfg.SyncTimeoutSeconds)
	}
	if cfg.AutoJoinInvites {
		t.Fatal("expected autoJoinInvites to be false")
	}
}

func TestParseConfigDefaultsAutoJoinInvites(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"homeserverUrl": "https://matrix.example.com",
		"accessToken":   "tok",
		"userId":        "@memoh:example.com",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if !cfg.AutoJoinInvites {
		t.Fatal("expected autoJoinInvites default to true")
	}
}

func TestParseUserConfigRequiresTarget(t *testing.T) {
	if _, err := parseUserConfig(map[string]any{}); err == nil {
		t.Fatal("expected parseUserConfig to fail")
	}
}

func TestResolveTargetPrefersRoomID(t *testing.T) {
	target, err := resolveTarget(map[string]any{
		"room_id": "!room:example.com",
		"user_id": "@alice:example.com",
	})
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	if target != "!room:example.com" {
		t.Fatalf("unexpected target: %q", target)
	}
}

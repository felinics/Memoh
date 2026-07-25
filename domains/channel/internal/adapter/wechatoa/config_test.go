package wechatoa

import "testing"

func TestNormalizeConfig_DefaultSafe(t *testing.T) {
	out, err := normalizeConfig(map[string]any{
		"appId":          "wx123",
		"appSecret":      "secret",
		"token":          "token",
		"encodingAESKey": "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
	})
	if err != nil {
		t.Fatalf("normalizeConfig error = %v", err)
	}
	if out["encryptionMode"] != encryptionModeSafe {
		t.Fatalf("unexpected encryption mode: %v", out["encryptionMode"])
	}
}

func TestNormalizeTarget(t *testing.T) {
	got := normalizeTarget("wechat:openid:o123")
	if got != "openid:o123" {
		t.Fatalf("normalizeTarget = %q", got)
	}
}

func TestResolveTargetRequiresOpenID(t *testing.T) {
	_, err := resolveTarget(map[string]any{"unionid": "u1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfig_HTTPProxy(t *testing.T) {
	proxyURL := "http://memoh:" + "proxy-secret" + "@sztu.cc:3128"
	cfg, err := parseConfig(map[string]any{
		"appId":          "wx123",
		"appSecret":      "secret",
		"token":          "token",
		"encodingAESKey": "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		"httpProxyUrl":   proxyURL,
	})
	if err != nil {
		t.Fatalf("parseConfig error = %v", err)
	}
	if cfg.HTTPProxy.URL != proxyURL {
		t.Fatalf("unexpected httpProxyUrl: %q", cfg.HTTPProxy.URL)
	}
}

func TestNormalizeConfig_HTTPProxy(t *testing.T) {
	proxyURL := "http://memoh:" + "proxy-secret" + "@sztu.cc:3128"
	out, err := normalizeConfig(map[string]any{
		"appId":          "wx123",
		"appSecret":      "secret",
		"token":          "token",
		"encodingAESKey": "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		"httpProxyUrl":   proxyURL,
	})
	if err != nil {
		t.Fatalf("normalizeConfig error = %v", err)
	}
	if out["httpProxyUrl"] != proxyURL {
		t.Fatalf("unexpected httpProxyUrl: %v", out["httpProxyUrl"])
	}
}

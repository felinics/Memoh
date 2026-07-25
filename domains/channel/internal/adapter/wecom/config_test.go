package wecom

import "testing"

func TestParseConfig(t *testing.T) {
	cfg, err := parseConfig(map[string]any{
		"botId":  "bot-1",
		"secret": "sec-1",
	})
	if err != nil {
		t.Fatalf("parseConfig error = %v", err)
	}
	if cfg.BotID != "bot-1" || cfg.Credential != "sec-1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseTarget(t *testing.T) {
	kind, id, ok := parseTarget("chat_id:abc")
	if !ok || kind != "chat_id" || id != "abc" {
		t.Fatalf("unexpected target parse result: ok=%v kind=%q id=%q", ok, kind, id)
	}
	kind, id, ok = parseTarget("user_id:zhangsan")
	if !ok || kind != "user_id" || id != "zhangsan" {
		t.Fatalf("unexpected target parse result: ok=%v kind=%q id=%q", ok, kind, id)
	}
}

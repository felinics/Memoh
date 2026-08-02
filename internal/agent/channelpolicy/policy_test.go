package channelpolicy

import "testing"

func TestParseTelegramPolicy(t *testing.T) {
	policy := Parse(TelegramPlatform, []byte(`{
		"telegram_tool_calls_enabled":true,
		"telegram_enabled_tools":["web_search","send_message","web_search",5],
		"telegram_skills_enabled":false,
		"telegram_message_metadata_mode":"full"
	}`))
	if !policy.EnabledToolsConfigured {
		t.Fatal("expected explicit tool policy")
	}
	if !policy.AllowsTool("web_search") || !policy.AllowsTool("send_message") {
		t.Fatalf("expected configured tools to be allowed: %#v", policy.EnabledTools)
	}
	if got, want := policy.ToolCacheKey(), "skills:off|allow:send_message\x1fweb_search"; got != want {
		t.Fatalf("canonical cache key = %q, want %q", got, want)
	}
	if policy.AllowsTool("send_email") {
		t.Fatal("unexpected unconfigured tool allowance")
	}
	if policy.AllowsTool("use_skill") || policy.SkillsEnabled {
		t.Fatal("disabled skills must remove skill tools")
	}
	if policy.MessageMetadataMode != MessageMetadataFull {
		t.Fatalf("metadata mode = %q", policy.MessageMetadataMode)
	}
}

func TestSemanticallyEqualToolListsShareCacheKey(t *testing.T) {
	t.Parallel()

	left := Parse(TelegramPlatform, []byte(`{"telegram_enabled_tools":["tool_b","tool_a"]}`))
	right := Parse(TelegramPlatform, []byte(`{"telegram_enabled_tools":["tool_a","tool_b"]}`))
	if left.ToolCacheKey() != right.ToolCacheKey() {
		t.Fatalf("equivalent allowlists produced different cache keys: %q != %q", left.ToolCacheKey(), right.ToolCacheKey())
	}
}

func TestDefaultTelegramPolicyPreservesToolsAndCompactsMetadata(t *testing.T) {
	policy := Parse(TelegramPlatform, nil)
	if policy.EnabledToolsConfigured || !policy.ToolCallsEnabled || !policy.SkillsEnabled || !policy.AllowsTool("anything") {
		t.Fatal("missing policy must preserve the legacy tool set")
	}
	if policy.MessageMetadataMode != MessageMetadataCompact {
		t.Fatalf("metadata mode = %q", policy.MessageMetadataMode)
	}
}

func TestTelegramToolCallMasterSwitchDisablesAll(t *testing.T) {
	policy := Parse(TelegramPlatform, []byte(`{
		"telegram_tool_calls_enabled":false,
		"telegram_enabled_tools":["send"]
	}`))
	if policy.AllowsTool("send") {
		t.Fatal("master switch must override the per-tool allowlist")
	}
	if got := policy.ToolCacheKey(); got != "off" {
		t.Fatalf("disabled cache key = %q, want off", got)
	}
}

func TestExplicitEmptyTelegramToolListDisablesAll(t *testing.T) {
	policy := Parse(TelegramPlatform, []byte(`{"telegram_enabled_tools":[]}`))
	if !policy.EnabledToolsConfigured || policy.AllowsTool("web_search") {
		t.Fatal("explicit empty list must disable all tools")
	}
}

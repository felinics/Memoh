package models

import (
	"testing"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	openaicompletions "github.com/felinics/twilight/provider/openai/completions"
	sdk "github.com/felinics/twilight/sdk"
)

func TestNormalizePromptCacheTTL(t *testing.T) {
	cases := map[string]string{
		"":         PromptCacheTTL5m,
		"5m":       PromptCacheTTL5m,
		"1h":       PromptCacheTTL1h,
		"off":      PromptCacheTTLOff,
		"garbage":  PromptCacheTTL5m,
		"DISABLED": PromptCacheTTL5m,
	}
	for input, want := range cases {
		if got := NormalizePromptCacheTTL(input); got != want {
			t.Errorf("NormalizePromptCacheTTL(%q) = %q, want %q", input, got, want)
		}
	}
}

func newAnthropicTestModel(t *testing.T) *sdk.Model {
	t.Helper()
	provider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	return provider.ChatModel("claude-test")
}

func newOpenAITestModel(t *testing.T) *sdk.Model {
	t.Helper()
	provider := openaicompletions.New(openaicompletions.WithAPIKey("test"))
	return provider.ChatModel("gpt-test")
}

func textCacheControl(t *testing.T, msg sdk.Message) *sdk.CacheControl {
	t.Helper()
	if len(msg.Content) == 0 {
		return nil
	}
	tp, ok := msg.Content[0].(sdk.TextPart)
	if !ok {
		return nil
	}
	return tp.CacheControl
}

func TestApplyPromptCache_AnthropicDefaultMovesSystemAndCachesLastTool(t *testing.T) {
	model := newAnthropicTestModel(t)
	system := "you are a helpful bot"
	messages := []sdk.Message{sdk.UserMessage("hi")}
	tools := []sdk.Tool{
		{Name: "first"},
		{Name: "second"},
	}

	gotSystem, gotMessages, gotTools := ApplyPromptCache(model, "", system, messages, tools)

	if gotSystem != "" {
		t.Fatalf("system should be cleared after promotion, got %q", gotSystem)
	}
	if len(gotMessages) != len(messages)+1 {
		t.Fatalf("messages length: got %d, want %d", len(gotMessages), len(messages)+1)
	}
	if gotMessages[0].Role != sdk.MessageRoleSystem {
		t.Errorf("first message role: got %q, want %q", gotMessages[0].Role, sdk.MessageRoleSystem)
	}
	cc := textCacheControl(t, gotMessages[0])
	if cc == nil || cc.Type != "ephemeral" || cc.TTL != "" {
		t.Errorf("system message cache: got %+v, want ephemeral 5m default", cc)
	}
	if got := gotTools[0].CacheControl; got != nil {
		t.Errorf("first tool should not be cached, got %+v", got)
	}
	if got := gotTools[1].CacheControl; got == nil || got.TTL != "" {
		t.Errorf("last tool cache: got %+v, want ephemeral 5m default", got)
	}
}

func TestApplyPromptCache_AnthropicOneHourTTL(t *testing.T) {
	model := newAnthropicTestModel(t)
	gotSystem, gotMessages, gotTools := ApplyPromptCache(
		model,
		PromptCacheTTL1h,
		"system text",
		[]sdk.Message{sdk.UserMessage("hi")},
		[]sdk.Tool{{Name: "only"}},
	)
	if gotSystem != "" {
		t.Fatalf("system should be cleared, got %q", gotSystem)
	}
	cc := textCacheControl(t, gotMessages[0])
	if cc == nil || cc.TTL != "1h" {
		t.Errorf("system cache TTL: got %+v, want 1h", cc)
	}
	if got := gotTools[0].CacheControl; got == nil || got.TTL != "1h" {
		t.Errorf("tool cache TTL: got %+v, want 1h", got)
	}
}

func TestApplyPromptCache_OffLeavesPayloadUnchanged(t *testing.T) {
	model := newAnthropicTestModel(t)
	system := "system text"
	messages := []sdk.Message{sdk.UserMessage("hi")}
	tools := []sdk.Tool{{Name: "first"}, {Name: "second"}}

	gotSystem, gotMessages, gotTools := ApplyPromptCache(
		model,
		PromptCacheTTLOff,
		system,
		messages,
		tools,
	)

	if gotSystem != system {
		t.Errorf("system: got %q, want %q", gotSystem, system)
	}
	if len(gotMessages) != len(messages) {
		t.Errorf("messages length: got %d, want %d", len(gotMessages), len(messages))
	}
	for i, tool := range gotTools {
		if tool.CacheControl != nil {
			t.Errorf("tool %d should not be cached when ttl=off, got %+v", i, tool.CacheControl)
		}
	}
}

func TestApplyPromptCache_NonAnthropicNoop(t *testing.T) {
	model := newOpenAITestModel(t)
	system := "system text"
	messages := []sdk.Message{sdk.UserMessage("hi")}
	tools := []sdk.Tool{{Name: "only"}}

	gotSystem, gotMessages, gotTools := ApplyPromptCache(model, "", system, messages, tools)
	if gotSystem != system {
		t.Errorf("system should be untouched for non-anthropic, got %q", gotSystem)
	}
	if len(gotMessages) != len(messages) {
		t.Errorf("messages should be untouched for non-anthropic, len=%d", len(gotMessages))
	}
	if gotTools[0].CacheControl != nil {
		t.Errorf("tool should not be cached for non-anthropic, got %+v", gotTools[0].CacheControl)
	}
}

func TestApplyPromptCache_AnthropicEmptySystemSkipsPromotion(t *testing.T) {
	model := newAnthropicTestModel(t)
	messages := []sdk.Message{sdk.UserMessage("hi")}
	gotSystem, gotMessages, _ := ApplyPromptCache(model, "", "", messages, nil)
	if gotSystem != "" {
		t.Errorf("system: got %q, want empty", gotSystem)
	}
	if len(gotMessages) != len(messages) {
		t.Fatalf("messages length: got %d, want %d", len(gotMessages), len(messages))
	}
	if cc := textCacheControl(t, gotMessages[0]); cc != nil {
		t.Errorf("user message should not be decorated, got %+v", cc)
	}
}

func TestApplyPromptCache_AnthropicDoesNotMutateInput(t *testing.T) {
	model := newAnthropicTestModel(t)
	tools := []sdk.Tool{{Name: "first"}, {Name: "second"}}
	messages := []sdk.Message{sdk.UserMessage("hi")}
	_, _, _ = ApplyPromptCache(model, "", "system", messages, tools)
	if tools[1].CacheControl != nil {
		t.Errorf("source tool slice was mutated: %+v", tools[1].CacheControl)
	}
}

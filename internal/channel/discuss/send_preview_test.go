package discuss

import "testing"

func TestPartialTopLevelJSONStringStreamsOnlyDecodedText(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw   string
		want  string
		found bool
	}{
		{raw: `{"platform":"telegram","text":"hello`, want: "hello", found: true},
		{raw: `{"text":"line\nnext`, want: "line\nnext", found: true},
		{raw: `{"text":"你好\u4e1`, want: "你好", found: true},
		{raw: `{"reasoning":"private"}`, found: false},
	} {
		got, found := partialTopLevelJSONString(test.raw, "text")
		if found != test.found || got != test.want {
			t.Fatalf("partialTopLevelJSONString(%q) = %q,%v want %q,%v", test.raw, got, found, test.want, test.found)
		}
	}
}

func TestSameDiscussConversationDefaultsOnlyToCurrentTarget(t *testing.T) {
	t.Parallel()
	cfg := DiscussSessionConfig{CurrentPlatform: "telegram", ReplyTarget: "chat-1"}
	if !sameDiscussConversation(cfg, "", "") {
		t.Fatal("omitted target should mean the current conversation")
	}
	if sameDiscussConversation(cfg, "telegram", "chat-2") || sameDiscussConversation(cfg, "discord", "chat-1") {
		t.Fatal("cross-conversation send was accepted for preview")
	}
}

package feishu

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestExtractReadableFromJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"json with text", `{"text":"extracted"}`, "extracted"},
		{"json with message", `{"message":"ok"}`, "ok"},
		{"json with content", `{"content":"result"}`, "result"},
		{"invalid json", `{invalid`, `{invalid`},
		{"empty object", `{}`, `{}`},
		{"array of strings", `["first"]`, "first"},
		{"array empty", `[]`, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractReadableFromJSON(tc.in)
			if got != tc.want {
				t.Errorf("extractReadableFromJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderFeishuStreamFinalTextUsesParts(t *testing.T) {
	t.Parallel()

	msg := channel.Message{
		Text: "plain fallback",
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	got := renderFeishuStreamFinalText(msg, "buffered plain text")
	want := "**Hello**\n\n[docs](https://example.test)"
	if got != want {
		t.Fatalf("expected rich parts to drive Feishu stream final\n  got:  %q\n  want: %q", got, want)
	}
}

func TestRenderFeishuStreamFinalTextLongRichPartsFallsBackToPlain(t *testing.T) {
	t.Parallel()

	got := renderFeishuStreamFinalText(channel.Message{
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: strings.Repeat("你", feishuStreamMaxRunes+100), Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
		},
	}, "buffered plain text")
	if strings.Contains(got, "**") {
		t.Fatalf("long rich stream final should fall back to plain text, got prefix %q", got[:20])
	}
	if len([]rune(got)) <= feishuStreamMaxRunes {
		t.Fatalf("render helper should return full plain fallback before patch truncation, got len=%d", len([]rune(got)))
	}
}

func TestRenderFeishuStreamFinalTextUsesAuthoritativeTextBeforeBuffer(t *testing.T) {
	t.Parallel()

	got := renderFeishuStreamFinalText(channel.Message{Text: "plain fallback"}, "buffered plain text")
	if got != "plain fallback" {
		t.Fatalf("expected authoritative final text, got %q", got)
	}
}

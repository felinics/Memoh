package telegram

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestTelegramMarkdownHasTable(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "left aligned table",
			text: "Intro\n\n| # | item | status |\n|---|------|--------|\n| 1 | a    | done   |\n",
			want: true,
		},
		{
			name: "alignment colons and outer pipes",
			text: "| Metric | Value |\n|:-------|------:|\n| a      | 1     |",
			want: true,
		},
		{
			name: "persian header cells",
			text: "| # | آیتم | وضعیت |\n|---|---|---|\n| ۱ | دریافت پیام | انجام شد |",
			want: true,
		},
		{
			name: "no table separator row",
			text: "| a | b |\n| 1 | 2 |",
			want: false,
		},
		{
			name: "pipes in prose",
			text: "use a | b for alternatives",
			want: false,
		},
		{
			name: "separator without header",
			text: "|---|------|\n| 1 | a |",
			want: false,
		},
		{
			name: "plain prose",
			text: "just a paragraph with no table at all",
			want: false,
		},
		{
			name: "single column separator only",
			text: "|-------|",
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := telegramMarkdownHasTable(tc.text); got != tc.want {
				t.Fatalf("telegramMarkdownHasTable(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestRenderTelegramMarkdownRichMessageTableRoutesToRich(t *testing.T) {
	msg := channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "numbers below\n\n| a | b |\n|---|---|\n| 1 | 2 |\n",
	}
	rich := renderTelegramMarkdownRichMessage(msg)
	if !rich.hasContent() {
		t.Fatal("expected table markdown to produce a rich message")
	}
	if rich.Markdown == "" {
		t.Fatalf("expected markdown rich payload, got %+v", rich)
	}
}

func TestRenderTelegramMarkdownRichMessagePlainTextStaysLegacy(t *testing.T) {
	msg := channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "no table and no math here",
	}
	if rich := renderTelegramMarkdownRichMessage(msg); rich.hasContent() {
		t.Fatalf("expected no rich payload, got %+v", rich)
	}
}

func TestShouldUseTelegramFinalRichMessageForTable(t *testing.T) {
	msg := channel.Message{
		Format: channel.MessageFormatMarkdown,
		Text:   "| a | b |\n|---|---|\n| 1 | 2 |",
	}
	if !shouldUseTelegramFinalRichMessage(msg) {
		t.Fatal("expected markdown table to select the rich send path")
	}
}

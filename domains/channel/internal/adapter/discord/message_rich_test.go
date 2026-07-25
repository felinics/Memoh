package discord

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func TestRenderDiscordMessagePartsMarkdown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		msg      gateway.Message
		want     string
		excludes []string
	}{
		{
			name: "plain text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "hello"},
			}},
			want: "hello",
		},
		{
			name: "bold then italic on separate parts",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "bold", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
				{Type: gateway.MessagePartText, Text: "italic", Styles: []gateway.MessageTextStyle{gateway.MessageStyleItalic}},
			}},
			want: "**bold**\n\n*italic*",
		},
		{
			name: "strikethrough",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "old", Styles: []gateway.MessageTextStyle{gateway.MessageStyleStrikethrough}},
			}},
			want: "~~old~~",
		},
		{
			name: "underline",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "under", Styles: []gateway.MessageTextStyle{gateway.MessageStyleUnderline}},
			}},
			want: "__under__",
		},
		{
			name: "spoiler",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "secret", Styles: []gateway.MessageTextStyle{gateway.MessageStyleSpoiler}},
			}},
			want: "||secret||",
		},
		{
			name: "inline code wins over other styles",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "x.y", Styles: []gateway.MessageTextStyle{gateway.MessageStyleCode, gateway.MessageStyleBold}},
			}},
			want:     "`x.y`",
			excludes: []string{"**"},
		},
		{
			name: "masked link",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
			}},
			want: "[docs](https://example.test/a)",
		},
		{
			name: "link without text uses url",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, URL: "https://example.test"},
			}},
			want: "https://example.test",
		},
		{
			name: "link with disallowed scheme falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "evil", URL: "javascript:alert(1)"},
			}},
			want:     "evil",
			excludes: []string{"javascript:", "["},
		},
		{
			name: "code block with language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "print(1)", Language: "python"},
			}},
			want: "```python\nprint(1)\n```",
		},
		{
			name: "code block allows csharp language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "Console.WriteLine(1);", Language: "c#"},
			}},
			want: "```c#\nConsole.WriteLine(1);\n```",
		},
		{
			name: "code block no language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "raw"},
			}},
			want: "```\nraw\n```",
		},
		{
			name: "code block strips invalid language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "raw", Language: "<script>"},
			}},
			want: "```\nraw\n```",
		},
		{
			name: "mention emits text only",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice"},
			}},
			want:     "@alice",
			excludes: []string{"<@"},
		},
		{
			name: "emoji prefers Emoji field when Text empty",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartEmoji, Emoji: "🎉"},
			}},
			want: "🎉",
		},
		{
			name: "heading",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartHeading, Text: "Title [x]"},
			}},
			want: `## Title \[x\]`,
		},
		{
			name: "blockquote",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartBlockquote, Text: "alpha [x]\nbeta"},
			}},
			want: "> alpha \\[x\\]\n> beta",
		},
		{
			name: "list item",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartListItem, Text: "item [x]"},
			}},
			want: `- item \[x\]`,
		},
		{
			name: "link text closing bracket is escaped",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "see docs]", URL: "https://example.test"},
			}},
			want: `[see docs\]](https://example.test)`,
		},
		{
			name: "link url closing paren is encoded",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "wiki", URL: "https://example.test/page)x"},
			}},
			want: "[wiki](https://example.test/page%29x)",
		},
		{
			name: "link url opening paren is encoded",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "wiki", URL: "https://example.test/a(b"},
			}},
			want: "[wiki](https://example.test/a%28b)",
		},
		{
			name: "link url space is encoded",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "wiki", URL: "https://example.test/foo bar"},
			}},
			want: "[wiki](https://example.test/foo%20bar)",
		},
		{
			name: "link url angle brackets are encoded",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "wiki", URL: "https://example.test/<x>"},
			}},
			want: "[wiki](https://example.test/%3Cx%3E)",
		},
		{
			name: "mixed inline + code block + link",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "title", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
				{Type: gateway.MessagePartCodeBlock, Text: "go test ./...", Language: "bash"},
				{Type: gateway.MessagePartLink, Text: "see docs", URL: "https://example.test"},
			}},
			want: "**title**\n\n```bash\ngo test ./...\n```\n\n[see docs](https://example.test)",
		},
		{
			name: "inline text neutralizes link injection",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "click [evil](https://evil.test)"},
			}},
			want:     `click \[evil\](https://evil.test)`,
			excludes: []string{"[evil]("},
		},
		{
			name: "inline text neutralizes autolink",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "see <https://evil.test>"},
			}},
			want: `see \<https://evil.test\>`,
		},
		{
			name: "inline text escapes inline code marker",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "use `code` here"},
			}},
			want: "use \\`code\\` here",
		},
		{
			name: "styled inline text cannot break out of wrapper",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "x**y", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
			}},
			want: `**x\*\*y**`,
		},
		{
			name: "styled inline text escapes underscore italic markers",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "a_b_c", Styles: []gateway.MessageTextStyle{gateway.MessageStyleItalic}},
			}},
			want: `*a\_b\_c*`,
		},
		{
			name: "inline code style with backtick uses longer fence and padding",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "a`b", Styles: []gateway.MessageTextStyle{gateway.MessageStyleCode}},
			}},
			want: "`` a`b ``",
		},
		{
			name: "code block with triple backticks uses fence of four",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "outer ``` end"},
			}},
			want: "````\nouter ``` end\n````",
		},
		{
			name: "code block with longer backtick run grows fence",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "wow ````` here"},
			}},
			want: "``````\nwow ````` here\n``````",
		},
		{
			name: "link text open bracket is escaped",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "see [docs", URL: "https://example.test"},
			}},
			want: `[see \[docs](https://example.test)`,
		},
		{
			name: "link text newline collapses to space",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "see\ndocs", URL: "https://example.test"},
			}},
			want: "[see docs](https://example.test)",
		},
		{
			name: "mention text escapes markdown control chars",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice [extra]"},
			}},
			want: `@alice \[extra\]`,
		},
		{
			name: "mention with ChannelIdentityID emits Discord-native ping",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "ping "},
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "1234567890"},
			}},
			want: "ping\n\n<@1234567890>",
		},
		{
			name: "mention with unsafe ChannelIdentityID falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "1234)>"},
			}},
			want: `@alice`,
		},
		{
			name: "empty parts returns empty",
			msg:  gateway.Message{Parts: nil},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderDiscordMessagePartsMarkdown(tc.msg)
			if got != tc.want {
				t.Errorf("renderDiscordMessagePartsMarkdown()\n  got:  %q\n  want: %q", got, tc.want)
			}
			for _, no := range tc.excludes {
				if strings.Contains(got, no) {
					t.Errorf("expected %q to NOT contain %q", got, no)
				}
			}
		})
	}
}

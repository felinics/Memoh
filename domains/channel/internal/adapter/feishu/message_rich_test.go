package feishu

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func TestRenderFeishuMessagePartsLarkMD(t *testing.T) {
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
			name: "underline degrades to visible text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "under", Styles: []gateway.MessageTextStyle{gateway.MessageStyleUnderline}},
			}},
			want: "under",
		},
		{
			name: "spoiler degrades to visible text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "secret", Styles: []gateway.MessageTextStyle{gateway.MessageStyleSpoiler}},
			}},
			want: "secret",
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
			name: "link with text",
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
			want: "[https://example.test](https://example.test)",
		},
		{
			name: "link with disallowed scheme falls back to plain text",
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
			name: "mention without identity emits text only",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice"},
			}},
			want:     "@alice",
			excludes: []string{"<at"},
		},
		{
			name: "mention with ChannelIdentityID emits lark_md at-tag",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "ping "},
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "ou_alice_open_id"},
			}},
			want: "ping" + "\n\n" + `<at user_id="ou_alice_open_id"></at>`,
		},
		{
			name: "mention with unsafe ChannelIdentityID falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: `ou_alice"><script>`},
			}},
			want: `@alice`,
		},
		{
			name: "emoji prefers Emoji field when Text empty",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartEmoji, Emoji: "🎉"},
			}},
			want: "🎉",
		},
		{
			name: "heading degrades to bold title",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartHeading, Text: "Title [x]"},
			}},
			want: `**Title \[x\]**`,
		},
		{
			name: "blockquote quotes each line",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartBlockquote, Text: "alpha [x]\nbeta"},
			}},
			want: "> alpha \\[x\\]\n> beta",
		},
		{
			name: "list item emits bullet line",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartListItem, Text: "item [x]"},
			}},
			want: `- item \[x\]`,
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
			name: "skips empty parts",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "   "},
				{Type: gateway.MessagePartText, Text: "kept"},
			}},
			want: "kept",
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
			name: "inline text neutralizes at-mention injection",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: `ping <at id="all"></at>`},
			}},
			want: `ping \<at id="all"\>\</at\>`,
		},
		{
			name: "inline text escapes inline code marker",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "use `code` here"},
			}},
			want: "use \\`code\\` here",
		},
		{
			name: "styled inline text cannot break out of bold wrapper",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "x**y", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
			}},
			want: `**x\*\*y**`,
		},
		{
			name: "styled italic escapes inner asterisks",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "a*b", Styles: []gateway.MessageTextStyle{gateway.MessageStyleItalic}},
			}},
			want: `*a\*b*`,
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
			name: "link url paren is encoded",
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
			name: "link url angle brackets are encoded",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "wiki", URL: "https://example.test/<x>"},
			}},
			want: "[wiki](https://example.test/%3Cx%3E)",
		},
		{
			name: "mention text escapes markdown chars",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice [extra]"},
			}},
			want: `@alice \[extra\]`,
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
			got := renderFeishuMessagePartsLarkMD(tc.msg)
			if got != tc.want {
				t.Errorf("renderFeishuMessagePartsLarkMD()\n  got:  %q\n  want: %q", got, tc.want)
			}
			for _, no := range tc.excludes {
				if strings.Contains(got, no) {
					t.Errorf("expected %q to NOT contain %q", got, no)
				}
			}
		})
	}
}

func TestBuildFeishuCardContentWrapsLarkMDFromParts(t *testing.T) {
	t.Parallel()

	msg := gateway.Message{Parts: []gateway.MessagePart{
		{Type: gateway.MessagePartText, Text: "title", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
		{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test"},
	}}
	rendered := renderFeishuMessagePartsLarkMD(msg)
	if rendered == "" {
		t.Fatalf("expected non-empty render")
	}
	content, err := buildFeishuCardContent(rendered)
	if err != nil {
		t.Fatalf("buildFeishuCardContent: %v", err)
	}
	for _, want := range []string{
		`"tag":"lark_md"`,
		"**title**",
		"[docs](https://example.test)",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("expected card content to contain %q\nfull: %s", want, content)
		}
	}
}

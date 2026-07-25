package slack

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func TestRenderSlackMessagePartsMrkdwn(t *testing.T) {
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
			name: "bold uses single asterisk",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "bold", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
			}},
			want: "*bold*",
		},
		{
			name: "italic uses underscore",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "italic", Styles: []gateway.MessageTextStyle{gateway.MessageStyleItalic}},
			}},
			want: "_italic_",
		},
		{
			name: "strikethrough uses single tilde",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "old", Styles: []gateway.MessageTextStyle{gateway.MessageStyleStrikethrough}},
			}},
			want: "~old~",
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
			excludes: []string{"*"},
		},
		{
			name: "link uses pipe syntax",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
			}},
			want: "<https://example.test/a|docs>",
		},
		{
			name: "link without text is bare url",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, URL: "https://example.test"},
			}},
			want: "<https://example.test>",
		},
		{
			name: "link with disallowed scheme falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "evil", URL: "javascript:alert(1)"},
			}},
			want:     "evil",
			excludes: []string{"javascript:", "<"},
		},
		{
			name: "code block has no language hint",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "print(1)", Language: "python"},
			}},
			want: "```\nprint(1)\n```",
		},
		{
			name: "code block no language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "raw"},
			}},
			want: "```\nraw\n```",
		},
		{
			name: "code block neutralizes Slack tag after embedded fence",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "before\n```\n<!channel>\n<https://evil.test|click>"},
			}},
			want:     "```\nbefore\n```\n&lt;!channel&gt;\n&lt;https://evil.test|click&gt;\n```",
			excludes: []string{"<!channel>", "<https://evil.test|click>"},
		},
		{
			name: "mention without identity emits text only",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice"},
			}},
			want:     "@alice",
			excludes: []string{"<@"},
		},
		{
			name: "mention with ChannelIdentityID emits Slack native ping",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "ping "},
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "U12345ABC"},
			}},
			want: "ping\n\n<@U12345ABC>",
		},
		{
			name: "mention with unsafe ChannelIdentityID falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "U123|<evil>"},
			}},
			want: "@alice",
		},
		{
			name: "emoji prefers Emoji field",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartEmoji, Emoji: "🎉"},
			}},
			want: "🎉",
		},
		{
			name: "heading degrades to bold title",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartHeading, Text: "Title"},
			}},
			want: "*Title*",
		},
		{
			name: "blockquote quotes each line",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartBlockquote, Text: "alpha <x>\nbeta"},
			}},
			want: "> alpha &lt;x&gt;\n> beta",
		},
		{
			name: "list item emits bullet line",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartListItem, Text: "item <x>"},
			}},
			want: "- item &lt;x&gt;",
		},
		{
			name: "special chars escaped in inline text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "x < y & z > 0"},
			}},
			want: "x &lt; y &amp; z &gt; 0",
		},
		{
			name: "link text special chars escaped",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "<docs>", URL: "https://example.test"},
			}},
			want: "<https://example.test|&lt;docs&gt;>",
		},
		{
			name: "link text pipe is escaped",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs|all", URL: "https://example.test"},
			}},
			want: "<https://example.test|docs&#124;all>",
		},
		{
			name: "mixed inline + code block + link",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "title", Styles: []gateway.MessageTextStyle{gateway.MessageStyleBold}},
				{Type: gateway.MessagePartCodeBlock, Text: "go test ./..."},
				{Type: gateway.MessagePartLink, Text: "see docs", URL: "https://example.test"},
			}},
			want: "*title*\n\n```\ngo test ./...\n```\n\n<https://example.test|see docs>",
		},
		{
			name: "inline text neutralizes forged Slack link",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "see <https://evil.test|Click here>"},
			}},
			want: "see &lt;https://evil.test|Click here&gt;",
		},
		{
			name: "inline text neutralizes channel mention injection",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "alert <!channel>"},
			}},
			want: "alert &lt;!channel&gt;",
		},
		{
			name: "inline text neutralizes user mention injection",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "ping <@U12345>"},
			}},
			want: "ping &lt;@U12345&gt;",
		},
		{
			name: "link url percent encodes angle bracket",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/<script>"},
			}},
			want: "<https://example.test/%3Cscript%3E|docs>",
		},
		{
			name: "link url percent encodes raw space",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/foo bar"},
			}},
			want: "<https://example.test/foo%20bar|docs>",
		},
		{
			name: "link url strips CRLF delimiters",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/a\r\n<bad>|x"},
			}},
			want: "<https://example.test/a%3Cbad%3E%7Cx|docs>",
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
			got := renderSlackMessagePartsMrkdwn(tc.msg)
			if got != tc.want {
				t.Errorf("renderSlackMessagePartsMrkdwn()\n  got:  %q\n  want: %q", got, tc.want)
			}
			for _, no := range tc.excludes {
				if strings.Contains(got, no) {
					t.Errorf("expected %q to NOT contain %q", got, no)
				}
			}
		})
	}
}

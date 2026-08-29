package slack

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestRenderSlackMessagePartsMrkdwn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		msg      channel.Message
		want     string
		excludes []string
	}{
		{
			name: "plain text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "hello"},
			}},
			want: "hello",
		},
		{
			name: "bold uses single asterisk",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "bold", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			}},
			want: "*bold*",
		},
		{
			name: "italic uses underscore",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "italic", Styles: []channel.MessageTextStyle{channel.MessageStyleItalic}},
			}},
			want: "_italic_",
		},
		{
			name: "strikethrough uses single tilde",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "old", Styles: []channel.MessageTextStyle{channel.MessageStyleStrikethrough}},
			}},
			want: "~old~",
		},
		{
			name: "underline degrades to visible text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "under", Styles: []channel.MessageTextStyle{channel.MessageStyleUnderline}},
			}},
			want: "under",
		},
		{
			name: "spoiler degrades to visible text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "secret", Styles: []channel.MessageTextStyle{channel.MessageStyleSpoiler}},
			}},
			want: "secret",
		},
		{
			name: "inline code wins over other styles",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "x.y", Styles: []channel.MessageTextStyle{channel.MessageStyleCode, channel.MessageStyleBold}},
			}},
			want:     "`x.y`",
			excludes: []string{"*"},
		},
		{
			name: "link uses pipe syntax",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a"},
			}},
			want: "<https://example.test/a|docs>",
		},
		{
			name: "link without text is bare url",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, URL: "https://example.test"},
			}},
			want: "<https://example.test>",
		},
		{
			name: "link with disallowed scheme falls back to text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "evil", URL: "javascript:alert(1)"},
			}},
			want:     "evil",
			excludes: []string{"javascript:", "<"},
		},
		{
			name: "code block has no language hint",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartCodeBlock, Text: "print(1)", Language: "python"},
			}},
			want: "```\nprint(1)\n```",
		},
		{
			name: "code block no language",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartCodeBlock, Text: "raw"},
			}},
			want: "```\nraw\n```",
		},
		{
			name: "code block neutralizes Slack tag after embedded fence",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartCodeBlock, Text: "before\n```\n<!channel>\n<https://evil.test|click>"},
			}},
			want:     "```\nbefore\n```\n&lt;!channel&gt;\n&lt;https://evil.test|click&gt;\n```",
			excludes: []string{"<!channel>", "<https://evil.test|click>"},
		},
		{
			name: "mention without identity emits text only",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartMention, Text: "@alice"},
			}},
			want:     "@alice",
			excludes: []string{"<@"},
		},
		{
			name: "mention with ChannelIdentityID emits Slack native ping",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "ping "},
				{Type: channel.MessagePartMention, Text: "@alice", ChannelIdentityID: "U12345ABC"},
			}},
			want: "ping\n\n<@U12345ABC>",
		},
		{
			name: "mention with unsafe ChannelIdentityID falls back to text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartMention, Text: "@alice", ChannelIdentityID: "U123|<evil>"},
			}},
			want: "@alice",
		},
		{
			name: "emoji prefers Emoji field",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartEmoji, Emoji: "🎉"},
			}},
			want: "🎉",
		},
		{
			name: "heading degrades to bold title",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartHeading, Text: "Title"},
			}},
			want: "*Title*",
		},
		{
			name: "blockquote quotes each line",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartBlockquote, Text: "alpha <x>\nbeta"},
			}},
			want: "> alpha &lt;x&gt;\n> beta",
		},
		{
			name: "list item emits bullet line",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartListItem, Text: "item <x>"},
			}},
			want: "- item &lt;x&gt;",
		},
		{
			name: "special chars escaped in inline text",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "x < y & z > 0"},
			}},
			want: "x &lt; y &amp; z &gt; 0",
		},
		{
			name: "link text special chars escaped",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "<docs>", URL: "https://example.test"},
			}},
			want: "<https://example.test|&lt;docs&gt;>",
		},
		{
			name: "link text pipe is escaped",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "docs|all", URL: "https://example.test"},
			}},
			want: "<https://example.test|docs&#124;all>",
		},
		{
			name: "mixed inline + code block + link",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "title", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
				{Type: channel.MessagePartCodeBlock, Text: "go test ./..."},
				{Type: channel.MessagePartLink, Text: "see docs", URL: "https://example.test"},
			}},
			want: "*title*\n\n```\ngo test ./...\n```\n\n<https://example.test|see docs>",
		},
		{
			name: "inline text neutralizes forged Slack link",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "see <https://evil.test|Click here>"},
			}},
			want: "see &lt;https://evil.test|Click here&gt;",
		},
		{
			name: "inline text neutralizes channel mention injection",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "alert <!channel>"},
			}},
			want: "alert &lt;!channel&gt;",
		},
		{
			name: "inline text neutralizes user mention injection",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "ping <@U12345>"},
			}},
			want: "ping &lt;@U12345&gt;",
		},
		{
			name: "link url percent encodes angle bracket",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/<script>"},
			}},
			want: "<https://example.test/%3Cscript%3E|docs>",
		},
		{
			name: "link url percent encodes raw space",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/foo bar"},
			}},
			want: "<https://example.test/foo%20bar|docs>",
		},
		{
			name: "link url strips CRLF delimiters",
			msg: channel.Message{Parts: []channel.MessagePart{
				{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/a\r\n<bad>|x"},
			}},
			want: "<https://example.test/a%3Cbad%3E%7Cx|docs>",
		},
		{
			name: "empty parts returns empty",
			msg:  channel.Message{Parts: nil},
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

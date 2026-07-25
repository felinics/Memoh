package telegram

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// TestRenderTelegramMessagePartsRichMessage_Mentions covers the
// ChannelIdentityID → tg://user?id=… path. The canonical fixture in
// parts_canonical_test.go locks down the no-id mention output (plain text),
// so cases here focus on the id-resolved render.
func TestRenderTelegramMessagePartsRichMessage_Mentions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  gateway.Message
		want string
		not  []string
	}{
		{
			name: "mention with numeric ChannelIdentityID emits tg user link",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "ping "},
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "42"},
			}},
			want: `<p>ping</p><p><a href="tg://user?id=42">@alice</a></p>`,
		},
		{
			name: "mention with empty ChannelIdentityID falls back to text paragraph",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice"},
			}},
			want: `<p>@alice</p>`,
			not:  []string{`tg://user`},
		},
		{
			name: "mention with non-numeric ChannelIdentityID falls back to text",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: "@alice", ChannelIdentityID: "ali ce"},
			}},
			want: `<p>@alice</p>`,
			not:  []string{`tg://user`},
		},
		{
			name: "mention text injection in href is escaped",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartMention, Text: `<script>`, ChannelIdentityID: "7"},
			}},
			want: `<p><a href="tg://user?id=7">&lt;script&gt;</a></p>`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rich := renderTelegramMessagePartsRichMessage(tc.msg)
			if rich.HTML != tc.want {
				t.Errorf("got %q\nwant %q", rich.HTML, tc.want)
			}
			for _, n := range tc.not {
				if strings.Contains(rich.HTML, n) {
					t.Errorf("expected %q to NOT contain %q", rich.HTML, n)
				}
			}
		})
	}
}

func TestRenderTelegramMessagePartsRichMessage_BlockPartsAndStyles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  gateway.Message
		want string
	}{
		{
			name: "underline",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "under", Styles: []gateway.MessageTextStyle{gateway.MessageStyleUnderline}},
			}},
			want: `<p><u>under</u></p>`,
		},
		{
			name: "spoiler",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartText, Text: "secret", Styles: []gateway.MessageTextStyle{gateway.MessageStyleSpoiler}},
			}},
			want: `<p><tg-spoiler>secret</tg-spoiler></p>`,
		},
		{
			name: "heading",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartHeading, Text: "Title <x>"},
			}},
			want: `<p><b>Title &lt;x&gt;</b></p>`,
		},
		{
			name: "blockquote",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartBlockquote, Text: "alpha <x>\nbeta"},
			}},
			want: `<blockquote>alpha &lt;x&gt;
beta</blockquote>`,
		},
		{
			name: "list item",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartListItem, Text: "item <x>"},
			}},
			want: `<p>- item &lt;x&gt;</p>`,
		},
		{
			name: "code block allows csharp language",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartCodeBlock, Text: "Console.WriteLine(1);", Language: "c#"},
			}},
			want: `<pre><code class="language-c#">Console.WriteLine(1);</code></pre>`,
		},
		{
			name: "link url percent encodes raw space",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/foo bar"},
			}},
			want: `<p><a href="https://example.test/foo%20bar">docs</a></p>`,
		},
		{
			name: "link url strips CRLF delimiters",
			msg: gateway.Message{Parts: []gateway.MessagePart{
				{Type: gateway.MessagePartLink, Text: "docs", URL: "https://example.test/a\r\n<bad>|x"},
			}},
			want: `<p><a href="https://example.test/a%3Cbad%3E%7Cx">docs</a></p>`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rich := renderTelegramMessagePartsRichMessage(tc.msg)
			if rich.HTML != tc.want {
				t.Errorf("got %q\nwant %q", rich.HTML, tc.want)
			}
		})
	}
}

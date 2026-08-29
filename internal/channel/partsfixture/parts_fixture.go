// Package partsfixture exposes the canonical Message.Parts fixture used by
// the cross-platform rendering regression tests. It lives in its own
// package because the existing channeltest package (also a test-helper) is
// imported by internal-package channel tests; channeltest must not import
// channel itself or the test binary would form an import cycle.
package partsfixture

import "github.com/felinics/memoh/internal/channel"

// Canonical returns the shared rich-message fixture used by the
// cross-platform Parts rendering regression tests. The slice covers the
// baseline inline/block mix (text, link, code_block, heading, blockquote,
// list_item, mention, emoji), style precedence, safe and unsafe links,
// delimiter-heavy URLs, language-bearing and language-sanitized code blocks,
// multiline code, and a mention without platform identity. Each call returns a
// fresh copy so callers can mutate the slice without affecting other tests.
func Canonical() []channel.MessagePart {
	return []channel.MessagePart{
		{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
		{Type: channel.MessagePartText, Text: "world", Styles: []channel.MessageTextStyle{channel.MessageStyleItalic}},
		{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test/page"},
		{Type: channel.MessagePartLink, Text: "wiki", URL: "https://example.test/<x>(y)|z"},
		{Type: channel.MessagePartLink, Text: "empty url"},
		{Type: channel.MessagePartLink, Text: "evil", URL: "javascript:alert(1)"},
		{Type: channel.MessagePartText, Text: "literal.code", Styles: []channel.MessageTextStyle{channel.MessageStyleCode, channel.MessageStyleBold}},
		{Type: channel.MessagePartCodeBlock, Text: "go test", Language: "bash"},
		{Type: channel.MessagePartCodeBlock, Text: "line1\nline2", Language: "python"},
		{Type: channel.MessagePartCodeBlock, Text: "raw", Language: "<script>"},
		{Type: channel.MessagePartHeading, Text: "Title [x]"},
		{Type: channel.MessagePartBlockquote, Text: "alpha [x]\nbeta"},
		{Type: channel.MessagePartListItem, Text: "item [x]"},
		{Type: channel.MessagePartMention, Text: "@alice"},
		{Type: channel.MessagePartEmoji, Emoji: "🎉"},
	}
}

// Expected outputs for Canonical() across each rendering target. Update
// these constants when changing the fixture, and the per-adapter regression
// tests stay aligned automatically.
const (
	// CanonicalMarkdown is the GFM-flavored degrader output, also matching
	// the Discord adapter.
	CanonicalMarkdown = "**Hello**\n\n*world*\n\n[docs](https://example.test/page)\n\n[wiki](https://example.test/%3Cx%3E%28y%29%7Cz)\n\nempty url\n\nevil\n\n`literal.code`\n\n```bash\ngo test\n```\n\n```python\nline1\nline2\n```\n\n```\nraw\n```\n\n## Title \\[x\\]\n\n> alpha \\[x\\]\n> beta\n\n- item \\[x\\]\n\n@alice\n\n🎉"

	// CanonicalFeishuLarkMD matches Feishu's lark_md renderer. It is mostly
	// GFM-aligned, but headings degrade to bold text because lark_md cards do
	// not support ATX headings reliably.
	CanonicalFeishuLarkMD = "**Hello**\n\n*world*\n\n[docs](https://example.test/page)\n\n[wiki](https://example.test/%3Cx%3E%28y%29%7Cz)\n\nempty url\n\nevil\n\n`literal.code`\n\n```bash\ngo test\n```\n\n```python\nline1\nline2\n```\n\n```\nraw\n```\n\n**Title \\[x\\]**\n\n> alpha \\[x\\]\n> beta\n\n- item \\[x\\]\n\n@alice\n\n🎉"

	// CanonicalPlain is the plain-text degrader output used when neither
	// RichText nor Markdown is supported.
	CanonicalPlain = "Hello\n\nworld\n\ndocs (https://example.test/page)\n\nwiki (https://example.test/<x>(y)|z)\n\nempty url\n\nevil (javascript:alert(1))\n\nliteral.code\n\ngo test\n\nline1\nline2\n\nraw\n\nTitle [x]\n\n> alpha [x]\n> beta\n\n- item [x]\n\n@alice\n\n🎉"

	// CanonicalSlackMrkdwn matches Slack's distinct mrkdwn dialect:
	// single-asterisk bold, underscore italic, <url|text> links, fenced
	// code blocks without a language hint.
	CanonicalSlackMrkdwn = "*Hello*\n\n_world_\n\n<https://example.test/page|docs>\n\n<https://example.test/%3Cx%3E%28y%29%7Cz|wiki>\n\nempty url\n\nevil\n\n`literal.code`\n\n```\ngo test\n```\n\n```\nline1\nline2\n```\n\n```\nraw\n```\n\n*Title [x]*\n\n> alpha [x]\n> beta\n\n- item [x]\n\n@alice\n\n🎉"

	// CanonicalTelegramRichHTML matches the HTML body Telegram's
	// sendRichMessage path emits — paragraph-wrapped inline elements,
	// <pre><code class="language-…"> for code blocks, no top-level whitespace.
	CanonicalTelegramRichHTML = "<p><b>Hello</b></p><p><i>world</i></p><p><a href=\"https://example.test/page\">docs</a></p><p><a href=\"https://example.test/%3Cx%3E%28y%29%7Cz\">wiki</a></p><p>empty url</p><p>evil</p><p><code>literal.code</code></p><pre><code class=\"language-bash\">go test</code></pre><pre><code class=\"language-python\">line1\nline2</code></pre><pre>raw</pre><p><b>Title [x]</b></p><blockquote>alpha [x]\nbeta</blockquote><p>- item [x]</p><p>@alice</p><p>🎉</p>"

	// CanonicalMatrixHTML matches Matrix's org.matrix.custom.html output.
	CanonicalMatrixHTML = "<p><strong>Hello</strong></p><p><em>world</em></p><p><a href=\"https://example.test/page\">docs</a></p><p><a href=\"https://example.test/&lt;x&gt;(y)|z\">wiki</a></p><p>empty url</p><p>evil</p><p><code>literal.code</code></p><pre><code class=\"language-bash\">go test</code></pre><pre><code class=\"language-python\">line1\nline2</code></pre><pre><code>raw</code></pre><h2>Title [x]</h2><blockquote>alpha [x]<br>beta</blockquote><ul><li>item [x]</li></ul><p>@alice</p><p>🎉</p>"
)

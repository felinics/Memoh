package feishu

import (
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

func renderFeishuMessagePartsLarkMD(msg channel.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case channel.MessagePartText:
			writeFeishuRichInlinePart(&b, part.Text, part.Styles)
		case channel.MessagePartLink:
			writeFeishuRichLinkPart(&b, part)
		case channel.MessagePartCodeBlock:
			writeFeishuRichCodeBlockPart(&b, part)
		case channel.MessagePartMention:
			writeFeishuRichMentionPart(&b, part)
		case channel.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeFeishuRichInlinePart(&b, text, part.Styles)
		case channel.MessagePartHeading:
			writeFeishuRichHeadingPart(&b, part)
		case channel.MessagePartBlockquote:
			writeFeishuRichBlockquotePart(&b, part)
		case channel.MessagePartListItem:
			writeFeishuRichListItemPart(&b, part)
		}
	}
	return strings.TrimSpace(b.String())
}

func writeFeishuRichInlinePart(b *strings.Builder, text string, styles []channel.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(renderFeishuRichStyledInline(text, styles))
}

func writeFeishuRichLinkPart(b *strings.Builder, part channel.MessagePart) {
	url := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if text == "" {
		text = url
	}
	if text == "" {
		return
	}
	if url == "" || !isAllowedFeishuRichHref(url) {
		writeFeishuRichInlinePart(b, text, part.Styles)
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("[")
	b.WriteString(escapeFeishuLinkText(text))
	b.WriteString("](")
	b.WriteString(escapeFeishuLinkURL(url))
	b.WriteString(")")
}

// writeFeishuRichMentionPart emits Feishu's lark_md <at user_id="…"></at>
// tag when the canonical Part carries a safe open_id. Feishu open IDs are
// lowercase alphanumeric with a single underscore-separated prefix
// (e.g. "ou_abc123"); IDs outside that class fall back to the inline-text
// path so the visible mention still reaches the channel.
func writeFeishuRichMentionPart(b *strings.Builder, part channel.MessagePart) {
	id := strings.TrimSpace(part.ChannelIdentityID)
	if id == "" || !isSafeFeishuMentionID(id) {
		writeFeishuRichInlinePart(b, part.Text, part.Styles)
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(`<at user_id="`)
	b.WriteString(id)
	b.WriteString(`"></at>`)
}

func isSafeFeishuMentionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func writeFeishuRichCodeBlockPart(b *strings.Builder, part channel.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fence := selectFeishuBacktickFence(text, 3)
	lang := channel.NormalizeMessagePartCodeLanguage(part.Language)
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n")
	b.WriteString(fence)
}

func writeFeishuRichHeadingPart(b *strings.Builder, part channel.MessagePart) {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	styles := append([]channel.MessageTextStyle{channel.MessageStyleBold}, part.Styles...)
	b.WriteString(renderFeishuRichStyledInline(text, styles))
}

func writeFeishuRichBlockquotePart(b *strings.Builder, part channel.MessagePart) {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(">")
		if line != "" {
			b.WriteString(" ")
			b.WriteString(renderFeishuRichStyledInline(line, part.Styles))
		}
	}
}

func writeFeishuRichListItemPart(b *strings.Builder, part channel.MessagePart) {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("- ")
	b.WriteString(renderFeishuRichStyledInline(lines[0], part.Styles))
	for _, line := range lines[1:] {
		b.WriteString("\n  ")
		if line != "" {
			b.WriteString(renderFeishuRichStyledInline(line, part.Styles))
		}
	}
}

func renderFeishuRichStyledInline(text string, styles []channel.MessageTextStyle) string {
	if hasFeishuRichTextStyle(styles, channel.MessageStyleCode) {
		return wrapFeishuInlineCode(text)
	}
	escaped := escapeFeishuInlineLarkMD(text)
	if hasFeishuRichTextStyle(styles, channel.MessageStyleStrikethrough) {
		escaped = "~~" + escaped + "~~"
	}
	if hasFeishuRichTextStyle(styles, channel.MessageStyleItalic) {
		escaped = "*" + escaped + "*"
	}
	if hasFeishuRichTextStyle(styles, channel.MessageStyleBold) {
		escaped = "**" + escaped + "**"
	}
	return escaped
}

func hasFeishuRichTextStyle(styles []channel.MessageTextStyle, want channel.MessageTextStyle) bool {
	for _, s := range styles {
		if s == want {
			return true
		}
	}
	return false
}

func isAllowedFeishuRichHref(href string) bool {
	href = strings.TrimSpace(href)
	return strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "http://") ||
		strings.HasPrefix(href, "mailto:") ||
		strings.HasPrefix(href, "tel:")
}

// escapeFeishuInlineLarkMD neutralises lark_md control characters in
// attacker-supplied inline text so injected `[text](url)`, `<at id=…>`, code
// spans, or stray bold/italic markers cannot break out of the wrapper. `\`
// must come first so subsequent escapes are themselves preserved.
var escapeFeishuInlineLarkMD = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`~`, `\~`,
	`[`, `\[`,
	`]`, `\]`,
	`<`, `\<`,
	`>`, `\>`,
).Replace

// escapeFeishuLinkText escapes the characters that can prematurely close or
// split a `[text](url)` label, and collapses control whitespace that lark_md
// otherwise treats as a paragraph break inside the label.
func escapeFeishuLinkText(text string) string {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " ")
	return escapeFeishuInlineLarkMD(text)
}

// escapeFeishuLinkURL strips control whitespace and percent-encodes the
// characters that would otherwise terminate the `(url)` segment.
func escapeFeishuLinkURL(url string) string {
	return channel.EscapeMessagePartLinkURL(url)
}

func selectFeishuBacktickFence(text string, minRun int) string {
	maxRun, cur := 0, 0
	for _, r := range text {
		if r == '`' {
			cur++
			if cur > maxRun {
				maxRun = cur
			}
			continue
		}
		cur = 0
	}
	n := minRun
	if maxRun >= n {
		n = maxRun + 1
	}
	return strings.Repeat("`", n)
}

func wrapFeishuInlineCode(text string) string {
	fence := selectFeishuBacktickFence(text, 1)
	pad := ""
	if strings.ContainsRune(text, '`') {
		pad = " "
	}
	return fence + pad + text + pad + fence
}

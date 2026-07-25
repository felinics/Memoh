package feishu

import (
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func renderFeishuMessagePartsLarkMD(msg gateway.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case gateway.MessagePartText:
			writeFeishuRichInlinePart(&b, part.Text, part.Styles)
		case gateway.MessagePartLink:
			writeFeishuRichLinkPart(&b, part)
		case gateway.MessagePartCodeBlock:
			writeFeishuRichCodeBlockPart(&b, part)
		case gateway.MessagePartMention:
			writeFeishuRichMentionPart(&b, part)
		case gateway.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeFeishuRichInlinePart(&b, text, part.Styles)
		case gateway.MessagePartHeading:
			writeFeishuRichHeadingPart(&b, part)
		case gateway.MessagePartBlockquote:
			writeFeishuRichBlockquotePart(&b, part)
		case gateway.MessagePartListItem:
			writeFeishuRichListItemPart(&b, part)
		}
	}
	return strings.TrimSpace(b.String())
}

func writeFeishuRichInlinePart(b *strings.Builder, text string, styles []gateway.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(renderFeishuRichStyledInline(text, styles))
}

func writeFeishuRichLinkPart(b *strings.Builder, part gateway.MessagePart) {
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
func writeFeishuRichMentionPart(b *strings.Builder, part gateway.MessagePart) {
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

func writeFeishuRichCodeBlockPart(b *strings.Builder, part gateway.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fence := selectFeishuBacktickFence(text, 3)
	lang := gateway.NormalizeMessagePartCodeLanguage(part.Language)
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n")
	b.WriteString(fence)
}

func writeFeishuRichHeadingPart(b *strings.Builder, part gateway.MessagePart) {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	styles := append([]gateway.MessageTextStyle{gateway.MessageStyleBold}, part.Styles...)
	b.WriteString(renderFeishuRichStyledInline(text, styles))
}

func writeFeishuRichBlockquotePart(b *strings.Builder, part gateway.MessagePart) {
	lines := gateway.SplitMessagePartTextLines(part.Text)
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

func writeFeishuRichListItemPart(b *strings.Builder, part gateway.MessagePart) {
	lines := gateway.SplitMessagePartTextLines(part.Text)
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

func renderFeishuRichStyledInline(text string, styles []gateway.MessageTextStyle) string {
	if hasFeishuRichTextStyle(styles, gateway.MessageStyleCode) {
		return wrapFeishuInlineCode(text)
	}
	escaped := escapeFeishuInlineLarkMD(text)
	if hasFeishuRichTextStyle(styles, gateway.MessageStyleStrikethrough) {
		escaped = "~~" + escaped + "~~"
	}
	if hasFeishuRichTextStyle(styles, gateway.MessageStyleItalic) {
		escaped = "*" + escaped + "*"
	}
	if hasFeishuRichTextStyle(styles, gateway.MessageStyleBold) {
		escaped = "**" + escaped + "**"
	}
	return escaped
}

func hasFeishuRichTextStyle(styles []gateway.MessageTextStyle, want gateway.MessageTextStyle) bool {
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
	return gateway.EscapeMessagePartLinkURL(url)
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

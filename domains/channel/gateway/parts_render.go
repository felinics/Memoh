package gateway

import "strings"

// RenderPartsAsMarkdown produces a GFM-flavored Markdown string from
// canonical MessageParts. It is the degradation path used by
// coerceFormatForCaps when a target channel supports Markdown but lacks
// RichText capability (e.g. DingTalk, Matrix), and is the reference output
// the per-platform renderers (Telegram HTML / Discord MD / Feishu lark_md /
// Slack mrkdwn) are tested against in the canonical-parts matrix.
//
// Attacker-controllable text and URL fields are neutralised so they cannot
// forge masked links, escape style wrappers, or open autolinks. The escape
// set mirrors what the Discord renderer applies; channels with stricter
// syntax (Slack mrkdwn) bypass this function via the RichText gate and run
// their own renderer instead.
func RenderPartsAsMarkdown(parts []MessagePart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		switch part.Type {
		case MessagePartText:
			writeDegradeMarkdownInline(&b, part.Text, part.Styles)
		case MessagePartLink:
			writeDegradeMarkdownLink(&b, part)
		case MessagePartCodeBlock:
			writeDegradeMarkdownCodeBlock(&b, part)
		case MessagePartMention:
			writeDegradeMarkdownInline(&b, part.Text, part.Styles)
		case MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeDegradeMarkdownInline(&b, text, part.Styles)
		case MessagePartHeading:
			writeDegradeMarkdownHeading(&b, part)
		case MessagePartBlockquote:
			writeDegradeMarkdownBlockquote(&b, part)
		case MessagePartListItem:
			writeDegradeMarkdownListItem(&b, part)
		}
	}
	return strings.TrimSpace(b.String())
}

// RenderPartsAsPlain joins canonical MessageParts into a plain-text block
// with style markers, masked-link syntax, and code fences stripped. Used by
// coerceFormatForCaps as the final degradation when the target channel
// supports neither RichText nor Markdown.
func RenderPartsAsPlain(parts []MessagePart) string {
	if len(parts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case MessagePartText, MessagePartMention:
			t := strings.TrimSpace(part.Text)
			if t != "" {
				lines = append(lines, t)
			}
		case MessagePartLink:
			text := strings.TrimSpace(part.Text)
			url := strings.TrimSpace(part.URL)
			switch {
			case text != "" && url != "":
				lines = append(lines, text+" ("+url+")")
			case url != "":
				lines = append(lines, url)
			case text != "":
				lines = append(lines, text)
			}
		case MessagePartCodeBlock:
			t := strings.Trim(part.Text, "\n\r ")
			if t != "" {
				lines = append(lines, t)
			}
		case MessagePartEmoji:
			t := strings.TrimSpace(part.Text)
			if t == "" {
				t = strings.TrimSpace(part.Emoji)
			}
			if t != "" {
				lines = append(lines, t)
			}
		case MessagePartHeading:
			if t := CollapseMessagePartTextLine(part.Text); t != "" {
				lines = append(lines, t)
			}
		case MessagePartBlockquote:
			if t := renderPlainBlockquote(part.Text); t != "" {
				lines = append(lines, t)
			}
		case MessagePartListItem:
			if t := renderPlainListItem(part.Text); t != "" {
				lines = append(lines, t)
			}
		}
	}
	return strings.Join(lines, "\n\n")
}

func writeDegradeMarkdownInline(b *strings.Builder, text string, styles []MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(renderDegradeMarkdownStyled(text, styles))
}

func writeDegradeMarkdownLink(b *strings.Builder, part MessagePart) {
	url := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if url == "" || !isAllowedDegradeMarkdownHref(url) {
		if text == "" {
			return
		}
		writeDegradeMarkdownInline(b, text, part.Styles)
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	if text == "" {
		b.WriteString(url)
		return
	}
	b.WriteString("[")
	b.WriteString(escapeDegradeMarkdownLinkText(text))
	b.WriteString("](")
	b.WriteString(EscapeMessagePartLinkURL(url))
	b.WriteString(")")
}

func writeDegradeMarkdownCodeBlock(b *strings.Builder, part MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	fence := selectDegradeMarkdownBacktickFence(text, 3)
	lang := NormalizeMessagePartCodeLanguage(part.Language)
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n")
	b.WriteString(fence)
}

func writeDegradeMarkdownHeading(b *strings.Builder, part MessagePart) {
	text := CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("## ")
	b.WriteString(renderDegradeMarkdownStyled(text, part.Styles))
}

func writeDegradeMarkdownBlockquote(b *strings.Builder, part MessagePart) {
	lines := SplitMessagePartTextLines(part.Text)
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
			b.WriteString(renderDegradeMarkdownStyled(line, part.Styles))
		}
	}
}

func writeDegradeMarkdownListItem(b *strings.Builder, part MessagePart) {
	lines := SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("- ")
	b.WriteString(renderDegradeMarkdownStyled(lines[0], part.Styles))
	for _, line := range lines[1:] {
		b.WriteString("\n  ")
		if line != "" {
			b.WriteString(renderDegradeMarkdownStyled(line, part.Styles))
		}
	}
}

func renderDegradeMarkdownStyled(text string, styles []MessageTextStyle) string {
	if hasDegradeMarkdownStyle(styles, MessageStyleCode) {
		return wrapDegradeMarkdownInlineCode(text)
	}
	escaped := escapeDegradeMarkdownInline(text)
	if hasDegradeMarkdownStyle(styles, MessageStyleStrikethrough) {
		escaped = "~~" + escaped + "~~"
	}
	if hasDegradeMarkdownStyle(styles, MessageStyleItalic) {
		escaped = "*" + escaped + "*"
	}
	if hasDegradeMarkdownStyle(styles, MessageStyleBold) {
		escaped = "**" + escaped + "**"
	}
	return escaped
}

func hasDegradeMarkdownStyle(styles []MessageTextStyle, want MessageTextStyle) bool {
	for _, s := range styles {
		if s == want {
			return true
		}
	}
	return false
}

// NormalizeMessagePartCodeLanguage returns a safe code-fence language hint.
func NormalizeMessagePartCodeLanguage(language string) string {
	language = strings.TrimSpace(language)
	if language == "" || len(language) > 32 {
		return ""
	}
	for _, r := range language {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '+' || r == '#' {
			continue
		}
		return ""
	}
	return language
}

func isAllowedDegradeMarkdownHref(href string) bool {
	href = strings.TrimSpace(href)
	return strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "http://") ||
		strings.HasPrefix(href, "mailto:") ||
		strings.HasPrefix(href, "tel:")
}

var escapeDegradeMarkdownInline = strings.NewReplacer(
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

func escapeDegradeMarkdownLinkText(text string) string {
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.ReplaceAll(text, "\n", " ")
	return escapeDegradeMarkdownInline(text)
}

// EscapeMessagePartLinkURL percent-encodes characters that can break platform
// rich-link wrappers while preserving ordinary http(s), mailto, tel and
// platform-specific URL prefixes.
func EscapeMessagePartLinkURL(url string) string {
	url = strings.ReplaceAll(strings.TrimSpace(url), "\n", "")
	url = strings.ReplaceAll(url, "\r", "")
	url = strings.ReplaceAll(url, " ", "%20")
	url = strings.ReplaceAll(url, "<", "%3C")
	url = strings.ReplaceAll(url, ">", "%3E")
	url = strings.ReplaceAll(url, "|", "%7C")
	url = strings.ReplaceAll(url, "(", "%28")
	url = strings.ReplaceAll(url, ")", "%29")
	return url
}

func selectDegradeMarkdownBacktickFence(text string, minRun int) string {
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

func wrapDegradeMarkdownInlineCode(text string) string {
	fence := selectDegradeMarkdownBacktickFence(text, 1)
	pad := ""
	if strings.ContainsRune(text, '`') {
		pad = " "
	}
	return fence + pad + text + pad + fence
}

// CollapseMessagePartTextLine normalizes block part text into one visible line.
func CollapseMessagePartTextLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// SplitMessagePartTextLines normalizes block part text into trimmed display lines.
func SplitMessagePartTextLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Trim(text, "\n\t ")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

func renderPlainBlockquote(text string) string {
	lines := SplitMessagePartTextLines(text)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(">")
		if line != "" {
			b.WriteString(" ")
			b.WriteString(line)
		}
	}
	return b.String()
}

func renderPlainListItem(text string) string {
	lines := SplitMessagePartTextLines(text)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		b.WriteString("\n  ")
		b.WriteString(line)
	}
	return b.String()
}

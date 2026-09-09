package matrix

import (
	"bytes"
	stdhtml "html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldhtml "github.com/yuin/goldmark/renderer/html"

	"github.com/felinics/memoh/internal/channel"
)

const matrixHTMLFormat = "org.matrix.custom.html"

var matrixMarkdownRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(
		goldhtml.WithHardWraps(),
	),
)

type matrixFormattedMessage struct {
	Body          string
	FormattedBody string
	HasHTML       bool
}

var (
	matrixTaskListPattern = regexp.MustCompile(`^(\s*(?:[-*+]\s+|\d+\.\s+))\[( |x|X)\]\s+(.*)$`)
	matrixTableAlignCell  = regexp.MustCompile(`^:?-{3,}:?$`)
)

func formatMatrixMessage(msg channel.Message) matrixFormattedMessage {
	if len(msg.Parts) > 0 {
		body := strings.TrimSpace(channel.RenderPartsAsPlain(msg.Parts))
		formatted := matrixFormattedMessage{Body: body}
		htmlBody := renderMatrixMessagePartsHTML(msg.Parts)
		if htmlBody != "" {
			formatted.FormattedBody = htmlBody
			formatted.HasHTML = true
		}
		return formatted
	}
	body := strings.TrimSpace(msg.PlainText())
	formatted := matrixFormattedMessage{Body: body}
	if msg.Format != channel.MessageFormatMarkdown || body == "" {
		return formatted
	}
	body = normalizeMatrixMarkdown(body)
	formatted.Body = body
	htmlBody, err := renderMatrixMarkdown(body)
	if err != nil || strings.TrimSpace(htmlBody) == "" {
		return formatted
	}
	formatted.FormattedBody = htmlBody
	formatted.HasHTML = true
	return formatted
}

func matrixMessageBody(msg channel.Message) string {
	return strings.TrimSpace(formatMatrixMessage(msg).Body)
}

func renderMatrixMessagePartsHTML(parts []channel.MessagePart) string {
	var b strings.Builder
	for _, part := range parts {
		switch part.Type {
		case channel.MessagePartText:
			writeMatrixRichInlinePart(&b, part.Text, part.Styles)
		case channel.MessagePartLink:
			writeMatrixRichLinkPart(&b, part)
		case channel.MessagePartCodeBlock:
			writeMatrixRichCodeBlockPart(&b, part)
		case channel.MessagePartMention:
			writeMatrixRichMentionPart(&b, part)
		case channel.MessagePartEmoji:
			text := part.Text
			if strings.TrimSpace(text) == "" {
				text = part.Emoji
			}
			writeMatrixRichInlinePart(&b, text, nil)
		case channel.MessagePartHeading:
			writeMatrixRichHeadingPart(&b, part)
		case channel.MessagePartBlockquote:
			writeMatrixRichBlockquotePart(&b, part)
		case channel.MessagePartListItem:
			writeMatrixRichListItemPart(&b, part)
		default:
			continue
		}
	}
	return b.String()
}

func writeMatrixRichInlinePart(b *strings.Builder, text string, styles []channel.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.WriteString("<p>")
	b.WriteString(renderMatrixRichStyledInline(text, styles))
	b.WriteString("</p>")
}

func writeMatrixRichLinkPart(b *strings.Builder, part channel.MessagePart) {
	rawURL := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if text == "" {
		text = rawURL
	}
	if text == "" {
		return
	}
	if rawURL == "" || !channel.IsHTTPURL(rawURL) {
		writeMatrixRichInlinePart(b, text, part.Styles)
		return
	}
	b.WriteString(`<p><a href="`)
	b.WriteString(stdhtml.EscapeString(rawURL))
	b.WriteString(`">`)
	b.WriteString(stdhtml.EscapeString(text))
	b.WriteString("</a></p>")
}

func writeMatrixRichMentionPart(b *strings.Builder, part channel.MessagePart) {
	id := strings.TrimSpace(part.ChannelIdentityID)
	text := strings.TrimSpace(part.Text)
	if !isMatrixUserID(id) || text == "" {
		writeMatrixRichInlinePart(b, part.Text, part.Styles)
		return
	}
	b.WriteString(`<p><a href="https://matrix.to/#/`)
	b.WriteString(stdhtml.EscapeString(id))
	b.WriteString(`">`)
	b.WriteString(stdhtml.EscapeString(text))
	b.WriteString("</a></p>")
}

func writeMatrixRichCodeBlockPart(b *strings.Builder, part channel.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	lang := channel.NormalizeMessagePartCodeLanguage(part.Language)
	b.WriteString("<pre><code")
	if lang != "" {
		b.WriteString(` class="language-`)
		b.WriteString(stdhtml.EscapeString(lang))
		b.WriteString(`"`)
	}
	b.WriteString(">")
	b.WriteString(stdhtml.EscapeString(text))
	b.WriteString("</code></pre>")
}

func writeMatrixRichHeadingPart(b *strings.Builder, part channel.MessagePart) {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	b.WriteString("<h2>")
	b.WriteString(renderMatrixRichStyledInline(text, part.Styles))
	b.WriteString("</h2>")
}

func writeMatrixRichBlockquotePart(b *strings.Builder, part channel.MessagePart) {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	b.WriteString("<blockquote>")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("<br>")
		}
		if line != "" {
			b.WriteString(renderMatrixRichStyledInline(line, part.Styles))
		}
	}
	b.WriteString("</blockquote>")
}

func writeMatrixRichListItemPart(b *strings.Builder, part channel.MessagePart) {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	b.WriteString("<ul><li>")
	b.WriteString(renderMatrixRichStyledInline(lines[0], part.Styles))
	for _, line := range lines[1:] {
		b.WriteString("<br>")
		if line != "" {
			b.WriteString(renderMatrixRichStyledInline(line, part.Styles))
		}
	}
	b.WriteString("</li></ul>")
}

func renderMatrixRichStyledInline(text string, styles []channel.MessageTextStyle) string {
	escaped := stdhtml.EscapeString(text)
	if hasMatrixRichTextStyle(styles, channel.MessageStyleCode) {
		return "<code>" + escaped + "</code>"
	}
	if hasMatrixRichTextStyle(styles, channel.MessageStyleSpoiler) {
		escaped = "<span data-mx-spoiler>" + escaped + "</span>"
	}
	if hasMatrixRichTextStyle(styles, channel.MessageStyleStrikethrough) {
		escaped = "<del>" + escaped + "</del>"
	}
	if hasMatrixRichTextStyle(styles, channel.MessageStyleUnderline) {
		escaped = "<u>" + escaped + "</u>"
	}
	if hasMatrixRichTextStyle(styles, channel.MessageStyleItalic) {
		escaped = "<em>" + escaped + "</em>"
	}
	if hasMatrixRichTextStyle(styles, channel.MessageStyleBold) {
		escaped = "<strong>" + escaped + "</strong>"
	}
	return escaped
}

func hasMatrixRichTextStyle(styles []channel.MessageTextStyle, want channel.MessageTextStyle) bool {
	for _, style := range styles {
		if style == want {
			return true
		}
	}
	return false
}

func isMatrixUserID(id string) bool {
	if !strings.HasPrefix(id, "@") || !strings.Contains(id, ":") {
		return false
	}
	return !strings.ContainsAny(id, " \t\r\n<>\"'")
}

func renderMatrixMarkdown(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	var buf bytes.Buffer
	if err := matrixMarkdownRenderer.Convert([]byte(text), &buf); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func normalizeMatrixMarkdown(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if isFenceLine(trimmed) {
			inFence = !inFence
			result = append(result, line)
			continue
		}
		if !inFence && i+1 < len(lines) && isMarkdownTableHeader(line, lines[i+1]) {
			block := []string{line, lines[i+1]}
			i += 2
			for i < len(lines) && isMarkdownTableRow(lines[i]) {
				block = append(block, lines[i])
				i++
			}
			i--
			result = append(result, "```text")
			result = append(result, block...)
			result = append(result, "```")
			continue
		}
		if !inFence {
			line = normalizeMatrixTaskListLine(line)
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func normalizeMatrixTaskListLine(line string) string {
	matches := matrixTaskListPattern.FindStringSubmatch(line)
	if len(matches) != 4 {
		return line
	}
	box := "☐"
	if strings.EqualFold(matches[2], "x") {
		box = "☑"
	}
	return matches[1] + box + " " + matches[3]
}

func isFenceLine(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func isMarkdownTableHeader(headerLine, delimiterLine string) bool {
	if !strings.Contains(headerLine, "|") {
		return false
	}
	return isMarkdownTableDelimiter(delimiterLine)
}

func isMarkdownTableDelimiter(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") {
		return false
	}
	parts := strings.Split(trimmed, "|")
	validCells := 0
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		if cell == "" {
			continue
		}
		if !matrixTableAlignCell.MatchString(cell) {
			return false
		}
		validCells++
	}
	return validCells >= 1
}

func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && strings.Contains(trimmed, "|")
}

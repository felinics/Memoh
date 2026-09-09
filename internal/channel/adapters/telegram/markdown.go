package telegram

import (
	"fmt"
	"regexp"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

const (
	codeBlockPlaceholder  = "\x00CB"
	inlineCodePlaceholder = "\x00IC"
)

var (
	reInlineCode = regexp.MustCompile("`([^`\\n]+?)`")
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reLink       = regexp.MustCompile(`\[([^\]]+?)\]\(([^)]+?)\)`)
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reListBullet = regexp.MustCompile(`(?m)^(\s*)[-+]\s`)
	reItalic     = regexp.MustCompile(`\*([^*\n]+?)\*`)
)

// formatTelegramOutput converts standard markdown to Telegram-compatible HTML
// when the message format is markdown. Returns the formatted text and the
// Telegram parse mode to use.
func formatTelegramOutput(text string, format channel.MessageFormat) (string, string) {
	if format == channel.MessageFormatMarkdown && strings.TrimSpace(text) != "" {
		return markdownToTelegramHTML(text), tele.ModeHTML
	}
	return text, ""
}

func renderTelegramMarkdownMathRichMessage(msg channel.Message) telegramInputRichMessage {
	if len(msg.Parts) > 0 {
		return telegramInputRichMessage{}
	}
	switch msg.Format {
	case "", channel.MessageFormatPlain, channel.MessageFormatMarkdown:
	default:
		return telegramInputRichMessage{}
	}
	markdown := strings.TrimSpace(msg.PlainText())
	if markdown == "" {
		return telegramInputRichMessage{}
	}
	markdown = normalizeTelegramRichMarkdownMath(markdown)
	if !telegramMarkdownHasMath(markdown) {
		return telegramInputRichMessage{}
	}
	return telegramInputRichMessage{Markdown: markdown}
}

func normalizeTelegramRichMarkdownMath(text string) string {
	text = replacePairedTelegramLatexDelimiters(text, `\[`, `\]`, "$$", "$$")
	text = replacePairedTelegramLatexDelimiters(text, `\(`, `\)`, "$", "$")
	return text
}

func replacePairedTelegramLatexDelimiters(text, openDelim, closeDelim, richOpen, richClose string) string {
	var b strings.Builder
	for {
		start := strings.Index(text, openDelim)
		if start < 0 {
			b.WriteString(text)
			return b.String()
		}
		contentStart := start + len(openDelim)
		endRel := strings.Index(text[contentStart:], closeDelim)
		if endRel < 0 {
			b.WriteString(text)
			return b.String()
		}
		end := contentStart + endRel
		b.WriteString(text[:start])
		b.WriteString(richOpen)
		b.WriteString(text[contentStart:end])
		b.WriteString(richClose)
		text = text[end+len(closeDelim):]
	}
}

func telegramMarkdownHasMath(text string) bool {
	if strings.Contains(text, "```math") ||
		strings.Contains(text, "$$") ||
		(strings.Contains(text, `\(`) && strings.Contains(text, `\)`)) ||
		(strings.Contains(text, `\[`) && strings.Contains(text, `\]`)) {
		return true
	}
	return hasTelegramInlineDollarMath(text)
}

func hasTelegramInlineDollarMath(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '$' || isEscapedTelegramMarkdownByte(text, i) {
			continue
		}
		if i+1 >= len(text) || text[i+1] == '$' || isTelegramMarkdownSpace(text[i+1]) || isTelegramMarkdownDigit(text[i+1]) {
			continue
		}
		for j := i + 1; j < len(text); j++ {
			if text[j] != '$' || isEscapedTelegramMarkdownByte(text, j) {
				continue
			}
			content := strings.TrimSpace(text[i+1 : j])
			if content != "" && !strings.ContainsAny(content, "\r\n") {
				return true
			}
			break
		}
	}
	return false
}

func isEscapedTelegramMarkdownByte(text string, idx int) bool {
	backslashes := 0
	for i := idx - 1; i >= 0 && text[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func isTelegramMarkdownSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isTelegramMarkdownDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// markdownToTelegramHTML converts standard markdown to Telegram-compatible HTML.
//
// Supported conversions:
//   - Fenced code blocks (```lang ... ```) → <pre><code>
//   - Inline code (`code`) → <code>
//   - Bold (**text**) → <b>
//   - Italic (*text*) → <i>
//   - Strikethrough (~~text~~) → <s>
//   - Links ([text](url)) → <a href>
//   - Headings (# text) → <b>
//   - Unordered lists (- item) → bullet
//   - Block quotes (> text) → <blockquote>
func markdownToTelegramHTML(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	// Split by fenced code blocks (``` ... ```).
	// Even-indexed segments are normal text, odd-indexed are code content.
	segments := splitCodeBlocks(text)
	var buf strings.Builder
	for i, seg := range segments {
		if i%2 == 0 {
			buf.WriteString(convertInlineMarkdown(seg))
		} else {
			lang, code := extractCodeBlockLang(seg)
			escaped := telegramEscapeHTML(strings.TrimRight(code, "\n"))
			if lang != "" {
				fmt.Fprintf(&buf, "<pre><code class=\"language-%s\">%s</code></pre>", lang, escaped)
			} else {
				buf.WriteString("<pre>" + escaped + "</pre>")
			}
		}
	}
	return strings.TrimSpace(buf.String())
}

// splitCodeBlocks splits text by triple-backtick fences.
// Returns alternating [normal, code, normal, code, ...] segments.
func splitCodeBlocks(text string) []string {
	const fence = "```"
	var segments []string
	for {
		start := strings.Index(text, fence)
		if start < 0 {
			segments = append(segments, text)
			break
		}
		segments = append(segments, text[:start])
		rest := text[start+len(fence):]
		end := strings.Index(rest, fence)
		if end < 0 {
			// Unclosed code block: treat remainder as normal text.
			segments = append(segments, text[start:])
			// Remove the last normal segment and replace with full remainder.
			segments[len(segments)-2] = segments[len(segments)-2] + segments[len(segments)-1]
			segments = segments[:len(segments)-1]
			break
		}
		segments = append(segments, rest[:end])
		text = rest[end+len(fence):]
	}
	return segments
}

// extractCodeBlockLang separates the optional language tag from code content.
func extractCodeBlockLang(block string) (string, string) {
	idx := strings.IndexByte(block, '\n')
	if idx < 0 {
		// Single line: check if it looks like a language tag.
		trimmed := strings.TrimSpace(block)
		if trimmed != "" && !strings.Contains(trimmed, " ") && len(trimmed) <= 20 {
			return trimmed, ""
		}
		return "", block
	}
	firstLine := strings.TrimSpace(block[:idx])
	rest := block[idx+1:]
	if firstLine != "" && !strings.Contains(firstLine, " ") && len(firstLine) <= 20 {
		return firstLine, rest
	}
	// No language tag: strip leading newline from content.
	return "", strings.TrimLeft(block, "\n")
}

// convertInlineMarkdown converts inline markdown formatting to Telegram HTML.
func convertInlineMarkdown(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	// Protect inline code spans from further processing.
	var inlineCodes []string
	text = reInlineCode.ReplaceAllStringFunc(text, func(match string) string {
		idx := len(inlineCodes)
		inlineCodes = append(inlineCodes, match)
		return fmt.Sprintf("%s%d\x00", inlineCodePlaceholder, idx)
	})

	// Escape HTML entities.
	text = telegramEscapeHTML(text)

	// Bold: **text** → <b>text</b> (must run before italic).
	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	// Strikethrough: ~~text~~ → <s>text</s>.
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// Links: [text](url) → <a href="url">text</a>.
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// Headings: # text → bold line.
	text = reHeading.ReplaceAllString(text, "<b>$1</b>")

	// Unordered lists: - item / + item → bullet.
	text = reListBullet.ReplaceAllString(text, "${1}• ")

	// Italic: *text* → <i>text</i> (after bold, so ** is already consumed).
	text = reItalic.ReplaceAllString(text, "<i>$1</i>")

	// Block quotes: > text → <blockquote>.
	text = convertBlockquotes(text)

	// Restore inline code spans.
	for i, original := range inlineCodes {
		sub := reInlineCode.FindStringSubmatch(original)
		content := ""
		if len(sub) >= 2 {
			content = sub[1]
		}
		placeholder := fmt.Sprintf("%s%d\x00", inlineCodePlaceholder, i)
		text = strings.Replace(text, placeholder, "<code>"+telegramEscapeHTML(content)+"</code>", 1)
	}

	return text
}

// convertBlockquotes converts markdown block quotes to Telegram HTML blockquotes.
// After HTML escaping, ">" becomes "&gt;", so we match the escaped form.
func convertBlockquotes(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	var quoteLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "&gt; ") || trimmed == "&gt;" {
			content := strings.TrimPrefix(trimmed, "&gt; ")
			if content == "&gt;" {
				content = ""
			}
			quoteLines = append(quoteLines, content)
		} else {
			if len(quoteLines) > 0 {
				result = append(result, "<blockquote>"+strings.Join(quoteLines, "\n")+"</blockquote>")
				quoteLines = nil
			}
			result = append(result, line)
		}
	}
	if len(quoteLines) > 0 {
		result = append(result, "<blockquote>"+strings.Join(quoteLines, "\n")+"</blockquote>")
	}
	return strings.Join(result, "\n")
}

// telegramEscapeHTML escapes characters that are special in HTML.
func telegramEscapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

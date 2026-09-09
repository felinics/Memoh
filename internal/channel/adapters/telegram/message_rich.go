package telegram

import (
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

func renderTelegramMessagePartsRichMessage(msg channel.Message) telegramInputRichMessage {
	if len(msg.Parts) == 0 {
		return telegramInputRichMessage{}
	}
	var b strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case channel.MessagePartText:
			writeTelegramRichInlinePart(&b, part.Text, part.Styles)
		case channel.MessagePartLink:
			writeTelegramRichLinkPart(&b, part)
		case channel.MessagePartCodeBlock:
			writeTelegramRichCodeBlockPart(&b, part)
		case channel.MessagePartMention:
			writeTelegramRichMentionPart(&b, part)
		case channel.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeTelegramRichInlinePart(&b, text, part.Styles)
		case channel.MessagePartHeading:
			writeTelegramRichHeadingPart(&b, part)
		case channel.MessagePartBlockquote:
			writeTelegramRichBlockquotePart(&b, part)
		case channel.MessagePartListItem:
			writeTelegramRichListItemPart(&b, part)
		}
	}
	html := strings.TrimSpace(b.String())
	if html == "" {
		return telegramInputRichMessage{}
	}
	return telegramInputRichMessage{HTML: html, SkipEntityDetection: true}
}

func renderTelegramPartsFallbackText(msg channel.Message) (string, string) {
	if len(msg.Parts) == 0 {
		text := strings.TrimSpace(msg.PlainText())
		return formatTelegramOutput(text, msg.Format)
	}
	return renderTelegramMessagePartsHTMLFallback(msg), tele.ModeHTML
}

func renderTelegramMessagePartsHTMLFallback(msg channel.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case channel.MessagePartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				blocks = append(blocks, renderTelegramStyledInline(text, part.Styles))
			}
		case channel.MessagePartLink:
			if block := renderTelegramLinkFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case channel.MessagePartCodeBlock:
			if block := renderTelegramCodeBlockFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case channel.MessagePartMention:
			if block := renderTelegramMentionFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case channel.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			if text != "" {
				blocks = append(blocks, renderTelegramStyledInline(text, part.Styles))
			}
		case channel.MessagePartHeading:
			if block := renderTelegramHeadingFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case channel.MessagePartBlockquote:
			if block := renderTelegramBlockquoteFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case channel.MessagePartListItem:
			if block := renderTelegramListItemFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		}
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func renderTelegramLinkFallback(part channel.MessagePart) string {
	url := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if text == "" {
		text = url
	}
	if text == "" {
		return ""
	}
	if url == "" || !isAllowedTelegramRichHref(url) {
		return renderTelegramStyledInline(text, part.Styles)
	}
	url = channel.EscapeMessagePartLinkURL(url)
	return `<a href="` + telegramEscapeAttr(url) + `">` + telegramEscapeHTML(text) + `</a>`
}

func renderTelegramMentionFallback(part channel.MessagePart) string {
	id := strings.TrimSpace(part.ChannelIdentityID)
	text := strings.TrimSpace(part.Text)
	if id == "" || !isTelegramNumericMentionID(id) || text == "" {
		return renderTelegramStyledInline(part.Text, part.Styles)
	}
	return `<a href="tg://user?id=` + id + `">` + telegramEscapeHTML(text) + `</a>`
}

func renderTelegramCodeBlockFallback(part channel.MessagePart) string {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lang := channel.NormalizeMessagePartCodeLanguage(part.Language)
	if lang != "" {
		return `<pre><code class="language-` + telegramEscapeAttr(lang) + `">` + telegramEscapeHTML(text) + `</code></pre>`
	}
	return "<pre>" + telegramEscapeHTML(text) + "</pre>"
}

func renderTelegramHeadingFallback(part channel.MessagePart) string {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return ""
	}
	return "<b>" + telegramEscapeHTML(text) + "</b>"
}

func renderTelegramBlockquoteFallback(part channel.MessagePart) string {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return ""
	}
	return "<blockquote>" + telegramEscapeHTML(strings.Join(lines, "\n")) + "</blockquote>"
}

func renderTelegramListItemFallback(part channel.MessagePart) string {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return ""
	}
	return renderTelegramStyledInline("- "+text, part.Styles)
}

func writeTelegramRichInlinePart(b *strings.Builder, text string, styles []channel.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, renderTelegramStyledInline(text, styles))
}

func writeTelegramRichLinkPart(b *strings.Builder, part channel.MessagePart) {
	url := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if text == "" {
		text = url
	}
	if text == "" {
		return
	}
	if url == "" || !isAllowedTelegramRichHref(url) {
		writeTelegramRichParagraph(b, renderTelegramStyledInline(text, part.Styles))
		return
	}
	url = channel.EscapeMessagePartLinkURL(url)
	link := `<a href="` + telegramEscapeAttr(url) + `">` + telegramEscapeHTML(text) + `</a>`
	writeTelegramRichParagraph(b, link)
}

// writeTelegramRichMentionPart emits Telegram's tg://user?id=… profile
// link when the canonical Part carries a numeric Telegram user id.
// Telegram user IDs are positive integers, so the safe character class is
// digits only; IDs outside that class fall back to the inline-text path so
// the visible mention still reaches the channel (and Telegram's
// auto-detection can still light up @-prefixed public usernames in plain
// text).
func writeTelegramRichMentionPart(b *strings.Builder, part channel.MessagePart) {
	id := strings.TrimSpace(part.ChannelIdentityID)
	text := strings.TrimSpace(part.Text)
	if id == "" || !isTelegramNumericMentionID(id) || text == "" {
		writeTelegramRichInlinePart(b, part.Text, part.Styles)
		return
	}
	writeTelegramRichParagraph(b, `<a href="tg://user?id=`+id+`">`+telegramEscapeHTML(text)+`</a>`)
}

func isTelegramNumericMentionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeTelegramRichCodeBlockPart(b *strings.Builder, part channel.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	lang := channel.NormalizeMessagePartCodeLanguage(part.Language)
	b.WriteString("<pre>")
	if lang != "" {
		b.WriteString(`<code class="language-`)
		b.WriteString(telegramEscapeAttr(lang))
		b.WriteString(`">`)
		b.WriteString(telegramEscapeHTML(text))
		b.WriteString("</code>")
	} else {
		b.WriteString(telegramEscapeHTML(text))
	}
	b.WriteString("</pre>")
}

func writeTelegramRichHeadingPart(b *strings.Builder, part channel.MessagePart) {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, "<b>"+telegramEscapeHTML(text)+"</b>")
}

func writeTelegramRichBlockquotePart(b *strings.Builder, part channel.MessagePart) {
	lines := channel.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	b.WriteString("<blockquote>")
	b.WriteString(telegramEscapeHTML(strings.Join(lines, "\n")))
	b.WriteString("</blockquote>")
}

func writeTelegramRichListItemPart(b *strings.Builder, part channel.MessagePart) {
	text := channel.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, renderTelegramStyledInline("- "+text, part.Styles))
}

func renderTelegramStyledInline(text string, styles []channel.MessageTextStyle) string {
	html := telegramEscapeHTML(text)
	if hasTelegramTextStyle(styles, channel.MessageStyleCode) {
		return "<code>" + html + "</code>"
	}
	if hasTelegramTextStyle(styles, channel.MessageStyleSpoiler) {
		html = "<tg-spoiler>" + html + "</tg-spoiler>"
	}
	if hasTelegramTextStyle(styles, channel.MessageStyleStrikethrough) {
		html = "<s>" + html + "</s>"
	}
	if hasTelegramTextStyle(styles, channel.MessageStyleUnderline) {
		html = "<u>" + html + "</u>"
	}
	if hasTelegramTextStyle(styles, channel.MessageStyleItalic) {
		html = "<i>" + html + "</i>"
	}
	if hasTelegramTextStyle(styles, channel.MessageStyleBold) {
		html = "<b>" + html + "</b>"
	}
	return html
}

func hasTelegramTextStyle(styles []channel.MessageTextStyle, want channel.MessageTextStyle) bool {
	for _, style := range styles {
		if style == want {
			return true
		}
	}
	return false
}

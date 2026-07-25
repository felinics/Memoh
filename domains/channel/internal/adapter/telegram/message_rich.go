package telegram

import (
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/memohai/memoh/domains/channel/gateway"
)

func renderTelegramMessagePartsRichMessage(msg gateway.Message) telegramInputRichMessage {
	if len(msg.Parts) == 0 {
		return telegramInputRichMessage{}
	}
	var b strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case gateway.MessagePartText:
			writeTelegramRichInlinePart(&b, part.Text, part.Styles)
		case gateway.MessagePartLink:
			writeTelegramRichLinkPart(&b, part)
		case gateway.MessagePartCodeBlock:
			writeTelegramRichCodeBlockPart(&b, part)
		case gateway.MessagePartMention:
			writeTelegramRichMentionPart(&b, part)
		case gateway.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeTelegramRichInlinePart(&b, text, part.Styles)
		case gateway.MessagePartHeading:
			writeTelegramRichHeadingPart(&b, part)
		case gateway.MessagePartBlockquote:
			writeTelegramRichBlockquotePart(&b, part)
		case gateway.MessagePartListItem:
			writeTelegramRichListItemPart(&b, part)
		}
	}
	html := strings.TrimSpace(b.String())
	if html == "" {
		return telegramInputRichMessage{}
	}
	return telegramInputRichMessage{HTML: html, SkipEntityDetection: true}
}

func renderTelegramPartsFallbackText(msg gateway.Message) (string, string) {
	if len(msg.Parts) == 0 {
		text := strings.TrimSpace(msg.PlainText())
		return formatTelegramOutput(text, msg.Format)
	}
	return renderTelegramMessagePartsHTMLFallback(msg), tele.ModeHTML
}

func renderTelegramMessagePartsHTMLFallback(msg gateway.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Type {
		case gateway.MessagePartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				blocks = append(blocks, renderTelegramStyledInline(text, part.Styles))
			}
		case gateway.MessagePartLink:
			if block := renderTelegramLinkFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case gateway.MessagePartCodeBlock:
			if block := renderTelegramCodeBlockFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case gateway.MessagePartMention:
			if block := renderTelegramMentionFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case gateway.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			if text != "" {
				blocks = append(blocks, renderTelegramStyledInline(text, part.Styles))
			}
		case gateway.MessagePartHeading:
			if block := renderTelegramHeadingFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case gateway.MessagePartBlockquote:
			if block := renderTelegramBlockquoteFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		case gateway.MessagePartListItem:
			if block := renderTelegramListItemFallback(part); block != "" {
				blocks = append(blocks, block)
			}
		}
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

func renderTelegramLinkFallback(part gateway.MessagePart) string {
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
	url = gateway.EscapeMessagePartLinkURL(url)
	return `<a href="` + telegramEscapeAttr(url) + `">` + telegramEscapeHTML(text) + `</a>`
}

func renderTelegramMentionFallback(part gateway.MessagePart) string {
	id := strings.TrimSpace(part.ChannelIdentityID)
	text := strings.TrimSpace(part.Text)
	if id == "" || !isTelegramNumericMentionID(id) || text == "" {
		return renderTelegramStyledInline(part.Text, part.Styles)
	}
	return `<a href="tg://user?id=` + id + `">` + telegramEscapeHTML(text) + `</a>`
}

func renderTelegramCodeBlockFallback(part gateway.MessagePart) string {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lang := gateway.NormalizeMessagePartCodeLanguage(part.Language)
	if lang != "" {
		return `<pre><code class="language-` + telegramEscapeAttr(lang) + `">` + telegramEscapeHTML(text) + `</code></pre>`
	}
	return "<pre>" + telegramEscapeHTML(text) + "</pre>"
}

func renderTelegramHeadingFallback(part gateway.MessagePart) string {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return ""
	}
	return "<b>" + telegramEscapeHTML(text) + "</b>"
}

func renderTelegramBlockquoteFallback(part gateway.MessagePart) string {
	lines := gateway.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return ""
	}
	return "<blockquote>" + telegramEscapeHTML(strings.Join(lines, "\n")) + "</blockquote>"
}

func renderTelegramListItemFallback(part gateway.MessagePart) string {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return ""
	}
	return renderTelegramStyledInline("- "+text, part.Styles)
}

func writeTelegramRichInlinePart(b *strings.Builder, text string, styles []gateway.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, renderTelegramStyledInline(text, styles))
}

func writeTelegramRichLinkPart(b *strings.Builder, part gateway.MessagePart) {
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
	url = gateway.EscapeMessagePartLinkURL(url)
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
func writeTelegramRichMentionPart(b *strings.Builder, part gateway.MessagePart) {
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

func writeTelegramRichCodeBlockPart(b *strings.Builder, part gateway.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	lang := gateway.NormalizeMessagePartCodeLanguage(part.Language)
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

func writeTelegramRichHeadingPart(b *strings.Builder, part gateway.MessagePart) {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, "<b>"+telegramEscapeHTML(text)+"</b>")
}

func writeTelegramRichBlockquotePart(b *strings.Builder, part gateway.MessagePart) {
	lines := gateway.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	b.WriteString("<blockquote>")
	b.WriteString(telegramEscapeHTML(strings.Join(lines, "\n")))
	b.WriteString("</blockquote>")
}

func writeTelegramRichListItemPart(b *strings.Builder, part gateway.MessagePart) {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	writeTelegramRichParagraph(b, renderTelegramStyledInline("- "+text, part.Styles))
}

func renderTelegramStyledInline(text string, styles []gateway.MessageTextStyle) string {
	html := telegramEscapeHTML(text)
	if hasTelegramTextStyle(styles, gateway.MessageStyleCode) {
		return "<code>" + html + "</code>"
	}
	if hasTelegramTextStyle(styles, gateway.MessageStyleSpoiler) {
		html = "<tg-spoiler>" + html + "</tg-spoiler>"
	}
	if hasTelegramTextStyle(styles, gateway.MessageStyleStrikethrough) {
		html = "<s>" + html + "</s>"
	}
	if hasTelegramTextStyle(styles, gateway.MessageStyleUnderline) {
		html = "<u>" + html + "</u>"
	}
	if hasTelegramTextStyle(styles, gateway.MessageStyleItalic) {
		html = "<i>" + html + "</i>"
	}
	if hasTelegramTextStyle(styles, gateway.MessageStyleBold) {
		html = "<b>" + html + "</b>"
	}
	return html
}

func hasTelegramTextStyle(styles []gateway.MessageTextStyle, want gateway.MessageTextStyle) bool {
	for _, style := range styles {
		if style == want {
			return true
		}
	}
	return false
}

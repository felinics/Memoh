package slack

import (
	"strings"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// Security model: Slack mrkdwn has no `\` escape, so we rely on entity
// encoding of <, >, and & — the only characters that can form a Slack tag
// (<url|text>, <@user>, <!channel>, etc.). This blocks all injection of
// clickable links, user/channel mentions, and broadcast pings. The remaining
// mrkdwn markers (*, _, ~, `) cannot be entity-escaped and may cause visual
// glitches when attacker-supplied text overlaps with style wrappers, but
// they cannot forge links or mentions, so they are a display concern only.

func renderSlackMessagePartsMrkdwn(msg gateway.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range msg.Parts {
		switch part.Type {
		case gateway.MessagePartText:
			writeSlackRichInlinePart(&b, part.Text, part.Styles)
		case gateway.MessagePartLink:
			writeSlackRichLinkPart(&b, part)
		case gateway.MessagePartCodeBlock:
			writeSlackRichCodeBlockPart(&b, part)
		case gateway.MessagePartMention:
			writeSlackRichMentionPart(&b, part)
		case gateway.MessagePartEmoji:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				text = strings.TrimSpace(part.Emoji)
			}
			writeSlackRichInlinePart(&b, text, part.Styles)
		case gateway.MessagePartHeading:
			writeSlackRichHeadingPart(&b, part)
		case gateway.MessagePartBlockquote:
			writeSlackRichBlockquotePart(&b, part)
		case gateway.MessagePartListItem:
			writeSlackRichListItemPart(&b, part)
		}
	}
	return strings.TrimSpace(b.String())
}

func writeSlackRichInlinePart(b *strings.Builder, text string, styles []gateway.MessageTextStyle) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(renderSlackRichStyledInline(slackEscapeMrkdwn(text), styles))
}

func writeSlackRichLinkPart(b *strings.Builder, part gateway.MessagePart) {
	url := strings.TrimSpace(part.URL)
	text := strings.TrimSpace(part.Text)
	if url == "" || !isAllowedSlackRichHref(url) {
		if text == "" {
			return
		}
		writeSlackRichInlinePart(b, text, part.Styles)
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("<")
	b.WriteString(slackEscapeMrkdwnURL(url))
	if text != "" {
		b.WriteString("|")
		b.WriteString(slackEscapeMrkdwnLinkText(text))
	}
	b.WriteString(">")
}

// writeSlackRichMentionPart emits Slack's <@USERID> ping when the canonical
// Part carries a safe identity. Slack IDs are uppercase-alphanumeric
// (U/W for users, C/G/D for channels, S for subteams); anything outside
// that class falls back to the inline-text path so the visible mention
// still reaches the channel even if it doesn't ping.
func writeSlackRichMentionPart(b *strings.Builder, part gateway.MessagePart) {
	id := strings.TrimSpace(part.ChannelIdentityID)
	if id == "" || !isSafeSlackMentionID(id) {
		writeSlackRichInlinePart(b, part.Text, part.Styles)
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("<@")
	b.WriteString(id)
	b.WriteString(">")
}

func isSafeSlackMentionID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func writeSlackRichCodeBlockPart(b *strings.Builder, part gateway.MessagePart) {
	text := strings.Trim(part.Text, "\n\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("```\n")
	b.WriteString(slackEscapeMrkdwn(text))
	b.WriteString("\n```")
}

func writeSlackRichHeadingPart(b *strings.Builder, part gateway.MessagePart) {
	text := gateway.CollapseMessagePartTextLine(part.Text)
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	styles := append([]gateway.MessageTextStyle{gateway.MessageStyleBold}, part.Styles...)
	b.WriteString(renderSlackRichStyledInline(slackEscapeMrkdwn(text), styles))
}

func writeSlackRichBlockquotePart(b *strings.Builder, part gateway.MessagePart) {
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
			b.WriteString(renderSlackRichStyledInline(slackEscapeMrkdwn(line), part.Styles))
		}
	}
}

func writeSlackRichListItemPart(b *strings.Builder, part gateway.MessagePart) {
	lines := gateway.SplitMessagePartTextLines(part.Text)
	if len(lines) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("- ")
	b.WriteString(renderSlackRichStyledInline(slackEscapeMrkdwn(lines[0]), part.Styles))
	for _, line := range lines[1:] {
		b.WriteString("\n  ")
		if line != "" {
			b.WriteString(renderSlackRichStyledInline(slackEscapeMrkdwn(line), part.Styles))
		}
	}
}

func renderSlackRichStyledInline(escaped string, styles []gateway.MessageTextStyle) string {
	if hasSlackRichTextStyle(styles, gateway.MessageStyleCode) {
		return "`" + escaped + "`"
	}
	if hasSlackRichTextStyle(styles, gateway.MessageStyleStrikethrough) {
		escaped = "~" + escaped + "~"
	}
	if hasSlackRichTextStyle(styles, gateway.MessageStyleItalic) {
		escaped = "_" + escaped + "_"
	}
	if hasSlackRichTextStyle(styles, gateway.MessageStyleBold) {
		escaped = "*" + escaped + "*"
	}
	return escaped
}

func hasSlackRichTextStyle(styles []gateway.MessageTextStyle, want gateway.MessageTextStyle) bool {
	for _, s := range styles {
		if s == want {
			return true
		}
	}
	return false
}

// slackEscapeMrkdwnURL strips characters that would prematurely terminate a
// <url|text> link sequence. Slack accepts raw URLs without entity escaping
// inside <...>, but a literal <, |, or > would corrupt the marker — a stray
// < would let attacker-controlled URL content open a nested Slack mrkdwn tag.
func slackEscapeMrkdwnURL(url string) string {
	return strings.ReplaceAll(gateway.EscapeMessagePartLinkURL(url), "|", "%7C")
}

func isAllowedSlackRichHref(href string) bool {
	href = strings.TrimSpace(href)
	return strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "http://") ||
		strings.HasPrefix(href, "mailto:") ||
		strings.HasPrefix(href, "tel:")
}

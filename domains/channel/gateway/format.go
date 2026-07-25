package gateway

import (
	"regexp"
	"strings"
)

// markdownPatterns lists the constructs that flag a string as Markdown. Compiled
// once at package init so the per-call ContainsMarkdown scan is a fixed-cost
// iteration over precompiled patterns instead of recompiling every regex on
// every call — important because ContainsMarkdown sits on hot paths (every
// streaming delta, every outbound normalization).
var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\*\*[^*]+\*\*`),
	regexp.MustCompile(`\*[^*]+\*`),
	regexp.MustCompile(`~~[^~]+~~`),
	regexp.MustCompile("`[^`]+`"),
	regexp.MustCompile("```[\\s\\S]*```"),
	regexp.MustCompile(`\[.+\]\(.+\)`),
	regexp.MustCompile(`(?m)^#{1,6}\s`),
	regexp.MustCompile(`(?m)^[-*]\s`),
	regexp.MustCompile(`(?m)^\d+\.\s`),
}

// ContainsMarkdown returns true if the text contains common Markdown constructs.
func ContainsMarkdown(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, p := range markdownPatterns {
		if p.MatchString(text) {
			return true
		}
	}
	return false
}

// StripInlineMarkup removes the inline Markdown markers (** and `) authored for
// capable channels, leaving clean text for plain-text-only channels.
//
// Scope: only ** (bold) and ` (code) are stripped, because those are the only
// inline markers the command renderers emit (MdBold/MdCode/CmdRef). Other
// constructs ContainsMarkdown recognizes — links [a](b), headings, list
// bullets — are intentionally NOT stripped: the renderers never produce them,
// so any such characters in a body are literal user/content text and must be
// preserved verbatim rather than mangled. Extend this (and coerceFormatForCaps)
// if a renderer ever starts emitting those constructs.
func StripInlineMarkup(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// coerceFormatForCaps degrades msg.Format when the target channel cannot
// render it. Called right before validateMessageCapabilities at the outbound
// boundary so a Rich- or Markdown-typed body destined for a less capable
// channel gets retyped instead of being rejected.
//
// Degradation rules (applied in order):
//   - Markdown body on a Rich-only channel: treat the body as literal text in
//     a single rich part so channel renderers can escape it for their syntax.
//   - Markdown body on a Plain-only channel: strip inline markup, retype Plain.
//   - Rich body (Parts) on a Markdown-capable channel: render Parts via the
//     canonical GFM degrader (RenderPartsAsMarkdown), retype Markdown.
//   - Rich body on a Plain-only channel: render Parts via RenderPartsAsPlain,
//     retype Plain.
//
// URL-only Actions on channels without any button support are downgraded to
// ordinary links. Callback Actions stay unsupported and are rejected by
// validateMessageCapabilities unless the channel advertises callback Buttons.
func coerceFormatForCaps(msg Message, caps ChannelCapabilities) Message {
	if !caps.RichText {
		msg = normalizeTextOnlyRichFormat(msg)
	}
	if msg.Format == MessageFormatMarkdown && !caps.Markdown {
		if caps.RichText && strings.TrimSpace(msg.Text) != "" {
			msg.Parts = []MessagePart{{Type: MessagePartText, Text: msg.Text}}
			msg.Text = ""
			msg.Format = MessageFormatRich
		} else if !caps.RichText {
			msg.Text = StripInlineMarkup(msg.Text)
			msg.Format = MessageFormatPlain
		}
	}
	if len(msg.Parts) > 0 && !caps.RichText {
		if caps.Markdown {
			msg.Text = RenderPartsAsMarkdown(msg.Parts)
			msg.Format = MessageFormatMarkdown
		} else {
			msg.Text = RenderPartsAsPlain(msg.Parts)
			msg.Format = MessageFormatPlain
		}
		msg.Parts = nil
	}
	if len(msg.Actions) > 0 && !caps.Buttons && !caps.URLButtons {
		msg = coerceURLActionsForCaps(msg, caps)
	}
	return msg
}

func normalizeTextOnlyRichFormat(msg Message) Message {
	if msg.Format != MessageFormatRich || len(msg.Parts) > 0 || strings.TrimSpace(msg.Text) == "" {
		return msg
	}
	if ContainsMarkdown(msg.Text) {
		msg.Format = MessageFormatMarkdown
	} else {
		msg.Format = MessageFormatPlain
	}
	return msg
}

func coerceURLActionsForCaps(msg Message, caps ChannelCapabilities) Message {
	parts, ok := urlActionParts(msg.Actions)
	if !ok {
		return msg
	}
	switch {
	case caps.RichText && (msg.Format == MessageFormatRich || len(msg.Parts) > 0):
		msg = appendRichURLActionParts(msg, parts)
		msg.Format = MessageFormatRich
	case msg.Format == MessageFormatMarkdown && caps.Markdown:
		msg.Text = appendTextSection(msg.Text, RenderPartsAsMarkdown(parts))
	case msg.Format == MessageFormatMarkdown && !caps.Markdown && caps.RichText:
		msg = appendRichURLActionParts(msg, parts)
		msg.Format = MessageFormatRich
	default:
		msg.Text = appendTextSection(msg.Text, RenderPartsAsPlain(parts))
		if msg.Format == "" {
			msg.Format = MessageFormatPlain
		}
	}
	msg.Actions = nil
	return msg
}

func appendRichURLActionParts(msg Message, parts []MessagePart) Message {
	if strings.TrimSpace(msg.Text) != "" {
		msg.Parts = append([]MessagePart{{Type: MessagePartText, Text: msg.Text}}, msg.Parts...)
		msg.Text = ""
	}
	msg.Parts = append(msg.Parts, parts...)
	return msg
}

func urlActionParts(actions []Action) ([]MessagePart, bool) {
	parts := make([]MessagePart, 0, len(actions))
	for _, action := range actions {
		if strings.TrimSpace(action.Value) != "" {
			return nil, false
		}
		rawURL := strings.TrimSpace(action.URL)
		if rawURL == "" || !IsHTTPURL(rawURL) {
			return nil, false
		}
		label := strings.TrimSpace(action.Label)
		if label == "" {
			label = rawURL
		}
		parts = append(parts, MessagePart{Type: MessagePartLink, Text: label, URL: rawURL})
	}
	return parts, true
}

func appendTextSection(base string, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

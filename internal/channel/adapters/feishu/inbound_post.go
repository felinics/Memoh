package feishu

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// getFeishuPostContentLines returns content lines from post message.
// Feishu event payload uses root-level content: {"title":"","content":[[...],[...]]}.
func getFeishuPostContentLines(contentMap map[string]any) []any {
	if lines, ok := contentMap["content"].([]any); ok {
		return lines
	}
	return nil
}

// extractFeishuPostAttachments extracts image/file attachments from post content (e.g. img elements).
func extractFeishuPostAttachments(contentMap map[string]any, messageID string) []channel.Attachment {
	var result []channel.Attachment
	linesRaw := getFeishuPostContentLines(contentMap)
	if linesRaw == nil {
		return result
	}
	for _, rawLine := range linesRaw {
		line, ok := rawLine.([]any)
		if !ok {
			continue
		}
		for _, rawPart := range line {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			tag := strings.ToLower(strings.TrimSpace(stringValue(part["tag"])))
			if tag == "img" {
				if key, ok := part["image_key"].(string); ok && strings.TrimSpace(key) != "" {
					mime := strings.TrimSpace(stringValue(part["mime_type"]))
					result = append(result, channel.NormalizeInboundChannelAttachment(channel.Attachment{
						Type:           channel.AttachmentImage,
						PlatformKey:    strings.TrimSpace(key),
						SourcePlatform: Type.String(),
						Mime:           mime,
						Metadata:       map[string]any{"message_id": messageID},
					}))
				}
			}
			if tag == "file" {
				if key, ok := part["file_key"].(string); ok && strings.TrimSpace(key) != "" {
					name := strings.TrimSpace(stringValue(part["file_name"]))
					mime := strings.TrimSpace(stringValue(part["mime_type"]))
					result = append(result, channel.NormalizeInboundChannelAttachment(channel.Attachment{
						Type:           channel.AttachmentFile,
						PlatformKey:    strings.TrimSpace(key),
						SourcePlatform: Type.String(),
						Name:           name,
						Mime:           mime,
						Metadata:       map[string]any{"message_id": messageID},
					}))
				}
			}
		}
	}
	return result
}

func extractFeishuPostText(contentMap map[string]any) string {
	linesRaw := getFeishuPostContentLines(contentMap)
	if linesRaw == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	for _, rawLine := range linesRaw {
		line, ok := rawLine.([]any)
		if !ok {
			continue
		}
		for _, rawPart := range line {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			tag := strings.ToLower(strings.TrimSpace(stringValue(part["tag"])))
			switch tag {
			case "text", "a":
				text := strings.TrimSpace(stringValue(part["text"]))
				if text != "" {
					parts = append(parts, text)
				}
			case "at":
				name := strings.TrimSpace(stringValue(part["text"]))
				if name == "" {
					name = strings.TrimSpace(stringValue(part["name"]))
				}
				if name == "" {
					name = strings.TrimSpace(stringValue(part["user_name"]))
				}
				if name == "" {
					parts = append(parts, "@")
					continue
				}
				if !strings.HasPrefix(name, "@") {
					name = "@" + name
				}
				parts = append(parts, name)
			default:
				text := strings.TrimSpace(stringValue(part["text"]))
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func stringValue(raw any) string {
	if raw == nil {
		return ""
	}
	value, ok := raw.(string)
	if ok {
		return value
	}
	return fmt.Sprint(raw)
}

func extractFeishuPostParts(contentMap map[string]any) []channel.MessagePart {
	linesRaw := getFeishuPostContentLines(contentMap)
	if linesRaw == nil {
		return nil
	}
	var parts []channel.MessagePart
	hasRich := false
	for li, rawLine := range linesRaw {
		line, ok := rawLine.([]any)
		if !ok {
			continue
		}
		if li > 0 && len(parts) > 0 {
			parts = append(parts, channel.MessagePart{Type: channel.MessagePartText, Text: "\n"})
			hasRich = true
		}
		for _, rawPart := range line {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			mp, rich, ok := feishuPostPartToMessagePart(part)
			if !ok {
				continue
			}
			if rich {
				hasRich = true
			}
			parts = append(parts, mp)
		}
	}
	if !hasRich {
		return nil
	}
	return parts
}

func feishuPostPartToMessagePart(part map[string]any) (channel.MessagePart, bool, bool) {
	tag := strings.ToLower(strings.TrimSpace(stringValue(part["tag"])))
	switch tag {
	case "text":
		text := stringValue(part["text"])
		if text == "" {
			return channel.MessagePart{}, false, false
		}
		styles := feishuPostStyles(part["style"])
		return channel.MessagePart{
			Type:   channel.MessagePartText,
			Text:   text,
			Styles: styles,
		}, len(styles) > 0, true
	case "a":
		text := stringValue(part["text"])
		href := strings.TrimSpace(stringValue(part["href"]))
		if text == "" && href == "" {
			return channel.MessagePart{}, false, false
		}
		if text == "" {
			text = href
		}
		return channel.MessagePart{
			Type: channel.MessagePartLink,
			Text: text,
			URL:  href,
		}, true, true
	case "at":
		uid := strings.TrimSpace(stringValue(part["user_id"]))
		display := strings.TrimSpace(stringValue(part["text"]))
		if display == "" {
			display = strings.TrimSpace(stringValue(part["name"]))
		}
		if display == "" {
			display = strings.TrimSpace(stringValue(part["user_name"]))
		}
		if display == "" {
			if uid == "" {
				return channel.MessagePart{}, false, false
			}
			display = "@" + uid
		} else if !strings.HasPrefix(display, "@") {
			display = "@" + display
		}
		return channel.MessagePart{
			Type:              channel.MessagePartMention,
			Text:              display,
			ChannelIdentityID: uid,
		}, true, true
	case "code_block":
		text := stringValue(part["text"])
		if text == "" {
			return channel.MessagePart{}, false, false
		}
		return channel.MessagePart{
			Type:     channel.MessagePartCodeBlock,
			Text:     text,
			Language: strings.TrimSpace(stringValue(part["language"])),
		}, true, true
	default:
		// Forward-compatible: any future tag carrying a `text` field should
		// still surface its body so users don't lose content when a styled
		// element promotes the post to rich (and adaptBody picks Parts over
		// Text). rich=false keeps this from falsely promoting a plain-only
		// post on its own.
		text := stringValue(part["text"])
		if text == "" {
			return channel.MessagePart{}, false, false
		}
		return channel.MessagePart{Type: channel.MessagePartText, Text: text}, false, true
	}
}

func feishuPostStyles(raw any) []channel.MessageTextStyle {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var styles []channel.MessageTextStyle
	for _, item := range arr {
		switch strings.ToLower(strings.TrimSpace(stringValue(item))) {
		case "bold":
			styles = append(styles, channel.MessageStyleBold)
		case "italic":
			styles = append(styles, channel.MessageStyleItalic)
		case "linethrough", "strike", "strikethrough":
			styles = append(styles, channel.MessageStyleStrikethrough)
		}
	}
	return styles
}

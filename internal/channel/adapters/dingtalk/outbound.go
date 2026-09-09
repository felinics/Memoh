package dingtalk

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/channel"
)

// buildAPIPayload converts a channel.Message and its prepared attachments to a DingTalk
// OpenAPI msgKey + msgParam pair. Markdown messages map to sampleMarkdown; all others fall
// back to sampleText. Attachments map to the corresponding DingTalk media message type.
//
// prepared is the adapter-facing attachment list; it is used to get the correct public URL
// or native ref rather than the local media-store access path stored in the logical message.
func buildAPIPayload(msg channel.Message, prepared []channel.PreparedAttachment) (msgKey, msgParam string, err error) {
	// Attachment-only message: use the first attachment to determine message type.
	if strings.TrimSpace(msg.PlainText()) == "" && len(prepared) > 0 {
		return buildAttachmentPayload(prepared[0])
	}

	text := strings.TrimSpace(msg.PlainText())
	if text == "" {
		return "", "", errors.New("dingtalk: outbound message text is empty")
	}

	if msg.Format == channel.MessageFormatMarkdown {
		return buildMarkdownAPIPayload(text)
	}
	return buildTextAPIPayload(text)
}

func buildTextAPIPayload(text string) (string, string, error) {
	param, _ := json.Marshal(map[string]string{"content": text})
	return "sampleText", string(param), nil
}

func buildMarkdownAPIPayload(text string) (string, string, error) {
	param, _ := json.Marshal(map[string]string{
		"title": extractMarkdownTitle(text),
		"text":  text,
	})
	return "sampleMarkdown", string(param), nil
}

// buildAttachmentPayload converts a PreparedAttachment to a DingTalk API message.
//
// Images/GIF: photoURL accepts either a public HTTP URL or a DingTalk mediaId
// (returned by the media upload API). resolveUploadAttachments pre-uploads local
// assets and converts Upload kind to NativeRef before this function is called,
// so here NativeRef carries a valid mediaId for all attachment types.
func buildAttachmentPayload(att channel.PreparedAttachment) (string, string, error) {
	logical := att.Logical

	switch logical.Type {
	case channel.AttachmentImage, channel.AttachmentGIF:
		var photoURL string
		switch att.Kind {
		case channel.PreparedAttachmentPublicURL:
			photoURL = att.PublicURL
		case channel.PreparedAttachmentNativeRef:
			// mediaId returned by DingTalk upload API is accepted as photoURL value.
			photoURL = att.NativeRef
		default:
			// Upload kind should have been resolved to NativeRef by resolveUploadAttachments.
			// Fallback: use logical URL only if it looks like a public HTTP URL.
			if u := strings.TrimSpace(logical.URL); strings.HasPrefix(strings.ToLower(u), "http") {
				photoURL = u
			}
		}
		if photoURL == "" {
			return "", "", errors.New("dingtalk: image attachment requires a publicly accessible URL or uploaded mediaId")
		}
		param, _ := json.Marshal(map[string]string{"photoURL": photoURL})
		return "sampleImageMsg", string(param), nil

	case channel.AttachmentFile:
		mediaID := strings.TrimSpace(att.NativeRef)
		if mediaID == "" {
			mediaID = strings.TrimSpace(logical.PlatformKey)
		}
		fileType := resolveFileType(logical)
		param, _ := json.Marshal(map[string]string{
			"mediaId":  mediaID,
			"fileName": strings.TrimSpace(logical.Name),
			"fileType": fileType,
		})
		return "sampleFile", string(param), nil

	case channel.AttachmentAudio, channel.AttachmentVoice:
		mediaID := strings.TrimSpace(att.NativeRef)
		if mediaID == "" {
			mediaID = strings.TrimSpace(logical.PlatformKey)
		}
		param, _ := json.Marshal(map[string]string{
			"mediaId":  mediaID,
			"duration": "0",
		})
		return "sampleAudio", string(param), nil

	case channel.AttachmentVideo:
		mediaID := strings.TrimSpace(att.NativeRef)
		if mediaID == "" {
			mediaID = strings.TrimSpace(logical.PlatformKey)
		}
		param, _ := json.Marshal(map[string]string{
			"mediaId":   mediaID,
			"videoType": "mp4",
		})
		return "sampleVideo", string(param), nil

	default:
		return "", "", errors.New("dingtalk: unsupported attachment type for outbound")
	}
}

// buildWebhookBody converts a channel.Message to a DingTalk session webhook payload.
// session webhook uses msgtype/text style instead of msgKey/msgParam.
func buildWebhookBody(msg channel.Message) (map[string]any, error) {
	if strings.TrimSpace(msg.PlainText()) == "" && len(msg.Attachments) > 0 {
		// Webhooks only support text and markdown; fall back to text describing the attachment.
		return map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": "[attachment]",
			},
		}, nil
	}

	text := strings.TrimSpace(msg.PlainText())
	if text == "" {
		return nil, errors.New("dingtalk: webhook message text is empty")
	}

	if msg.Format == channel.MessageFormatMarkdown {
		return map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": extractMarkdownTitle(text),
				"text":  text,
			},
		}, nil
	}
	return map[string]any{
		"msgtype": "text",
		"text": map[string]string{
			"content": text,
		},
	}, nil
}

// extractMarkdownTitle tries to extract the first heading from markdown text.
// Falls back to "消息" if no heading is present.
func extractMarkdownTitle(text string) string {
	for _, line := range strings.SplitN(text, "\n", 5) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title := strings.TrimLeft(trimmed, "# ")
			if t := strings.TrimSpace(title); t != "" {
				if len([]rune(t)) > 20 {
					r := []rune(t)
					return string(r[:20])
				}
				return t
			}
		}
	}
	return "消息"
}

// resolveFileType returns a DingTalk-compatible file type string for file attachments.
func resolveFileType(att channel.Attachment) string {
	if ext := fileExtFromName(att.Name); ext != "" {
		return ext
	}
	switch att.Type {
	case channel.AttachmentImage:
		return "jpg"
	case channel.AttachmentVideo:
		return "mp4"
	case channel.AttachmentAudio, channel.AttachmentVoice:
		return "mp3"
	default:
		return "doc"
	}
}

// fileExtFromName extracts a lowercase extension (without the dot) from a filename.
func fileExtFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	idx := strings.LastIndex(name, ".")
	if idx < 0 || idx == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[idx+1:])
}

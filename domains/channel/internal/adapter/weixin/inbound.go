// Derived from @tencent-weixin/openclaw-weixin (MIT License, Copyright (c) 2026 Tencent Inc.)
// See LICENSE in this directory for the full license text.

package weixin

import (
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/memohai/memoh/domains/channel/gateway"
)

// buildInboundMessage maps a WeixinMessage to a Memoh InboundMessage.
func buildInboundMessage(msg WeixinMessage) (gateway.InboundMessage, bool) {
	text, attachments := extractContent(msg)
	replyRef := extractReplyRef(msg)
	if strings.TrimSpace(text) == "" && len(attachments) == 0 && replyRef == nil {
		return gateway.InboundMessage{}, false
	}

	fromUserID := strings.TrimSpace(msg.FromUserID)
	if fromUserID == "" {
		return gateway.InboundMessage{}, false
	}

	msgID := strconv.FormatInt(msg.MessageID, 10)
	if msg.Seq > 0 {
		msgID = strconv.FormatInt(msg.MessageID, 10) + ":" + strconv.Itoa(msg.Seq)
	}

	meta := map[string]any{
		"session_id": strings.TrimSpace(msg.SessionID),
		"seq":        msg.Seq,
	}
	if msg.ContextToken != "" {
		meta["context_token"] = msg.ContextToken
	}

	var receivedAt time.Time
	if msg.CreateTimeMs > 0 {
		receivedAt = time.UnixMilli(msg.CreateTimeMs)
	} else {
		receivedAt = time.Now().UTC()
	}

	return gateway.InboundMessage{
		Channel: Type,
		Message: gateway.Message{
			ID:          msgID,
			Format:      gateway.MessageFormatPlain,
			Text:        text,
			Attachments: attachments,
			Reply:       replyRef,
			Metadata:    meta,
		},
		ReplyTarget: fromUserID,
		Sender: gateway.Identity{
			SubjectID: fromUserID,
			Attributes: map[string]string{
				"user_id": fromUserID,
			},
		},
		Conversation: gateway.Conversation{
			ID:   fromUserID,
			Type: gateway.ConversationTypePrivate,
		},
		ReceivedAt: receivedAt,
		Source:     "weixin",
		Metadata:   meta,
	}, true
}

// extractContent extracts text and attachments from the message item list.
func extractContent(msg WeixinMessage) (string, []gateway.Attachment) {
	if len(msg.ItemList) == 0 {
		return "", nil
	}

	var textParts []string
	var attachments []gateway.Attachment

	for _, item := range msg.ItemList {
		switch item.Type {
		case ItemTypeText:
			t := extractTextFromItem(item)
			if t != "" {
				textParts = append(textParts, t)
			}
		case ItemTypeImage:
			if att, ok := buildImageAttachment(item); ok {
				attachments = append(attachments, att)
			}
		case ItemTypeVoice:
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" && !hasMediaRef(item) {
				textParts = append(textParts, item.VoiceItem.Text)
			} else if att, ok := buildVoiceAttachment(item); ok {
				attachments = append(attachments, att)
			}
		case ItemTypeFile:
			if att, ok := buildFileAttachment(item); ok {
				attachments = append(attachments, att)
			}
		case ItemTypeVideo:
			if att, ok := buildVideoAttachment(item); ok {
				attachments = append(attachments, att)
			}
		}
	}

	return strings.Join(textParts, "\n"), attachments
}

func extractTextFromItem(item MessageItem) string {
	if item.TextItem == nil || strings.TrimSpace(item.TextItem.Text) == "" {
		return ""
	}
	return item.TextItem.Text
}

func extractReplyRef(msg WeixinMessage) *gateway.ReplyRef {
	for _, item := range msg.ItemList {
		ref := item.RefMsg
		if ref == nil {
			continue
		}
		reply := &gateway.ReplyRef{
			Sender:  strings.TrimSpace(ref.Title),
			Preview: previewRefMessageItem(ref.MessageItem),
		}
		if ref.MessageItem != nil {
			reply.MessageID = strings.TrimSpace(ref.MessageItem.MsgID)
		}
		if reply.MessageID != "" || reply.Sender != "" || reply.Preview != "" {
			return reply
		}
	}
	return nil
}

func previewRefMessageItem(item *MessageItem) string {
	if item == nil {
		return ""
	}
	switch item.Type {
	case ItemTypeText:
		if item.TextItem != nil {
			return trimPreview(item.TextItem.Text)
		}
	case ItemTypeVoice:
		if item.VoiceItem != nil {
			return trimPreview(item.VoiceItem.Text)
		}
	}
	return ""
}

func trimPreview(value string) string {
	preview := strings.TrimSpace(value)
	if len([]rune(preview)) > 200 {
		return string([]rune(preview)[:200]) + "..."
	}
	return preview
}

func hasMediaRef(item MessageItem) bool {
	return item.VoiceItem != nil && item.VoiceItem.Media != nil &&
		strings.TrimSpace(item.VoiceItem.Media.EncryptQueryParam) != ""
}

func buildImageAttachment(item MessageItem) (gateway.Attachment, bool) {
	img := item.ImageItem
	if img == nil || img.Media == nil || strings.TrimSpace(img.Media.EncryptQueryParam) == "" {
		return gateway.Attachment{}, false
	}
	aesKey := resolveImageAESKey(img)
	return gateway.Attachment{
		Type:           gateway.AttachmentImage,
		PlatformKey:    img.Media.EncryptQueryParam,
		SourcePlatform: Type.String(),
		Metadata: map[string]any{
			"encrypt_query_param": img.Media.EncryptQueryParam,
			"aes_key":             aesKey,
		},
	}, true
}

// resolveImageAESKey picks the best AES key for image decryption.
// Prefers the hex-encoded aeskey field, falling back to media.aes_key.
func resolveImageAESKey(img *ImageItem) string {
	if strings.TrimSpace(img.AESKey) != "" {
		keyBytes, err := hex.DecodeString(img.AESKey)
		if err == nil {
			return base64.StdEncoding.EncodeToString(keyBytes)
		}
	}
	if img.Media != nil {
		return strings.TrimSpace(img.Media.AESKey)
	}
	return ""
}

func buildVoiceAttachment(item MessageItem) (gateway.Attachment, bool) {
	v := item.VoiceItem
	if v == nil || v.Media == nil || strings.TrimSpace(v.Media.EncryptQueryParam) == "" || strings.TrimSpace(v.Media.AESKey) == "" {
		return gateway.Attachment{}, false
	}
	return gateway.Attachment{
		Type:           gateway.AttachmentVoice,
		PlatformKey:    v.Media.EncryptQueryParam,
		SourcePlatform: Type.String(),
		DurationMs:     int64(v.Playtime),
		Metadata: map[string]any{
			"encrypt_query_param": v.Media.EncryptQueryParam,
			"aes_key":             v.Media.AESKey,
			"encode_type":         v.EncodeType,
		},
	}, true
}

func buildFileAttachment(item MessageItem) (gateway.Attachment, bool) {
	f := item.FileItem
	if f == nil || f.Media == nil || strings.TrimSpace(f.Media.EncryptQueryParam) == "" || strings.TrimSpace(f.Media.AESKey) == "" {
		return gateway.Attachment{}, false
	}
	return gateway.Attachment{
		Type:           gateway.AttachmentFile,
		PlatformKey:    f.Media.EncryptQueryParam,
		SourcePlatform: Type.String(),
		Name:           strings.TrimSpace(f.FileName),
		Metadata: map[string]any{
			"encrypt_query_param": f.Media.EncryptQueryParam,
			"aes_key":             f.Media.AESKey,
		},
	}, true
}

func buildVideoAttachment(item MessageItem) (gateway.Attachment, bool) {
	v := item.VideoItem
	if v == nil || v.Media == nil || strings.TrimSpace(v.Media.EncryptQueryParam) == "" || strings.TrimSpace(v.Media.AESKey) == "" {
		return gateway.Attachment{}, false
	}
	return gateway.Attachment{
		Type:           gateway.AttachmentVideo,
		PlatformKey:    v.Media.EncryptQueryParam,
		SourcePlatform: Type.String(),
		Metadata: map[string]any{
			"encrypt_query_param": v.Media.EncryptQueryParam,
			"aes_key":             v.Media.AESKey,
		},
	}, true
}

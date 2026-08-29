package feishu

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

const feishuQuotedTextMaxLength = 200

// enrichQuotedMessage fetches the parent message via API and fills structured
// reply context so the AI and UI can see what is being replied to. It also sets
// the "is_reply_to_bot" metadata flag.
func (a *FeishuAdapter) enrichQuotedMessage(ctx context.Context, cfg channel.ChannelConfig, msg *channel.InboundMessage, botOpenID string) {
	if msg == nil || msg.Message.Reply == nil {
		return
	}
	parentID := strings.TrimSpace(msg.Message.Reply.MessageID)
	if parentID == "" {
		return
	}

	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client := lark.NewClient(feishuCfg.AppID, feishuCfg.AppSecret, lark.WithOpenBaseUrl(feishuCfg.openBaseURL()))
	resp, err := client.Im.Message.Get(lookupCtx, larkim.NewGetMessageReqBuilder().MessageId(parentID).Build())
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("feishu quoted message fetch failed",
				slog.String("parent_id", parentID),
				slog.Any("error", err),
			)
		}
		return
	}
	if resp == nil || !resp.Success() || resp.Data == nil || len(resp.Data.Items) == 0 {
		if a.logger != nil {
			code, respMsg := 0, ""
			if resp != nil {
				code = resp.Code
				respMsg = resp.Msg
			}
			a.logger.Debug("feishu quoted message fetch empty",
				slog.String("parent_id", parentID),
				slog.Int("code", code),
				slog.String("response_msg", respMsg),
			)
		}
		return
	}

	parent := resp.Data.Items[0]

	// Determine if the parent message is from the bot itself.
	isReplyToBot := false
	senderName := ""
	if parent.Sender != nil {
		senderType := ptrStr(parent.Sender.SenderType)
		senderID := ptrStr(parent.Sender.Id)
		if senderType == "app" {
			// When botOpenID is known, match precisely; otherwise any app sender counts.
			isReplyToBot = strings.TrimSpace(botOpenID) == "" || senderID == strings.TrimSpace(botOpenID)
		}
	}
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata["is_reply_to_bot"] = isReplyToBot

	// Fill structured reply context instead of prepending [Reply to ...] text.
	preview := extractFeishuMessageText(parent)
	if preview == "" {
		msgType := ptrStr(parent.MsgType)
		if msgType != "" && msgType != larkim.MsgTypeText {
			preview = "[" + msgType + "]"
		}
	}
	if len([]rune(preview)) > feishuQuotedTextMaxLength {
		preview = string([]rune(preview)[:feishuQuotedTextMaxLength]) + "..."
	}
	msg.Message.Reply.Sender = senderName
	msg.Message.Reply.Preview = preview
	if attachments, known := feishuQuotedAttachments(parent); known {
		msg.Message.Reply.Attachments = attachments
		msg.Message.Reply.AttachmentsKnown = true
	}
}

// extractFeishuMessageText extracts plain text from a Feishu message object
// returned by the Get Message API.
func extractFeishuMessageText(msg *larkim.Message) string {
	if msg == nil || msg.Body == nil || msg.Body.Content == nil {
		return ""
	}
	content := strings.TrimSpace(*msg.Body.Content)
	if content == "" {
		return ""
	}

	var contentMap map[string]any
	if err := json.Unmarshal([]byte(content), &contentMap); err != nil {
		return ""
	}

	msgType := ptrStr(msg.MsgType)
	switch msgType {
	case larkim.MsgTypeText:
		if txt, ok := contentMap["text"].(string); ok {
			return strings.TrimSpace(txt)
		}
	case larkim.MsgTypePost:
		return extractFeishuPostText(contentMap)
	}

	// Fallback: try "text" key for unknown types.
	if txt, ok := contentMap["text"].(string); ok {
		return strings.TrimSpace(txt)
	}
	return ""
}

func feishuQuotedAttachments(msg *larkim.Message) ([]channel.Attachment, bool) {
	if msg == nil || msg.Body == nil || msg.Body.Content == nil {
		return nil, false
	}
	content := strings.TrimSpace(*msg.Body.Content)
	if content == "" {
		return nil, false
	}
	var contentMap map[string]any
	if err := json.Unmarshal([]byte(content), &contentMap); err != nil {
		return nil, false
	}

	messageID := ptrStr(msg.MessageId)
	switch ptrStr(msg.MsgType) {
	case larkim.MsgTypeText:
		return nil, true
	case larkim.MsgTypePost:
		return extractFeishuPostAttachments(contentMap, messageID), true
	case larkim.MsgTypeImage:
		key, _ := contentMap["image_key"].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, false
		}
		return []channel.Attachment{channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           channel.AttachmentImage,
			PlatformKey:    key,
			SourcePlatform: Type.String(),
			Metadata:       map[string]any{"message_id": messageID},
		})}, true
	case larkim.MsgTypeFile, larkim.MsgTypeAudio, larkim.MsgTypeMedia:
		key, _ := contentMap["file_key"].(string)
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, false
		}
		name, _ := contentMap["file_name"].(string)
		mime, _ := contentMap["mime_type"].(string)
		attType := channel.AttachmentFile
		switch ptrStr(msg.MsgType) {
		case larkim.MsgTypeAudio:
			attType = channel.AttachmentAudio
		case larkim.MsgTypeMedia:
			attType = channel.AttachmentVideo
		}
		return []channel.Attachment{channel.NormalizeInboundChannelAttachment(channel.Attachment{
			Type:           attType,
			PlatformKey:    key,
			SourcePlatform: Type.String(),
			Name:           strings.TrimSpace(name),
			Mime:           strings.TrimSpace(mime),
			Metadata:       map[string]any{"message_id": messageID},
		})}, true
	default:
		return nil, false
	}
}

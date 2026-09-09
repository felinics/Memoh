package feishu

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

// extractFeishuInbound converts a Feishu P2MessageReceiveV1 event into a channel.InboundMessage.
// botOpenID is the bot's own open_id used to filter mentions; if empty, any mention is treated as bot mention.
func extractFeishuInbound(event *larkim.P2MessageReceiveV1, botOpenID string, loggers ...*slog.Logger) channel.InboundMessage {
	var log *slog.Logger
	if len(loggers) > 0 {
		log = loggers[0]
	}
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return channel.InboundMessage{Channel: Type}
	}
	message := event.Event.Message

	var msg channel.Message
	if message.MessageId != nil {
		msg.ID = *message.MessageId
	}

	var contentMap map[string]any
	if message.Content != nil {
		if err := json.Unmarshal([]byte(*message.Content), &contentMap); err != nil {
			if log != nil {
				log.Warn("feishu inbound: unmarshal content failed", slog.Any("error", err))
			}
		}
	}
	mentions := normalizeFeishuMentions(message.Mentions)
	isMentioned := isFeishuBotMentioned(contentMap, mentions, botOpenID)

	if message.MessageType != nil {
		switch *message.MessageType {
		case larkim.MsgTypeText:
			if txt, ok := contentMap["text"].(string); ok {
				msg.Text = rewriteFeishuMentionKeys(txt, mentions)
			}
		case larkim.MsgTypePost:
			postText := extractFeishuPostText(contentMap)
			if postText != "" {
				msg.Text = postText
			}
			if postParts := extractFeishuPostParts(contentMap); len(postParts) > 0 {
				msg.Parts = postParts
				msg.Format = channel.MessageFormatRich
			}
			postAtts := extractFeishuPostAttachments(contentMap, msg.ID)
			msg.Attachments = append(msg.Attachments, postAtts...)
			if len(postAtts) > 0 || postText != "" {
				if log != nil {
					log.Debug("feishu post extracted",
						slog.String("message_id", msg.ID),
						slog.Int("text_len", len(postText)),
						slog.Int("attachments", len(postAtts)),
					)
				}
			}
		case larkim.MsgTypeImage:
			if key, ok := contentMap["image_key"].(string); ok {
				msg.Attachments = append(msg.Attachments, channel.NormalizeInboundChannelAttachment(channel.Attachment{
					Type:           channel.AttachmentImage,
					PlatformKey:    key,
					SourcePlatform: Type.String(),
					Metadata:       map[string]any{"message_id": msg.ID},
				}))
			}
		case larkim.MsgTypeFile, larkim.MsgTypeAudio, larkim.MsgTypeMedia:
			if key, ok := contentMap["file_key"].(string); ok {
				name, _ := contentMap["file_name"].(string)
				mime, _ := contentMap["mime_type"].(string)
				attType := channel.AttachmentFile
				switch *message.MessageType {
				case larkim.MsgTypeAudio:
					attType = channel.AttachmentAudio
				case larkim.MsgTypeMedia:
					attType = channel.AttachmentVideo
				}
				msg.Attachments = append(msg.Attachments, channel.NormalizeInboundChannelAttachment(channel.Attachment{
					Type:           attType,
					PlatformKey:    key,
					SourcePlatform: Type.String(),
					Name:           name,
					Mime:           mime,
					Metadata:       map[string]any{"message_id": msg.ID},
				}))
			}
		}
	}

	if message.ParentId != nil && *message.ParentId != "" {
		msg.Reply = &channel.ReplyRef{
			MessageID: *message.ParentId,
		}
	}

	senderID, senderOpenID := "", ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		if event.Event.Sender.SenderId.UserId != nil {
			senderID = strings.TrimSpace(*event.Event.Sender.SenderId.UserId)
		}
		if event.Event.Sender.SenderId.OpenId != nil {
			senderOpenID = strings.TrimSpace(*event.Event.Sender.SenderId.OpenId)
		}
	}
	chatID := ""
	chatTypeRaw := ""
	chatType := channel.ConversationTypePrivate
	if message.ChatId != nil {
		chatID = strings.TrimSpace(*message.ChatId)
	}
	if msg.Reply != nil && chatID != "" {
		msg.Reply.Target = chatID
	}
	if message.ChatType != nil {
		chatTypeRaw = strings.TrimSpace(*message.ChatType)
		chatType = normalizeFeishuConversationType(chatTypeRaw)
	}
	replyTo := senderOpenID
	if replyTo == "" {
		replyTo = senderID
	}
	if chatID != "" && chatType != channel.ConversationTypePrivate {
		replyTo = "chat_id:" + chatID
	}
	attrs := map[string]string{}
	if senderID != "" {
		attrs["user_id"] = senderID
	}
	if senderOpenID != "" {
		attrs["open_id"] = senderOpenID
	}
	subjectID := senderOpenID
	if subjectID == "" {
		subjectID = senderID
	}

	return channel.InboundMessage{
		Channel:     Type,
		Message:     msg,
		ReplyTarget: replyTo,
		Sender: channel.Identity{
			SubjectID:  subjectID,
			Attributes: attrs,
		},
		Conversation: channel.Conversation{
			ID:   chatID,
			Type: chatType,
		},
		ReceivedAt: time.Now().UTC(),
		Source:     "feishu",
		Metadata: map[string]any{
			"is_mentioned":      isMentioned,
			"bot_alias":         feishuBotMentionAlias(mentions, botOpenID),
			"raw_chat_type":     chatTypeRaw,
			"mentions":          feishuMentionsMetadata(mentions),
			"mentioned_targets": feishuMentionTargets(mentions),
		},
	}
}

func normalizeFeishuConversationType(chatType string) string {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "p2p":
		return channel.ConversationTypePrivate
	case "group":
		return channel.ConversationTypeGroup
	default:
		return channel.ConversationTypeGroup
	}
}

// resolveFeishuReceiveID parses target (open_id:/user_id:/chat_id: prefix) and returns receiveID and receiveType.
func resolveFeishuReceiveID(raw string) (string, string, error) {
	if raw == "" {
		return "", "", errors.New("feishu target is required")
	}
	if strings.HasPrefix(raw, "open_id:") {
		return strings.TrimPrefix(raw, "open_id:"), larkim.ReceiveIdTypeOpenId, nil
	}
	if strings.HasPrefix(raw, "user_id:") {
		return strings.TrimPrefix(raw, "user_id:"), larkim.ReceiveIdTypeUserId, nil
	}
	if strings.HasPrefix(raw, "chat_id:") {
		return strings.TrimPrefix(raw, "chat_id:"), larkim.ReceiveIdTypeChatId, nil
	}
	return raw, larkim.ReceiveIdTypeOpenId, nil
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

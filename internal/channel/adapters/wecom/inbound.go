package wecom

import (
	"context"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

func (a *WeComAdapter) handleFrame(ctx context.Context, cfg channel.ChannelConfig, frame WSFrame, handler channel.InboundHandler) error {
	switch frame.Cmd {
	case WSCmdMsgCallback:
		if handler == nil {
			return nil
		}
		var body MessageCallbackBody
		if err := frame.DecodeBody(&body); err != nil {
			return err
		}
		msg, ok := buildInboundMessage(body, frame.Headers.ReqID)
		if !ok {
			return nil
		}
		a.rememberCallback(cfg.ID, body.MsgID, frame.Headers.ReqID, body.ResponseURL, body.ChatID, body.From.UserID)
		return handler(ctx, cfg, msg)
	case WSCmdEventCallback:
		if handler == nil {
			return nil
		}
		var body EventCallbackBody
		if err := frame.DecodeBody(&body); err != nil {
			return err
		}
		msg, ok := buildInboundEventMessage(body, frame.Headers.ReqID)
		if !ok {
			return nil
		}
		a.rememberCallback(cfg.ID, body.MsgID, frame.Headers.ReqID, body.ResponseURL, body.ChatID, body.From.UserID)
		return handler(ctx, cfg, msg)
	default:
		return nil
	}
}

func buildInboundMessage(body MessageCallbackBody, reqID string) (channel.InboundMessage, bool) {
	text, attachments := extractBodyContent(body)
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return channel.InboundMessage{}, false
	}
	target := resolveDeliveryTarget(body.ChatID, body.From.UserID)
	if target == "" {
		return channel.InboundMessage{}, false
	}
	convType := normalizeConversationType(body.ChatType)
	convID := strings.TrimSpace(body.ChatID)
	if convID == "" {
		convID = strings.TrimSpace(body.From.UserID)
	}
	msg := channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          strings.TrimSpace(body.MsgID),
			Format:      channel.MessageFormatPlain,
			Text:        strings.TrimSpace(text),
			Attachments: attachments,
			Reply:       messageReplyRef(body.Quote, target),
			Metadata: map[string]any{
				"response_url": strings.TrimSpace(body.ResponseURL),
			},
		},
		ReplyTarget: target,
		Sender: channel.Identity{
			SubjectID: strings.TrimSpace(body.From.UserID),
			Attributes: map[string]string{
				"user_id": strings.TrimSpace(body.From.UserID),
				"chat_id": strings.TrimSpace(body.ChatID),
			},
		},
		Conversation: channel.Conversation{
			ID:   convID,
			Type: convType,
		},
		ReceivedAt: parseCreateTime(body.CreateTime),
		Source:     "wecom",
		Metadata: map[string]any{
			"req_id":       strings.TrimSpace(reqID),
			"chat_id":      strings.TrimSpace(body.ChatID),
			"chat_type":    strings.TrimSpace(body.ChatType),
			"response_url": strings.TrimSpace(body.ResponseURL),
			// WeCom smart-robot webhooks only fire on an @-mention (group) or a
			// direct chat, so every delivered user message is bot-directed. Mark
			// it so isDirectedAtBot lets group replies drive plain-text ask_user;
			// event callbacks (buildInboundEventMessage) intentionally omit this.
			"is_mentioned": true,
		},
	}
	return msg, true
}

func messageReplyRef(quote *MessageQuote, target string) *channel.ReplyRef {
	if quote == nil {
		return nil
	}
	messageID := strings.TrimSpace(quote.MsgID)
	if messageID == "" {
		return nil
	}
	return &channel.ReplyRef{
		Target:    strings.TrimSpace(target),
		MessageID: messageID,
	}
}

func buildInboundEventMessage(body EventCallbackBody, reqID string) (channel.InboundMessage, bool) {
	eventType := normalizeEventType(body.Event.EventType, body.Event.EventType2)
	if eventType == "" {
		return channel.InboundMessage{}, false
	}
	target := resolveDeliveryTarget(body.ChatID, body.From.UserID)
	if target == "" {
		return channel.InboundMessage{}, false
	}
	convType := normalizeConversationType(body.ChatType)
	convID := strings.TrimSpace(body.ChatID)
	if convID == "" {
		convID = strings.TrimSpace(body.From.UserID)
	}
	return channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:     strings.TrimSpace(body.MsgID),
			Format: channel.MessageFormatPlain,
			Text:   eventType,
			Metadata: map[string]any{
				"event_type":   eventType,
				"task_id":      strings.TrimSpace(body.Event.TaskID),
				"event_key":    strings.TrimSpace(body.Event.EventKey),
				"event_code":   strings.TrimSpace(body.Event.Code),
				"event_reason": strings.TrimSpace(body.Event.Reason),
				"task_status":  strings.TrimSpace(body.Task.TaskStatus),
				"response_url": strings.TrimSpace(body.ResponseURL),
			},
		},
		ReplyTarget: target,
		Sender: channel.Identity{
			SubjectID: strings.TrimSpace(body.From.UserID),
			Attributes: map[string]string{
				"user_id": strings.TrimSpace(body.From.UserID),
				"chat_id": strings.TrimSpace(body.ChatID),
			},
		},
		Conversation: channel.Conversation{
			ID:   convID,
			Type: convType,
		},
		ReceivedAt: parseCreateTime(body.CreateTime),
		Source:     "wecom",
		Metadata: map[string]any{
			"is_event":     true,
			"event_type":   eventType,
			"event_key":    strings.TrimSpace(body.Event.EventKey),
			"event_code":   strings.TrimSpace(body.Event.Code),
			"event_reason": strings.TrimSpace(body.Event.Reason),
			"req_id":       strings.TrimSpace(reqID),
			"response_url": strings.TrimSpace(body.ResponseURL),
		},
	}, true
}

func extractBodyContent(body MessageCallbackBody) (string, []channel.Attachment) {
	switch strings.ToLower(strings.TrimSpace(body.MsgType)) {
	case "text":
		if body.Text == nil {
			return "", nil
		}
		return strings.TrimSpace(body.Text.Content), nil
	case "markdown":
		if body.Markdown == nil {
			return "", nil
		}
		return strings.TrimSpace(body.Markdown.Content), nil
	case "voice":
		if body.Voice == nil {
			return "", nil
		}
		return strings.TrimSpace(body.Voice.Content), nil
	case "video":
		if body.Video == nil {
			return "", nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentVideo,
			URL:            strings.TrimSpace(body.Video.URL),
			SourcePlatform: Type.String(),
			Metadata: map[string]any{
				"aeskey": strings.TrimSpace(body.Video.AESKey),
			},
		}
		return "", []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}
	case "image":
		if body.Image == nil {
			return "", nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentImage,
			URL:            strings.TrimSpace(body.Image.URL),
			SourcePlatform: Type.String(),
			Metadata: map[string]any{
				"aeskey": strings.TrimSpace(body.Image.AESKey),
			},
		}
		return "", []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}
	case "file":
		if body.File == nil {
			return "", nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentFile,
			URL:            strings.TrimSpace(body.File.URL),
			SourcePlatform: Type.String(),
			Name:           strings.TrimSpace(body.File.FileName),
			Metadata: map[string]any{
				"aeskey": strings.TrimSpace(body.File.AESKey),
			},
		}
		return "", []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}
	case "mixed":
		var textParts []string
		attachments := make([]channel.Attachment, 0)
		for _, item := range body.Mixed {
			switch strings.ToLower(strings.TrimSpace(item.MsgType)) {
			case "text":
				if item.Text != nil && strings.TrimSpace(item.Text.Content) != "" {
					textParts = append(textParts, strings.TrimSpace(item.Text.Content))
				}
			case "markdown":
				if item.Markdown != nil && strings.TrimSpace(item.Markdown.Content) != "" {
					textParts = append(textParts, strings.TrimSpace(item.Markdown.Content))
				}
			case "image":
				if item.Image == nil {
					continue
				}
				att := channel.Attachment{
					Type:           channel.AttachmentImage,
					URL:            strings.TrimSpace(item.Image.URL),
					SourcePlatform: Type.String(),
					Metadata: map[string]any{
						"aeskey": strings.TrimSpace(item.Image.AESKey),
					},
				}
				attachments = append(attachments, channel.NormalizeInboundChannelAttachment(att))
			case "file":
				if item.File == nil {
					continue
				}
				att := channel.Attachment{
					Type:           channel.AttachmentFile,
					URL:            strings.TrimSpace(item.File.URL),
					SourcePlatform: Type.String(),
					Name:           strings.TrimSpace(item.File.FileName),
					Metadata: map[string]any{
						"aeskey": strings.TrimSpace(item.File.AESKey),
					},
				}
				attachments = append(attachments, channel.NormalizeInboundChannelAttachment(att))
			case "video":
				if item.Video == nil {
					continue
				}
				att := channel.Attachment{
					Type:           channel.AttachmentVideo,
					URL:            strings.TrimSpace(item.Video.URL),
					SourcePlatform: Type.String(),
					Metadata: map[string]any{
						"aeskey": strings.TrimSpace(item.Video.AESKey),
					},
				}
				attachments = append(attachments, channel.NormalizeInboundChannelAttachment(att))
			case "voice":
				if item.Voice != nil && strings.TrimSpace(item.Voice.Content) != "" {
					textParts = append(textParts, strings.TrimSpace(item.Voice.Content))
				}
			}
		}
		return strings.Join(textParts, "\n"), attachments
	default:
		return "", nil
	}
}

func resolveDeliveryTarget(chatID, userID string) string {
	if v := strings.TrimSpace(chatID); v != "" {
		return "chat_id:" + v
	}
	if v := strings.TrimSpace(userID); v != "" {
		return "user_id:" + v
	}
	return ""
}

func normalizeConversationType(chatType string) string {
	if strings.EqualFold(strings.TrimSpace(chatType), "group") {
		return channel.ConversationTypeGroup
	}
	return channel.ConversationTypePrivate
}

func parseCreateTime(ts int64) time.Time {
	if t := unixMilliseconds(ts); !t.IsZero() {
		return t
	}
	return time.Now().UTC()
}

func (a *WeComAdapter) rememberCallback(configID, msgID, reqID, responseURL, chatID, userID string) {
	if a == nil || a.cache == nil {
		return
	}
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return
	}
	if strings.TrimSpace(reqID) == "" {
		return
	}
	a.cache.Put(configID, msgID, callbackContext{
		ReqID:       strings.TrimSpace(reqID),
		ResponseURL: strings.TrimSpace(responseURL),
		ChatID:      strings.TrimSpace(chatID),
		UserID:      strings.TrimSpace(userID),
		CreatedAt:   time.Now().UTC(),
	})
}

func normalizeEventType(values ...string) string {
	candidates := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" {
			continue
		}
		v = strings.ReplaceAll(v, "-", "_")
		v = strings.ReplaceAll(v, " ", "_")
		candidates = append(candidates, v)
	}
	for _, v := range candidates {
		switch v {
		case "enter_chat", "template_card_event", "feedback_event":
			return v
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

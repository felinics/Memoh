package dingtalk

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/felinics/dingtalk-stream-sdk-go/chatbot"

	"github.com/felinics/memoh/internal/channel"
)

// sessionWebhookContext holds a cached session webhook for a received message.
type sessionWebhookContext struct {
	SessionWebhook string
	ExpiredTime    int64 // unix milliseconds; 0 means never set
	ConversationID string
	SenderID       string
	CreatedAt      time.Time
}

// isValid reports whether the session webhook is still within its validity window.
func (w sessionWebhookContext) isValid() bool {
	if strings.TrimSpace(w.SessionWebhook) == "" {
		return false
	}
	if w.ExpiredTime <= 0 {
		return false
	}
	return time.Now().UnixMilli() < w.ExpiredTime
}

// sessionWebhookCache stores recent sessionWebhook contexts by channel config
// and msgId. DingTalk message IDs are external-account scoped, so msgId alone
// cannot isolate multiple configured bots in the same process.
type sessionWebhookCache struct {
	mu    sync.RWMutex
	items map[string]sessionWebhookContext
	ttl   time.Duration
}

func newSessionWebhookCache(ttl time.Duration) *sessionWebhookCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &sessionWebhookCache{
		items: make(map[string]sessionWebhookContext),
		ttl:   ttl,
	}
}

func (c *sessionWebhookCache) put(configID, msgID string, ctx sessionWebhookContext) {
	key := sessionWebhookCacheKey(configID, msgID)
	if key == "" {
		return
	}
	if ctx.CreatedAt.IsZero() {
		ctx.CreatedAt = time.Now().UTC()
	}
	c.mu.Lock()
	c.items[key] = ctx
	c.gcLocked()
	c.mu.Unlock()
}

func (c *sessionWebhookCache) get(configID, msgID string) (sessionWebhookContext, bool) {
	key := sessionWebhookCacheKey(configID, msgID)
	if key == "" {
		return sessionWebhookContext{}, false
	}
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		return sessionWebhookContext{}, false
	}
	if time.Since(item.CreatedAt) > c.ttl {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return sessionWebhookContext{}, false
	}
	return item, true
}

func sessionWebhookCacheKey(configID, msgID string) string {
	configID = strings.TrimSpace(configID)
	msgID = strings.TrimSpace(msgID)
	if configID == "" || msgID == "" {
		return ""
	}
	return configID + "\x00" + msgID
}

func (c *sessionWebhookCache) gcLocked() {
	if len(c.items) < 512 {
		return
	}
	now := time.Now().UTC()
	for key, item := range c.items {
		if now.Sub(item.CreatedAt) > c.ttl {
			delete(c.items, key)
		}
	}
}

// richTextItem is an element within a DingTalk richText message payload.
type richTextItem struct {
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	DownloadCode string `json:"downloadCode,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

// pictureContent is the payload for msgtype="picture".
type pictureContent struct {
	DownloadCode string `json:"downloadCode"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

// fileContent is the payload for msgtype="file".
type fileContent struct {
	DownloadCode string `json:"downloadCode"`
	FileName     string `json:"fileName,omitempty"`
	FileSize     string `json:"fileSize,omitempty"`
	FileType     string `json:"fileType,omitempty"`
}

// audioContent is the payload for msgtype="audio".
type audioContent struct {
	DownloadCode string `json:"downloadCode,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	Recognition  string `json:"recognition,omitempty"`
}

// videoContent is the payload for msgtype="video".
type videoContent struct {
	VideoMediaId string `json:"videoMediaId"`
	Duration     int    `json:"duration,omitempty"`
	VideoType    string `json:"videoType,omitempty"`
}

// buildInboundMessage converts a DingTalk BotCallbackDataModel to a channel.InboundMessage.
// Returns false when the message should be silently ignored (e.g. empty content).
func buildInboundMessage(data *chatbot.BotCallbackDataModel) (channel.InboundMessage, bool) {
	text, format, attachments := extractContent(data)
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return channel.InboundMessage{}, false
	}

	convType := normalizeDingTalkConversationType(data.ConversationType)
	convID := strings.TrimSpace(data.ConversationId)
	if convID == "" {
		convID = strings.TrimSpace(data.SenderId)
	}

	// ReplyTarget: for private chat prefer senderStaffId (required by DingTalk OpenAPI),
	// falling back to senderId (works for session webhook but may be unionId in ISV apps).
	// For group chat use group:{conversationId}.
	replyTarget := buildReplyTarget(convType, data.ConversationId, data.SenderId, data.SenderStaffId)

	// Sanitize @ mentions from text body (DingTalk prepends "@botNick " in group messages).
	if strings.TrimSpace(text) != "" {
		text = strings.TrimSpace(text)
	}

	// Use staffId as SubjectID when available: it is the stable employee identifier
	// accepted by the DingTalk OpenAPI. senderId may be a unionId ($:LWCP_v1:$...)
	// which the batchSend API rejects with "staffId.notExisted".
	senderID := strings.TrimSpace(data.SenderId)
	staffID := strings.TrimSpace(data.SenderStaffId)
	subjectID := staffID
	if subjectID == "" {
		subjectID = senderID
	}

	return channel.InboundMessage{
		Channel: Type,
		Message: channel.Message{
			ID:          strings.TrimSpace(data.MsgId),
			Format:      format,
			Text:        text,
			Attachments: attachments,
		},
		ReplyTarget: replyTarget,
		Sender: channel.Identity{
			SubjectID:   subjectID,
			DisplayName: strings.TrimSpace(data.SenderNick),
			Attributes: map[string]string{
				"user_id":         senderID,
				"staff_id":        staffID,
				"corp_id":         strings.TrimSpace(data.SenderCorpId),
				"chatbot_user_id": strings.TrimSpace(data.ChatbotUserId),
			},
		},
		Conversation: channel.Conversation{
			ID:   convID,
			Type: convType,
			Name: strings.TrimSpace(data.ConversationTitle),
			Metadata: map[string]any{
				"open_conversation_id": strings.TrimSpace(data.ConversationId),
			},
		},
		ReceivedAt: parseCreateAt(data.CreateAt),
		Source:     "dingtalk",
		Metadata: map[string]any{
			"msg_id":              strings.TrimSpace(data.MsgId),
			"conversation_id":     strings.TrimSpace(data.ConversationId),
			"conversation_type":   strings.TrimSpace(data.ConversationType),
			"chatbot_corp_id":     strings.TrimSpace(data.ChatbotCorpId),
			"is_admin":            data.IsAdmin,
			"session_webhook":     strings.TrimSpace(data.SessionWebhook),
			"session_webhook_exp": data.SessionWebhookExpiredTime,
			// DingTalk only delivers a message to the bot when it is @-mentioned
			// (group) or a direct chat, so every inbound is bot-directed. Mark it
			// so isDirectedAtBot lets group replies drive plain-text ask_user;
			// without this, the group fallback silently drops answers.
			"is_mentioned": true,
		},
	}, true
}

// extractContent parses the msgtype-specific payload and returns text, format, and attachments.
func extractContent(data *chatbot.BotCallbackDataModel) (string, channel.MessageFormat, []channel.Attachment) {
	msgtype := strings.ToLower(strings.TrimSpace(data.Msgtype))
	switch msgtype {
	case "text":
		return strings.TrimSpace(data.Text.Content), channel.MessageFormatPlain, nil

	case "markdown":
		content := extractStringField(data.Content, "text")
		if content == "" {
			content = strings.TrimSpace(data.Text.Content)
		}
		return content, channel.MessageFormatMarkdown, nil

	case "picture", "image":
		raw, _ := json.Marshal(data.Content)
		var pic pictureContent
		if err := json.Unmarshal(raw, &pic); err != nil || strings.TrimSpace(pic.DownloadCode) == "" {
			return "", channel.MessageFormatPlain, nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentImage,
			PlatformKey:    strings.TrimSpace(pic.DownloadCode),
			SourcePlatform: Type.String(),
			Width:          pic.Width,
			Height:         pic.Height,
		}
		return "", channel.MessageFormatPlain, []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}

	case "file":
		raw, _ := json.Marshal(data.Content)
		var f fileContent
		if err := json.Unmarshal(raw, &f); err != nil || strings.TrimSpace(f.DownloadCode) == "" {
			return "", channel.MessageFormatPlain, nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentFile,
			PlatformKey:    strings.TrimSpace(f.DownloadCode),
			SourcePlatform: Type.String(),
			Name:           strings.TrimSpace(f.FileName),
		}
		return "", channel.MessageFormatPlain, []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}

	case "audio", "voice":
		raw, _ := json.Marshal(data.Content)
		var a audioContent
		_ = json.Unmarshal(raw, &a)
		// Prefer voice-recognition text; fall back to attachment if code present.
		if rec := strings.TrimSpace(a.Recognition); rec != "" {
			return rec, channel.MessageFormatPlain, nil
		}
		if code := strings.TrimSpace(a.DownloadCode); code != "" {
			att := channel.Attachment{
				Type:           channel.AttachmentVoice,
				PlatformKey:    code,
				SourcePlatform: Type.String(),
				DurationMs:     int64(a.Duration) * 1000,
			}
			return "", channel.MessageFormatPlain, []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}
		}
		return "", channel.MessageFormatPlain, nil

	case "video":
		raw, _ := json.Marshal(data.Content)
		var v videoContent
		if err := json.Unmarshal(raw, &v); err != nil || strings.TrimSpace(v.VideoMediaId) == "" {
			return "", channel.MessageFormatPlain, nil
		}
		att := channel.Attachment{
			Type:           channel.AttachmentVideo,
			PlatformKey:    strings.TrimSpace(v.VideoMediaId),
			SourcePlatform: Type.String(),
			DurationMs:     int64(v.Duration) * 1000,
		}
		return "", channel.MessageFormatPlain, []channel.Attachment{channel.NormalizeInboundChannelAttachment(att)}

	case "richtext":
		return extractRichText(data.Content)

	default:
		// Try to extract a text field from Content as a best-effort fallback.
		if text := extractStringField(data.Content, "content", "text"); text != "" {
			return text, channel.MessageFormatPlain, nil
		}
		return "", channel.MessageFormatPlain, nil
	}
}

// extractRichText parses a richText message content into text and attachments.
func extractRichText(raw any) (string, channel.MessageFormat, []channel.Attachment) {
	rawJSON, _ := json.Marshal(raw)
	var payload struct {
		RichText []richTextItem `json:"richText"`
	}
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return "", channel.MessageFormatPlain, nil
	}
	var textParts []string
	var attachments []channel.Attachment
	for _, item := range payload.RichText {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			if v := strings.TrimSpace(item.Text); v != "" {
				textParts = append(textParts, v)
			}
		case "picture", "image":
			if code := strings.TrimSpace(item.DownloadCode); code != "" {
				att := channel.Attachment{
					Type:           channel.AttachmentImage,
					PlatformKey:    code,
					SourcePlatform: Type.String(),
					Width:          item.Width,
					Height:         item.Height,
				}
				attachments = append(attachments, channel.NormalizeInboundChannelAttachment(att))
			}
		}
	}
	return strings.Join(textParts, "\n"), channel.MessageFormatPlain, attachments
}

// extractStringField attempts to read a string value from an interface{} map by trying keys in order.
func extractStringField(raw any, keys ...string) string {
	m, ok := raw.(map[string]any)
	if !ok {
		// Try JSON round-trip for non-map types.
		b, err := json.Marshal(raw)
		if err != nil {
			return ""
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return ""
		}
	}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// normalizeDingTalkConversationType maps DingTalk conversationType values to Memoh constants.
// "1" = single/private; "2" = group.
func normalizeDingTalkConversationType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "2", "group":
		return channel.ConversationTypeGroup
	default:
		return channel.ConversationTypePrivate
	}
}

// buildReplyTarget produces the canonical reply target for a DingTalk inbound message.
// For private chat, staffID is preferred over senderID because the DingTalk OpenAPI's
// batchSend endpoint requires the staffId, while senderId may be a unionId in ISV apps.
func buildReplyTarget(convType, conversationID, senderID, staffID string) string {
	if channel.IsPrivateConversationType(convType) {
		if v := strings.TrimSpace(staffID); v != "" {
			return "user:" + v
		}
		if v := strings.TrimSpace(senderID); v != "" {
			return "user:" + v
		}
	}
	if v := strings.TrimSpace(conversationID); v != "" {
		return "group:" + v
	}
	return ""
}

// parseCreateAt converts a DingTalk createAt unix millisecond timestamp to time.Time.
func parseCreateAt(ms int64) time.Time {
	if ms <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(ms).UTC()
}

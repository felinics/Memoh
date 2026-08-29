package inbound

import (
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// AdaptInbound converts a channel.InboundMessage into a timeline.CanonicalEvent.
// The event type is determined by the "event_type" metadata key set by channel
// adapters: "edit" → EditEvent, "service" → ServiceEvent. All other messages
// (including the default) produce a MessageEvent.
func AdaptInbound(msg channel.InboundMessage, sessionID, channelIdentityID, displayName string) timeline.CanonicalEvent {
	eventType, _ := msg.Metadata["event_type"].(string)
	switch eventType {
	case "edit":
		return adaptEdit(msg, sessionID, channelIdentityID, displayName)
	case "service":
		return adaptService(msg, sessionID)
	default:
		return adaptMessage(msg, sessionID, channelIdentityID, displayName)
	}
}

func adaptMessage(msg channel.InboundMessage, sessionID, channelIdentityID, displayName string) timeline.MessageEvent {
	now := msg.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}

	var sender *timeline.CanonicalUser
	if channelIdentityID != "" || displayName != "" {
		sender = &timeline.CanonicalUser{
			ID:          channelIdentityID,
			DisplayName: displayName,
			Username:    strings.TrimSpace(msg.Sender.Attribute("username")),
			IsBot:       adaptMetadataBool(msg.Metadata, "is_bot"),
		}
	}

	content := adaptBody(msg.Message)
	attachments := adaptAttachments(msg.Message.Attachments)
	forwardInfo := adaptForward(msg.Message.Forward)

	var replyToMessageID, replyToSender, replyToPreview string
	if msg.Message.Reply != nil {
		replyToMessageID = strings.TrimSpace(msg.Message.Reply.MessageID)
		replyToSender = strings.TrimSpace(msg.Message.Reply.Sender)
		replyToPreview = strings.TrimSpace(msg.Message.Reply.Preview)
	}

	_, offset := now.Zone()
	utcOffsetMin := offset / 60

	convType := channel.NormalizeConversationType(msg.Conversation.Type)

	return timeline.MessageEvent{
		SessionID:        sessionID,
		MessageID:        strings.TrimSpace(msg.Message.ID),
		Sender:           sender,
		ReceivedAtMs:     now.UnixMilli(),
		TimestampSec:     now.Unix(),
		UTCOffsetMin:     utcOffsetMin,
		Content:          content,
		ReplyToMessageID: replyToMessageID,
		ReplyToSender:    replyToSender,
		ReplyToPreview:   replyToPreview,
		ForwardInfo:      forwardInfo,
		Attachments:      attachments,
		MentionsMe:       adaptMetadataBool(msg.Metadata, "is_mentioned"),
		RepliesToMe:      adaptMetadataBool(msg.Metadata, "is_reply_to_bot"),
		Conversation: timeline.ConversationMeta{
			Channel:          msg.Channel.String(),
			ConversationName: strings.TrimSpace(msg.Conversation.Name),
			ConversationType: convType,
			Target:           strings.TrimSpace(msg.ReplyTarget),
		},
	}
}

func adaptContent(text string) []timeline.ContentNode {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []timeline.ContentNode{{Type: "text", Text: text}}
}

func adaptBody(m channel.Message) []timeline.ContentNode {
	if nodes := adaptParts(m.Parts); len(nodes) > 0 {
		return nodes
	}
	return adaptContent(m.Text)
}

func adaptParts(parts []channel.MessagePart) []timeline.ContentNode {
	if len(parts) == 0 {
		return nil
	}
	nodes := make([]timeline.ContentNode, 0, len(parts))
	for _, p := range parts {
		node, ok := partToNode(p)
		if !ok {
			continue
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		return nil
	}
	return nodes
}

func partToNode(p channel.MessagePart) (timeline.ContentNode, bool) {
	switch p.Type {
	case channel.MessagePartCodeBlock:
		if strings.TrimSpace(p.Text) == "" {
			return timeline.ContentNode{}, false
		}
		return timeline.ContentNode{Type: "pre", Language: strings.TrimSpace(p.Language), Text: p.Text}, true
	case channel.MessagePartLink:
		display := p.Text
		if strings.TrimSpace(display) == "" {
			display = p.URL
		}
		if strings.TrimSpace(display) == "" {
			return timeline.ContentNode{}, false
		}
		return timeline.ContentNode{
			Type:     "link",
			URL:      strings.TrimSpace(p.URL),
			Children: []timeline.ContentNode{{Type: "text", Text: display}},
		}, true
	case channel.MessagePartMention:
		display := strings.TrimSpace(p.Text)
		uid := strings.TrimSpace(p.ChannelIdentityID)
		if display == "" {
			if uid == "" {
				return timeline.ContentNode{}, false
			}
			display = "@" + uid
		}
		return timeline.ContentNode{
			Type:     "mention",
			UserID:   uid,
			Children: []timeline.ContentNode{{Type: "text", Text: display}},
		}, true
	case channel.MessagePartEmoji:
		text := strings.TrimSpace(p.Emoji)
		if text == "" {
			text = strings.TrimSpace(p.Text)
		}
		if text == "" {
			return timeline.ContentNode{}, false
		}
		return timeline.ContentNode{Type: "text", Text: text}, true
	case channel.MessagePartText:
		return textPartToNode(p)
	default:
		return timeline.ContentNode{}, false
	}
}

func textPartToNode(p channel.MessagePart) (timeline.ContentNode, bool) {
	if p.Text == "" {
		return timeline.ContentNode{}, false
	}

	hasInlineCode := false
	wrappers := make([]channel.MessageTextStyle, 0, len(p.Styles))
	for _, s := range p.Styles {
		if s == channel.MessageStyleCode {
			hasInlineCode = true
			continue
		}
		wrappers = append(wrappers, s)
	}

	var node timeline.ContentNode
	if hasInlineCode {
		node = timeline.ContentNode{Type: "code", Text: p.Text}
	} else {
		node = timeline.ContentNode{Type: "text", Text: p.Text}
	}
	for i := len(wrappers) - 1; i >= 0; i-- {
		kind := styleToNodeType(wrappers[i])
		if kind == "" {
			continue
		}
		node = timeline.ContentNode{Type: kind, Children: []timeline.ContentNode{node}}
	}
	return node, true
}

func styleToNodeType(s channel.MessageTextStyle) string {
	switch s {
	case channel.MessageStyleBold:
		return "bold"
	case channel.MessageStyleItalic:
		return "italic"
	case channel.MessageStyleStrikethrough:
		return "strikethrough"
	default:
		return ""
	}
}

func adaptAttachments(atts []channel.Attachment) []timeline.Attachment {
	if len(atts) == 0 {
		return nil
	}
	result := make([]timeline.Attachment, 0, len(atts))
	for _, a := range atts {
		bundle := channel.BundleFromAttachment(a)
		att := timeline.Attachment{
			Type:        bundle.Type,
			MimeType:    strings.TrimSpace(bundle.Mime),
			FileName:    strings.TrimSpace(bundle.Name),
			ContentHash: strings.TrimSpace(bundle.ContentHash),
			Width:       bundle.Width,
			Height:      bundle.Height,
		}
		if bundle.DurationMs > 0 {
			att.Duration = int(bundle.DurationMs / 1000)
		}
		if ref := strings.TrimSpace(bundle.Path); ref != "" {
			att.FilePath = ref
		} else if ref := strings.TrimSpace(bundle.URL); ref != "" {
			att.FilePath = ref
		} else if ref := strings.TrimSpace(bundle.PlatformKey); ref != "" {
			att.FilePath = ref
		}
		result = append(result, att)
	}
	return result
}

func adaptForward(ref *channel.ForwardRef) *timeline.ForwardInfo {
	if ref == nil {
		return nil
	}
	forward := &timeline.ForwardInfo{
		MessageID:          strings.TrimSpace(ref.MessageID),
		FromUserID:         strings.TrimSpace(ref.FromUserID),
		FromConversationID: strings.TrimSpace(ref.FromConversationID),
		SenderName:         strings.TrimSpace(ref.Sender),
		Date:               ref.Date,
	}
	if forward.MessageID == "" && forward.FromUserID == "" && forward.FromConversationID == "" && forward.SenderName == "" && forward.Date == 0 {
		return nil
	}
	if forward.SenderName != "" {
		forward.Sender = &timeline.CanonicalUser{DisplayName: forward.SenderName}
	}
	return forward
}

func adaptEdit(msg channel.InboundMessage, sessionID, channelIdentityID, displayName string) timeline.EditEvent {
	now := msg.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}

	var sender *timeline.CanonicalUser
	if channelIdentityID != "" || displayName != "" {
		sender = &timeline.CanonicalUser{
			ID:          channelIdentityID,
			DisplayName: displayName,
			Username:    strings.TrimSpace(msg.Sender.Attribute("username")),
			IsBot:       adaptMetadataBool(msg.Metadata, "is_bot"),
		}
	}

	_, offset := now.Zone()
	return timeline.EditEvent{
		SessionID:    sessionID,
		MessageID:    strings.TrimSpace(msg.Message.ID),
		Sender:       sender,
		ReceivedAtMs: now.UnixMilli(),
		TimestampSec: now.Unix(),
		UTCOffsetMin: offset / 60,
		Content:      adaptBody(msg.Message),
		Attachments:  adaptAttachments(msg.Message.Attachments),
	}
}

func adaptService(msg channel.InboundMessage, sessionID string) timeline.ServiceEvent {
	now := msg.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}

	action, _ := msg.Metadata["service_action"].(string)
	var actor *timeline.CanonicalUser
	if msg.Sender.SubjectID != "" || msg.Sender.DisplayName != "" {
		actor = &timeline.CanonicalUser{
			ID:          strings.TrimSpace(msg.Sender.SubjectID),
			DisplayName: strings.TrimSpace(msg.Sender.DisplayName),
			Username:    strings.TrimSpace(msg.Sender.Attribute("username")),
		}
	}

	_, offset := now.Zone()
	event := timeline.ServiceEvent{
		SessionID:    sessionID,
		Action:       timeline.ServiceAction(action),
		Actor:        actor,
		ReceivedAtMs: now.UnixMilli(),
		TimestampSec: now.Unix(),
		UTCOffsetMin: offset / 60,
	}
	if title, ok := msg.Metadata["new_title"].(string); ok {
		event.NewTitle = title
	}
	if title, ok := msg.Metadata["old_title"].(string); ok {
		event.OldTitle = title
	}
	return event
}

func adaptMetadataBool(meta map[string]any, key string) bool {
	if meta == nil {
		return false
	}
	v, ok := meta[key]
	if !ok {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(val, "true") || val == "1"
	default:
		return false
	}
}

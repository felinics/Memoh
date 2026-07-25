package timeline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RenderedContentPiece maps to LLM API content parts.
type RenderedContentPiece struct {
	Type string `json:"type"` // "text" or "image"
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
}

// ImageAttachmentRef holds the content hash and MIME type of an image
// attachment that can be inlined as a vision input via the media store.
type ImageAttachmentRef struct {
	ContentHash string `json:"content_hash"`
	Mime        string `json:"mime,omitempty"`
}

// RenderedSegment is a single segment of rendered context, one per IC node.
type RenderedSegment struct {
	ReceivedAtMs int64                  `json:"received_at_ms"`
	Content      []RenderedContentPiece `json:"content"`
	IsMyself     bool                   `json:"is_myself,omitempty"`
	IsSelfSent   bool                   `json:"is_self_sent,omitempty"`
	MentionsMe   bool                   `json:"mentions_me,omitempty"`
	RepliesToMe  bool                   `json:"replies_to_me,omitempty"`
	ImageRefs    []ImageAttachmentRef   `json:"image_refs,omitempty"`
}

// RenderedContext is the output of the Rendering layer — a slice of segments.
type RenderedContext []RenderedSegment

// RenderParams controls rendering behavior.
type RenderParams struct {
	BotUserID    string
	ContactNames map[string]string
}

// Render converts an IntermediateContext into a RenderedContext.
func Render(ic IntermediateContext, params RenderParams) RenderedContext {
	segments := make([]RenderedSegment, 0, len(ic.Nodes))

	for _, node := range ic.Nodes {
		if node.Message != nil {
			seg := renderMessage(node.Message, params)
			segments = append(segments, seg)
		} else if node.SystemEvent != nil {
			seg := renderSystemEvent(node.SystemEvent, params)
			segments = append(segments, seg)
		}
	}

	return segments
}

// RCToXML converts a RenderedContext to a single XML string for debugging.
func RCToXML(rc RenderedContext) string {
	var sb strings.Builder
	for _, seg := range rc {
		for _, p := range seg.Content {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			} else {
				sb.WriteString("[thumbnail]")
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func renderMessage(msg *ICMessage, params RenderParams) RenderedSegment {
	isMyself := params.BotUserID != "" && msg.Sender != nil && msg.Sender.ID == params.BotUserID
	mentionsMe := msg.MentionsMe || (params.BotUserID != "" && hasMention(msg.Content, params.BotUserID))
	repliesToMe := msg.RepliesToMe || (params.BotUserID != "" && msg.ReplyToSender != nil && msg.ReplyToSender.ID == params.BotUserID)

	attrs := []string{}
	attrs = append(attrs, fmt.Sprintf("id=%q", escapeXMLAttrValue(msg.MessageID)))
	if msg.Sender != nil {
		attrs = append(attrs, fmt.Sprintf("sender=%q", escapeXMLAttrValue(formatSender(msg.Sender, params.ContactNames))))
	}
	if isMyself {
		attrs = append(attrs, `myself="true"`)
	}
	attrs = append(attrs, fmt.Sprintf("t=%q", formatTimestamp(msg.TimestampSec, msg.UTCOffsetMin)))

	if msg.EditedAtSec > 0 {
		attrs = append(attrs, fmt.Sprintf("edited=%q", formatTimestamp(msg.EditedAtSec, msg.EditUTCOffsetMin)))
	}

	attrs = append(attrs, fmt.Sprintf("channel=%q", escapeXMLAttrValue(msg.Conversation.Channel)))
	if msg.Conversation.ConversationName != "" {
		attrs = append(attrs, fmt.Sprintf("conversation=%q", escapeXMLAttrValue(msg.Conversation.ConversationName)))
	}
	if msg.Conversation.ConversationType != "" {
		attrs = append(attrs, fmt.Sprintf("type=%q", escapeXMLAttrValue(msg.Conversation.ConversationType)))
	}
	if msg.Conversation.Target != "" {
		attrs = append(attrs, fmt.Sprintf("target=%q", escapeXMLAttrValue(msg.Conversation.Target)))
	}

	if msg.ForwardInfo != nil {
		from := resolveForwardFrom(msg.ForwardInfo, params.ContactNames)
		attrs = append(attrs, fmt.Sprintf("forwarded_from=%q", escapeXMLAttrValue(from)))
		if msg.ForwardInfo.MessageID != "" {
			attrs = append(attrs, fmt.Sprintf("forwarded_message_id=%q", escapeXMLAttrValue(msg.ForwardInfo.MessageID)))
		}
	}

	if msg.Deleted {
		text := fmt.Sprintf("<message %s/>", strings.Join(attrs, " "))
		return RenderedSegment{
			ReceivedAtMs: msg.ReceivedAtMs,
			Content:      []RenderedContentPiece{{Type: "text", Text: text}},
			IsMyself:     isMyself,
			IsSelfSent:   msg.IsSelfSent,
			MentionsMe:   mentionsMe,
			RepliesToMe:  repliesToMe,
		}
	}

	var parts []string

	if msg.ReplyToMessageID != "" {
		replyAttrs := []string{fmt.Sprintf("id=%q", escapeXMLAttrValue(msg.ReplyToMessageID))}
		if msg.ReplyToSender != nil {
			replyAttrs = append(replyAttrs, fmt.Sprintf("sender=%q", escapeXMLAttrValue(formatSender(msg.ReplyToSender, params.ContactNames))))
		}
		preview := ""
		if msg.ReplyToPreview != "" {
			preview = escapeXMLText(msg.ReplyToPreview)
		}
		parts = append(parts, fmt.Sprintf("<in-reply-to %s>%s</in-reply-to>", strings.Join(replyAttrs, " "), preview))
	}

	body := renderContentNodes(msg.Content)
	if body != "" {
		parts = append(parts, body)
	}

	for _, att := range msg.Attachments {
		parts = append(parts, renderAttachment(att))
	}

	text := fmt.Sprintf("<message %s>\n%s\n</message>", strings.Join(attrs, " "), strings.Join(parts, "\n"))

	pieces := []RenderedContentPiece{{Type: "text", Text: text}}

	var imageRefs []ImageAttachmentRef
	for _, att := range msg.Attachments {
		if strings.EqualFold(att.Type, "image") && att.ContentHash != "" {
			imageRefs = append(imageRefs, ImageAttachmentRef{
				ContentHash: att.ContentHash,
				Mime:        att.MimeType,
			})
		}
	}

	return RenderedSegment{
		ReceivedAtMs: msg.ReceivedAtMs,
		Content:      pieces,
		IsMyself:     isMyself,
		IsSelfSent:   msg.IsSelfSent,
		MentionsMe:   mentionsMe,
		RepliesToMe:  repliesToMe,
		ImageRefs:    imageRefs,
	}
}

func renderSystemEvent(event *ICSystemEvent, params RenderParams) RenderedSegment {
	text := renderSystemEventXML(event, params.ContactNames)
	return RenderedSegment{
		ReceivedAtMs: event.ReceivedAtMs,
		Content:      []RenderedContentPiece{{Type: "text", Text: text}},
	}
}

func renderSystemEventXML(event *ICSystemEvent, contactNames map[string]string) string {
	t := formatTimestamp(event.TimestampSec, event.UTCOffsetMin)
	actorAttr := ""
	if event.Actor != nil {
		actorAttr = fmt.Sprintf(` actor=%q`, escapeXMLAttrValue(formatSender(event.Actor, contactNames)))
	}

	switch event.Kind {
	case "user_renamed":
		return fmt.Sprintf(`<event type="name_change" t=%q from_name=%q to_name=%q/>`,
			t,
			escapeXMLAttrValue(formatSender(event.OldUser, contactNames)),
			escapeXMLAttrValue(formatSender(event.NewUser, contactNames)))

	case "members_joined":
		names := make([]string, 0, len(event.Members))
		for _, m := range event.Members {
			names = append(names, formatSender(&m, contactNames))
		}
		return fmt.Sprintf(`<event type="members_joined" t=%q%s members=%q/>`,
			t, actorAttr, escapeXMLAttrValue(strings.Join(names, ", ")))

	case "member_left":
		return fmt.Sprintf(`<event type="member_left" t=%q%s member=%q/>`,
			t, actorAttr, escapeXMLAttrValue(formatSender(event.Member, contactNames)))

	case "chat_renamed":
		fromAttr := ""
		if event.OldTitle != "" {
			fromAttr = fmt.Sprintf(` from=%q`, escapeXMLAttrValue(event.OldTitle))
		}
		return fmt.Sprintf(`<event type="chat_renamed" t=%q%s%s to=%q/>`,
			t, actorAttr, fromAttr, escapeXMLAttrValue(event.NewTitle))

	case "chat_photo_changed":
		return fmt.Sprintf(`<event type="chat_photo_changed" t=%q%s/>`, t, actorAttr)

	case "chat_photo_deleted":
		return fmt.Sprintf(`<event type="chat_photo_deleted" t=%q%s/>`, t, actorAttr)

	case "message_pinned":
		if event.PinnedPreview != "" {
			return fmt.Sprintf(`<event type="message_pinned" t=%q%s message_id=%q>%s</event>`,
				t, actorAttr, escapeXMLAttrValue(event.PinnedMessageID), escapeXMLText(event.PinnedPreview))
		}
		return fmt.Sprintf(`<event type="message_pinned" t=%q%s message_id=%q/>`,
			t, actorAttr, escapeXMLAttrValue(event.PinnedMessageID))

	default:
		return ""
	}
}

// --- Helpers ---

func escapeXMLAttrValue(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
	)
	return r.Replace(s)
}

func escapeXMLText(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}

func formatSender(user *CanonicalUser, contactNames map[string]string) string {
	if user == nil {
		return ""
	}
	if contactNames != nil {
		if name, ok := contactNames[user.ID]; ok {
			if user.Username != "" && user.Username != name {
				return name + " (@" + user.Username + ")"
			}
			return name
		}
	}
	displayName := user.DisplayName
	if displayName == "" {
		if user.Username != "" {
			return user.Username
		}
		return user.ID
	}
	if user.Username != "" && user.Username != displayName {
		return displayName + " (@" + user.Username + ")"
	}
	return displayName
}

func resolveForwardFrom(info *ForwardInfo, contactNames map[string]string) string {
	if info.Sender != nil {
		return formatSender(info.Sender, contactNames)
	}
	if info.SenderName != "" {
		return info.SenderName
	}
	if info.FromUserID != "" {
		return "user:" + info.FromUserID
	}
	if info.FromConversationID != "" {
		return "conversation:" + info.FromConversationID
	}
	return "unknown"
}

func pad2(n int) string {
	if n < 10 && n >= 0 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func formatTimestamp(epochSec int64, utcOffsetMin int) string {
	t := time.Unix(epochSec, 0).UTC().Add(time.Duration(utcOffsetMin) * time.Minute)
	date := fmt.Sprintf("%d-%s-%s", t.Year(), pad2(int(t.Month())), pad2(t.Day()))
	timeStr := fmt.Sprintf("%s:%s:%s", pad2(t.Hour()), pad2(t.Minute()), pad2(t.Second()))

	sign := "+"
	abs := utcOffsetMin
	if utcOffsetMin < 0 {
		sign = "-"
		abs = -utcOffsetMin
	}
	offset := fmt.Sprintf("%s%s:%s", sign, pad2(abs/60), pad2(abs%60))
	return date + "T" + timeStr + offset
}

func hasMention(nodes []ContentNode, userID string) bool {
	for _, n := range nodes {
		if n.Type == "mention" && n.UserID == userID {
			return true
		}
		if hasMention(n.Children, userID) {
			return true
		}
	}
	return false
}

func renderContentNodes(nodes []ContentNode) string {
	var sb strings.Builder
	for _, n := range nodes {
		renderContentNode(&sb, n)
	}
	return sb.String()
}

func renderContentNode(sb *strings.Builder, node ContentNode) {
	switch node.Type {
	case "text":
		sb.WriteString(escapeXMLText(node.Text))
	case "code":
		sb.WriteString("<code>")
		sb.WriteString(escapeXMLText(node.Text))
		sb.WriteString("</code>")
	case "pre":
		if node.Language != "" {
			fmt.Fprintf(sb, `<pre lang=%q>`, escapeXMLAttrValue(node.Language))
		} else {
			sb.WriteString("<pre>")
		}
		sb.WriteString(escapeXMLText(node.Text))
		sb.WriteString("</pre>")
	case "bold":
		sb.WriteString("<b>")
		renderChildren(sb, node.Children)
		sb.WriteString("</b>")
	case "italic":
		sb.WriteString("<i>")
		renderChildren(sb, node.Children)
		sb.WriteString("</i>")
	case "underline":
		sb.WriteString("<u>")
		renderChildren(sb, node.Children)
		sb.WriteString("</u>")
	case "strikethrough":
		sb.WriteString("<s>")
		renderChildren(sb, node.Children)
		sb.WriteString("</s>")
	case "spoiler":
		sb.WriteString("<spoiler>")
		renderChildren(sb, node.Children)
		sb.WriteString("</spoiler>")
	case "blockquote":
		sb.WriteString("<blockquote>")
		renderChildren(sb, node.Children)
		sb.WriteString("</blockquote>")
	case "link":
		fmt.Fprintf(sb, `<a href=%q>`, escapeXMLAttrValue(node.URL))
		renderChildren(sb, node.Children)
		sb.WriteString("</a>")
	case "mention":
		if node.UserID != "" {
			fmt.Fprintf(sb, `<mention uid=%q>`, escapeXMLAttrValue(node.UserID))
		} else {
			sb.WriteString("<mention>")
		}
		renderChildren(sb, node.Children)
		sb.WriteString("</mention>")
	case "custom_emoji":
		renderChildren(sb, node.Children)
	}
}

func renderChildren(sb *strings.Builder, children []ContentNode) {
	for _, child := range children {
		renderContentNode(sb, child)
	}
}

func renderAttachment(att Attachment) string {
	attrs := []string{fmt.Sprintf("type=%q", att.Type)}
	if att.MimeType != "" {
		attrs = append(attrs, fmt.Sprintf("mime=%q", escapeXMLAttrValue(att.MimeType)))
	}
	if att.FileName != "" {
		attrs = append(attrs, fmt.Sprintf("name=%q", escapeXMLAttrValue(att.FileName)))
	}
	if att.Width > 0 && att.Height > 0 {
		attrs = append(attrs, fmt.Sprintf("size=%q", fmt.Sprintf("%dx%d", att.Width, att.Height)))
	}
	if att.Duration > 0 {
		attrs = append(attrs, fmt.Sprintf("duration=%q", strconv.Itoa(att.Duration)))
	}
	if att.FilePath != "" {
		attrs = append(attrs, fmt.Sprintf("path=%q", escapeXMLAttrValue(att.FilePath)))
	}
	if att.AltText != "" {
		return fmt.Sprintf("<image %s>%s</image>", strings.Join(attrs, " "), escapeXMLText(att.AltText))
	}
	return fmt.Sprintf("<attachment %s/>", strings.Join(attrs, " "))
}

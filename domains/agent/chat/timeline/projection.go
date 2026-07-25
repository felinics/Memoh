package timeline

import "strings"

// ICMessage represents a message node in the IntermediateContext.
type ICMessage struct {
	Type             string           `json:"type"` // always "message"
	MessageID        string           `json:"message_id"`
	Sender           *CanonicalUser   `json:"sender,omitempty"`
	ReceivedAtMs     int64            `json:"received_at_ms"`
	TimestampSec     int64            `json:"timestamp_sec"`
	UTCOffsetMin     int              `json:"utc_offset_min"`
	Content          []ContentNode    `json:"content"`
	ReplyToMessageID string           `json:"reply_to_message_id,omitempty"`
	ReplyToSender    *CanonicalUser   `json:"reply_to_sender,omitempty"`
	ReplyToPreview   string           `json:"reply_to_preview,omitempty"`
	ForwardInfo      *ForwardInfo     `json:"forward_info,omitempty"`
	Attachments      []Attachment     `json:"attachments"`
	EditedAtSec      int64            `json:"edited_at_sec,omitempty"`
	EditUTCOffsetMin int              `json:"edit_utc_offset_min,omitempty"`
	Deleted          bool             `json:"deleted,omitempty"`
	IsSelfSent       bool             `json:"is_self_sent,omitempty"`
	MentionsMe       bool             `json:"mentions_me,omitempty"`
	RepliesToMe      bool             `json:"replies_to_me,omitempty"`
	Conversation     ConversationMeta `json:"conversation"`
}

// ICSystemEvent represents a group lifecycle event in the IC.
type ICSystemEvent struct {
	Type         string         `json:"type"` // always "system_event"
	Kind         string         `json:"kind"`
	ReceivedAtMs int64          `json:"received_at_ms"`
	TimestampSec int64          `json:"timestamp_sec"`
	UTCOffsetMin int            `json:"utc_offset_min"`
	Actor        *CanonicalUser `json:"actor,omitempty"`

	// Kind-specific fields
	UserID   string          `json:"user_id,omitempty"`
	OldUser  *CanonicalUser  `json:"old_user,omitempty"`
	NewUser  *CanonicalUser  `json:"new_user,omitempty"`
	Members  []CanonicalUser `json:"members,omitempty"`
	Member   *CanonicalUser  `json:"member,omitempty"`
	OldTitle string          `json:"old_title,omitempty"`
	NewTitle string          `json:"new_title,omitempty"`
	// For message_pinned
	PinnedMessageID string `json:"pinned_message_id,omitempty"`
	PinnedPreview   string `json:"pinned_preview,omitempty"`
}

// ICNode is a union of ICMessage and ICSystemEvent.
type ICNode struct {
	Message     *ICMessage     `json:"message,omitempty"`
	SystemEvent *ICSystemEvent `json:"system_event,omitempty"`
}

// GetReceivedAtMs returns the node's receivedAtMs for ordering.
func (n ICNode) GetReceivedAtMs() int64 {
	if n.Message != nil {
		return n.Message.ReceivedAtMs
	}
	if n.SystemEvent != nil {
		return n.SystemEvent.ReceivedAtMs
	}
	return 0
}

// ICUserState tracks per-user statistics.
type ICUserState struct {
	User          CanonicalUser `json:"user"`
	FirstSeenAtMs int64         `json:"first_seen_at_ms"`
	LastSeenAtMs  int64         `json:"last_seen_at_ms"`
	MessageCount  int           `json:"message_count"`
}

// IntermediateContext is the per-session state produced by the Projection layer.
type IntermediateContext struct {
	SessionID string                 `json:"session_id"`
	Nodes     []ICNode               `json:"nodes"`
	Users     map[string]ICUserState `json:"users"`
	ChatTitle string                 `json:"chat_title,omitempty"`
}

// NewEmptyIC creates a fresh IntermediateContext for a session.
func NewEmptyIC(sessionID string) IntermediateContext {
	return IntermediateContext{
		SessionID: sessionID,
		Nodes:     nil,
		Users:     make(map[string]ICUserState),
	}
}

const replyPreviewMax = 100

func truncate(text string, maxLen int) string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen]) + "…"
}

// ContentToPlainText extracts plain text from a ContentNode tree.
func ContentToPlainText(nodes []ContentNode) string {
	var sb strings.Builder
	for _, n := range nodes {
		contentNodeToPlainText(&sb, n)
	}
	return sb.String()
}

func contentNodeToPlainText(sb *strings.Builder, n ContentNode) {
	if n.Text != "" {
		sb.WriteString(n.Text)
	}
	for _, child := range n.Children {
		contentNodeToPlainText(sb, child)
	}
}

func findMessageIndex(nodes []ICNode, messageID string) int {
	for i := len(nodes) - 1; i >= 0; i-- {
		if m := nodes[i].Message; m != nil && m.MessageID == messageID {
			return i
		}
	}
	return -1
}

func userChanged(a, b CanonicalUser) bool {
	return a.DisplayName != b.DisplayName || a.Username != b.Username
}

// Reduce applies a CanonicalEvent to an IntermediateContext, returning the new IC.
// This is a pure function — it does not mutate the input IC.
func Reduce(ic IntermediateContext, event CanonicalEvent) IntermediateContext {
	out := cloneIC(ic)
	switch e := event.(type) {
	case MessageEvent:
		reduceMessage(&out, e)
	case EditEvent:
		reduceEdit(&out, e)
	case DeleteEvent:
		reduceDelete(&out, e)
	case ServiceEvent:
		reduceService(&out, e)
	}
	return out
}

func cloneIC(ic IntermediateContext) IntermediateContext {
	nodes := make([]ICNode, len(ic.Nodes))
	copy(nodes, ic.Nodes)
	users := make(map[string]ICUserState, len(ic.Users))
	for k, v := range ic.Users {
		users[k] = v
	}
	return IntermediateContext{
		SessionID: ic.SessionID,
		Nodes:     nodes,
		Users:     users,
		ChatTitle: ic.ChatTitle,
	}
}

func reduceMessage(ic *IntermediateContext, event MessageEvent) {
	// Dedup: skip if message already exists; merge isSelfSent flag.
	existingIdx := findMessageIndex(ic.Nodes, event.MessageID)
	if existingIdx != -1 {
		if event.IsSelfSent && ic.Nodes[existingIdx].Message != nil {
			ic.Nodes[existingIdx].Message.IsSelfSent = true
		}
		if event.MentionsMe && ic.Nodes[existingIdx].Message != nil {
			ic.Nodes[existingIdx].Message.MentionsMe = true
		}
		if event.RepliesToMe && ic.Nodes[existingIdx].Message != nil {
			ic.Nodes[existingIdx].Message.RepliesToMe = true
		}
		return
	}

	// Detect user rename before appending the message.
	if event.Sender != nil {
		if existing, ok := ic.Users[event.Sender.ID]; ok && userChanged(existing.User, *event.Sender) {
			sysEvt := &ICSystemEvent{
				Type:         "system_event",
				Kind:         "user_renamed",
				ReceivedAtMs: event.ReceivedAtMs,
				TimestampSec: event.TimestampSec,
				UTCOffsetMin: event.UTCOffsetMin,
				UserID:       event.Sender.ID,
				OldUser:      &existing.User,
				NewUser:      event.Sender,
			}
			ic.Nodes = append(ic.Nodes, ICNode{SystemEvent: sysEvt})
		}
	}

	msg := &ICMessage{
		Type:         "message",
		MessageID:    event.MessageID,
		Sender:       event.Sender,
		ReceivedAtMs: event.ReceivedAtMs,
		TimestampSec: event.TimestampSec,
		UTCOffsetMin: event.UTCOffsetMin,
		Content:      event.Content,
		Attachments:  event.Attachments,
		IsSelfSent:   event.IsSelfSent,
		MentionsMe:   event.MentionsMe,
		RepliesToMe:  event.RepliesToMe,
		Conversation: event.Conversation,
	}

	if event.ReplyToMessageID != "" {
		msg.ReplyToMessageID = event.ReplyToMessageID
		targetIdx := findMessageIndex(ic.Nodes, event.ReplyToMessageID)
		if targetIdx != -1 {
			target := ic.Nodes[targetIdx].Message
			if target != nil {
				msg.ReplyToSender = target.Sender
				plain := ContentToPlainText(target.Content)
				if plain != "" {
					msg.ReplyToPreview = truncate(plain, replyPreviewMax)
				}
			}
		}
		if msg.ReplyToSender == nil && event.ReplyToSender != "" {
			msg.ReplyToSender = &CanonicalUser{DisplayName: event.ReplyToSender}
		}
		if msg.ReplyToPreview == "" && event.ReplyToPreview != "" {
			msg.ReplyToPreview = truncate(event.ReplyToPreview, replyPreviewMax)
		}
	}
	if event.ForwardInfo != nil {
		msg.ForwardInfo = event.ForwardInfo
	}

	ic.Nodes = append(ic.Nodes, ICNode{Message: msg})

	// Update user state.
	if event.Sender != nil {
		if existing, ok := ic.Users[event.Sender.ID]; ok {
			existing.User = *event.Sender
			existing.LastSeenAtMs = event.ReceivedAtMs
			existing.MessageCount++
			ic.Users[event.Sender.ID] = existing
		} else {
			ic.Users[event.Sender.ID] = ICUserState{
				User:          *event.Sender,
				FirstSeenAtMs: event.ReceivedAtMs,
				LastSeenAtMs:  event.ReceivedAtMs,
				MessageCount:  1,
			}
		}
	}
}

func reduceEdit(ic *IntermediateContext, event EditEvent) {
	idx := findMessageIndex(ic.Nodes, event.MessageID)
	if idx == -1 {
		return
	}
	msg := ic.Nodes[idx].Message
	if msg == nil {
		return
	}
	msg.Content = event.Content
	msg.Attachments = event.Attachments
	msg.EditedAtSec = event.TimestampSec
	msg.EditUTCOffsetMin = event.UTCOffsetMin
}

func reduceDelete(ic *IntermediateContext, event DeleteEvent) {
	for _, messageID := range event.MessageIDs {
		idx := findMessageIndex(ic.Nodes, messageID)
		if idx == -1 {
			continue
		}
		if msg := ic.Nodes[idx].Message; msg != nil {
			msg.Deleted = true
		}
	}
}

func reduceService(ic *IntermediateContext, event ServiceEvent) {
	base := ICSystemEvent{
		Type:         "system_event",
		ReceivedAtMs: event.ReceivedAtMs,
		TimestampSec: event.TimestampSec,
		UTCOffsetMin: event.UTCOffsetMin,
		Actor:        event.Actor,
	}

	switch event.Action {
	case ServiceMembersJoined:
		base.Kind = "members_joined"
		base.Members = event.Members
	case ServiceMemberLeft:
		base.Kind = "member_left"
		base.Member = event.Member
	case ServiceChatRenamed:
		base.Kind = "chat_renamed"
		base.OldTitle = ic.ChatTitle
		base.NewTitle = event.NewTitle
		ic.ChatTitle = event.NewTitle
	case ServiceChatPhotoChanged:
		base.Kind = "chat_photo_changed"
	case ServiceChatPhotoDeleted:
		base.Kind = "chat_photo_deleted"
	case ServiceMessagePinned:
		base.Kind = "message_pinned"
		base.PinnedMessageID = event.PinnedMessageID
		targetIdx := findMessageIndex(ic.Nodes, event.PinnedMessageID)
		if targetIdx != -1 {
			if target := ic.Nodes[targetIdx].Message; target != nil {
				plain := ContentToPlainText(target.Content)
				if plain != "" {
					base.PinnedPreview = truncate(plain, replyPreviewMax)
				}
			}
		}
	}

	ic.Nodes = append(ic.Nodes, ICNode{SystemEvent: &base})
}

// Package timeline implements the Deterministic Context Pipeline (DCP) for
// assembling LLM context from canonical IM events. It provides Adaptation
// types, Projection (reduce), and Rendering (RC) layers.
package timeline

// EventKind classifies a canonical event.
type EventKind string

const (
	EventMessage EventKind = "message"
	EventEdit    EventKind = "edit"
	EventDelete  EventKind = "delete"
	EventService EventKind = "service"
)

// CanonicalUser is a platform-agnostic sender identity.
type CanonicalUser struct {
	// ID is the channel_identity_id (Memoh UUID).
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username,omitempty"`
	IsBot       bool   `json:"is_bot,omitempty"`
}

// ContentNode represents a rich-text tree node, parsed from platform-specific
// encodings (e.g. Telegram entities, Discord markdown).
type ContentNode struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	Language string        `json:"language,omitempty"`
	URL      string        `json:"url,omitempty"`
	UserID   string        `json:"user_id,omitempty"`
	Children []ContentNode `json:"children,omitempty"`
}

// Attachment is a platform-agnostic media attachment.
type Attachment struct {
	Type         string `json:"type"`
	MimeType     string `json:"mime_type,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	ThumbnailB64 string `json:"thumbnail_b64,omitempty"`
	AltText      string `json:"alt_text,omitempty"`
	// FilePath is the workspace path where the attachment is stored.
	FilePath string `json:"file_path,omitempty"`
	// ContentHash is the media-store content hash for persisted attachments.
	ContentHash string `json:"content_hash,omitempty"`
}

// ForwardInfo describes a forwarded message origin.
type ForwardInfo struct {
	MessageID          string         `json:"message_id,omitempty"`
	FromUserID         string         `json:"from_user_id,omitempty"`
	FromConversationID string         `json:"from_conversation_id,omitempty"`
	Sender             *CanonicalUser `json:"sender,omitempty"`
	SenderName         string         `json:"sender_name,omitempty"`
	Date               int64          `json:"date,omitempty"`
}

// ConversationMeta carries session-level context embedded in every event,
// so each rendered message is self-contained.
type ConversationMeta struct {
	Channel          string `json:"channel"`
	ConversationName string `json:"conversation_name,omitempty"`
	ConversationType string `json:"conversation_type"`
	Target           string `json:"target,omitempty"`
}

// --- Concrete event types ---

// MessageEvent represents a new message in a session.
type MessageEvent struct {
	SessionID        string           `json:"session_id"`
	EventID          string           `json:"event_id,omitempty"`
	MessageID        string           `json:"message_id"`
	Sender           *CanonicalUser   `json:"sender,omitempty"`
	ReceivedAtMs     int64            `json:"received_at_ms"`
	TimestampSec     int64            `json:"timestamp_sec"`
	UTCOffsetMin     int              `json:"utc_offset_min"`
	Content          []ContentNode    `json:"content"`
	ReplyToMessageID string           `json:"reply_to_message_id,omitempty"`
	ReplyToSender    string           `json:"reply_to_sender,omitempty"`
	ReplyToPreview   string           `json:"reply_to_preview,omitempty"`
	ForwardInfo      *ForwardInfo     `json:"forward_info,omitempty"`
	Attachments      []Attachment     `json:"attachments"`
	IsSelfSent       bool             `json:"is_self_sent,omitempty"`
	MentionsMe       bool             `json:"mentions_me,omitempty"`
	RepliesToMe      bool             `json:"replies_to_me,omitempty"`
	Conversation     ConversationMeta `json:"conversation"`
}

func (MessageEvent) Kind() EventKind          { return EventMessage }
func (e MessageEvent) GetSessionID() string   { return e.SessionID }
func (e MessageEvent) GetReceivedAtMs() int64 { return e.ReceivedAtMs }

// EditEvent represents a message edit.
type EditEvent struct {
	SessionID    string         `json:"session_id"`
	EventID      string         `json:"event_id,omitempty"`
	MessageID    string         `json:"message_id"`
	Sender       *CanonicalUser `json:"sender,omitempty"`
	ReceivedAtMs int64          `json:"received_at_ms"`
	TimestampSec int64          `json:"timestamp_sec"`
	UTCOffsetMin int            `json:"utc_offset_min"`
	Content      []ContentNode  `json:"content"`
	Attachments  []Attachment   `json:"attachments"`
}

func (EditEvent) Kind() EventKind          { return EventEdit }
func (e EditEvent) GetSessionID() string   { return e.SessionID }
func (e EditEvent) GetReceivedAtMs() int64 { return e.ReceivedAtMs }

// DeleteEvent represents one or more deleted messages.
type DeleteEvent struct {
	SessionID    string   `json:"session_id"`
	EventID      string   `json:"event_id,omitempty"`
	MessageIDs   []string `json:"message_ids"`
	ReceivedAtMs int64    `json:"received_at_ms"`
	TimestampSec int64    `json:"timestamp_sec"`
	UTCOffsetMin int      `json:"utc_offset_min"`
}

func (DeleteEvent) Kind() EventKind          { return EventDelete }
func (e DeleteEvent) GetSessionID() string   { return e.SessionID }
func (e DeleteEvent) GetReceivedAtMs() int64 { return e.ReceivedAtMs }

// ServiceAction classifies a group lifecycle event.
type ServiceAction string

const (
	ServiceMembersJoined    ServiceAction = "members_joined"
	ServiceMemberLeft       ServiceAction = "member_left"
	ServiceChatRenamed      ServiceAction = "chat_renamed"
	ServiceChatPhotoChanged ServiceAction = "chat_photo_changed"
	ServiceChatPhotoDeleted ServiceAction = "chat_photo_deleted"
	ServiceMessagePinned    ServiceAction = "message_pinned"
)

// ServiceEvent represents a group lifecycle event (join, leave, rename, etc.).
type ServiceEvent struct {
	SessionID    string         `json:"session_id"`
	EventID      string         `json:"event_id,omitempty"`
	Action       ServiceAction  `json:"action"`
	Actor        *CanonicalUser `json:"actor,omitempty"`
	ReceivedAtMs int64          `json:"received_at_ms"`
	TimestampSec int64          `json:"timestamp_sec"`
	UTCOffsetMin int            `json:"utc_offset_min"`

	// Action-specific fields
	Members  []CanonicalUser `json:"members,omitempty"`
	Member   *CanonicalUser  `json:"member,omitempty"`
	NewTitle string          `json:"new_title,omitempty"`
	OldTitle string          `json:"old_title,omitempty"`
	// For message_pinned
	PinnedMessageID string `json:"pinned_message_id,omitempty"`
	PinnedPreview   string `json:"pinned_preview,omitempty"`
}

func (ServiceEvent) Kind() EventKind          { return EventService }
func (e ServiceEvent) GetSessionID() string   { return e.SessionID }
func (e ServiceEvent) GetReceivedAtMs() int64 { return e.ReceivedAtMs }

// CanonicalEvent is the interface satisfied by all event types.
type CanonicalEvent interface {
	Kind() EventKind
	GetSessionID() string
	GetReceivedAtMs() int64
}

// Package channel provides a unified abstraction for multi-platform messaging channels.
// It defines types, interfaces, and a registry for channel adapters such as Telegram and Feishu.
package channel

import (
	"strings"
	"time"
)

// ChannelType identifies a messaging platform (e.g., "telegram", "feishu").
type ChannelType string

// String returns the channel type as a plain string.
func (c ChannelType) String() string {
	return string(c)
}

// Identity represents a sender's identity on a channel.
type Identity struct {
	SubjectID   string
	DisplayName string
	Attributes  map[string]string
}

// Attribute returns the trimmed value for the given key, or empty string if absent.
func (i Identity) Attribute(key string) string {
	if i.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(i.Attributes[key])
}

// Conversation holds metadata about the chat or group context.
type Conversation struct {
	ID       string
	Type     string
	Name     string
	ThreadID string
	Metadata map[string]any
}

const (
	ConversationTypePrivate = "private"
	ConversationTypeGroup   = "group"
	ConversationTypeThread  = "thread"
)

const (
	TelegramDiscussPassiveSampleRateMetadataKey  = "telegram_discuss_passive_sample_rate"
	TelegramDiscussForceReplyKeywordsMetadataKey = "telegram_discuss_force_reply_keywords"
	TelegramDiscussSendFallbackMetadataKey       = "telegram_discuss_send_fallback_enabled"
	DefaultTelegramDiscussPassiveSampleRate      = 0.25
)

// NormalizeConversationType normalizes conversation type values within the
// channel abstraction domain: private/group/thread.
func NormalizeConversationType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "p2p", "direct", ConversationTypePrivate:
		return ConversationTypePrivate
	case ConversationTypeThread:
		return ConversationTypeThread
	case ConversationTypeGroup:
		return ConversationTypeGroup
	default:
		return ConversationTypeGroup
	}
}

// IsPrivateConversationType reports whether the conversation is private.
func IsPrivateConversationType(raw string) bool {
	return NormalizeConversationType(raw) == ConversationTypePrivate
}

// InboundMessage is a message received from an external channel.
type InboundMessage struct {
	Channel      ChannelType
	Message      Message
	BotID        string
	ReplyTarget  string
	RouteKey     string
	Sender       Identity
	Conversation Conversation
	ReceivedAt   time.Time
	Source       string
	Metadata     map[string]any
}

// RoutingKey returns a stable identifier used for reply routing.
// Format: platform:bot_id:conversation_id[:sender_id].
func (m InboundMessage) RoutingKey() string {
	if strings.TrimSpace(m.RouteKey) != "" {
		return strings.TrimSpace(m.RouteKey)
	}
	senderID := strings.TrimSpace(m.Sender.SubjectID)
	if senderID == "" {
		senderID = strings.TrimSpace(m.Sender.DisplayName)
	}
	return GenerateRoutingKey(string(m.Channel), m.BotID, m.Conversation.ID, m.Conversation.Type, senderID)
}

// GenerateRoutingKey builds a route key from platform, bot, conversation, and sender info.
// For group chats, the sender ID is appended to provide per-user context.
func GenerateRoutingKey(platform, botID, conversationID, conversationType, senderID string) string {
	parts := []string{platform, botID, conversationID}
	if !IsPrivateConversationType(conversationType) {
		senderID = strings.TrimSpace(senderID)
		if senderID != "" {
			parts = append(parts, senderID)
		}
	}
	return strings.Join(parts, ":")
}

// OutboundMessage pairs a delivery target with the message content.
type OutboundMessage struct {
	Target  string  `json:"target"`
	Message Message `json:"message"`
}

// StreamEventType defines the kind of outbound stream event.
type StreamEventType string

const (
	StreamEventStatus              StreamEventType = "status"
	StreamEventDelta               StreamEventType = "delta"
	StreamEventFinal               StreamEventType = "final"
	StreamEventError               StreamEventType = "error"
	StreamEventToolCallStart       StreamEventType = "tool_call_start"
	StreamEventToolCallEnd         StreamEventType = "tool_call_end"
	StreamEventPhaseStart          StreamEventType = "phase_start"
	StreamEventPhaseEnd            StreamEventType = "phase_end"
	StreamEventAttachment          StreamEventType = "attachment"
	StreamEventAgentStart          StreamEventType = "agent_start"
	StreamEventAgentEnd            StreamEventType = "agent_end"
	StreamEventReaction            StreamEventType = "reaction"
	StreamEventSpeech              StreamEventType = "speech"
	StreamEventProcessingStarted   StreamEventType = "processing_started"
	StreamEventProcessingCompleted StreamEventType = "processing_completed"
	StreamEventProcessingFailed    StreamEventType = "processing_failed"
)

// StreamStatus indicates the lifecycle state of a streaming reply.
type StreamStatus string

const (
	StreamStatusStarted   StreamStatus = "started"
	StreamStatusCompleted StreamStatus = "completed"
	StreamStatusFailed    StreamStatus = "failed"
)

// StreamFinalizePayload carries the final reply message emitted by a stream.
type StreamFinalizePayload struct {
	Message Message `json:"message"`
}

// StreamToolCall carries tool invocation data for tool_call_start / tool_call_end events.
type StreamToolCall struct {
	Name       string   `json:"name"`
	CallID     string   `json:"call_id,omitempty"`
	Locale     string   `json:"locale,omitempty"`
	Input      any      `json:"input,omitempty"`
	Result     any      `json:"result,omitempty"`
	ApprovalID string   `json:"approval_id,omitempty"`
	ShortID    int      `json:"short_id,omitempty"`
	Actions    []Action `json:"actions,omitempty"`
}

// StreamPhase labels a processing stage within a stream (e.g., reasoning, text).
type StreamPhase string

const (
	StreamPhaseReasoning StreamPhase = "reasoning"
	StreamPhaseText      StreamPhase = "text"
)

// StreamEvent represents a unified stream event routed through the channel layer.
type StreamEvent struct {
	Type        StreamEventType        `json:"type"`
	Status      StreamStatus           `json:"status,omitempty"`
	Delta       string                 `json:"delta,omitempty"`
	Final       *StreamFinalizePayload `json:"final,omitempty"`
	Error       string                 `json:"error,omitempty"`
	ToolCall    *StreamToolCall        `json:"tool_call,omitempty"`
	Phase       StreamPhase            `json:"phase,omitempty"`
	Attachments []Attachment           `json:"attachments,omitempty"`
	Reactions   []ReactRequest         `json:"reactions,omitempty"`
	Speeches    []SpeechRequest        `json:"speeches,omitempty"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
}

// SpeechRequest carries text-to-speech synthesis text from a speech_delta stream event.
type SpeechRequest struct {
	Text string `json:"text"`
}

// StreamOptions configures how an outbound stream is initialized.
type StreamOptions struct {
	Reply           *ReplyRef      `json:"reply,omitempty"`
	SourceMessageID string         `json:"source_message_id,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// MessageFormat indicates how the message text should be rendered.
type MessageFormat string

const (
	MessageFormatPlain    MessageFormat = "plain"
	MessageFormatMarkdown MessageFormat = "markdown"
	MessageFormatRich     MessageFormat = "rich"
)

// MessagePartType identifies the kind of a rich-text message part.
type MessagePartType string

const (
	MessagePartText       MessagePartType = "text"
	MessagePartLink       MessagePartType = "link"
	MessagePartCodeBlock  MessagePartType = "code_block"
	MessagePartMention    MessagePartType = "mention"
	MessagePartEmoji      MessagePartType = "emoji"
	MessagePartHeading    MessagePartType = "heading"
	MessagePartBlockquote MessagePartType = "blockquote"
	MessagePartListItem   MessagePartType = "list_item"
)

// MessageTextStyle describes inline formatting for a text part.
type MessageTextStyle string

const (
	MessageStyleBold          MessageTextStyle = "bold"
	MessageStyleItalic        MessageTextStyle = "italic"
	MessageStyleStrikethrough MessageTextStyle = "strikethrough"
	MessageStyleCode          MessageTextStyle = "code"
	MessageStyleUnderline     MessageTextStyle = "underline"
	MessageStyleSpoiler       MessageTextStyle = "spoiler"
)

// MessagePart is a single element within a rich-text message.
type MessagePart struct {
	Type              MessagePartType    `json:"type"`
	Text              string             `json:"text,omitempty"`
	URL               string             `json:"url,omitempty"`
	Styles            []MessageTextStyle `json:"styles,omitempty"`
	Language          string             `json:"language,omitempty"`
	ChannelIdentityID string             `json:"channel_identity_id,omitempty"`
	Emoji             string             `json:"emoji,omitempty"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
}

// AttachmentType classifies the kind of binary attachment.
type AttachmentType string

const (
	AttachmentImage AttachmentType = "image"
	AttachmentAudio AttachmentType = "audio"
	AttachmentVideo AttachmentType = "video"
	AttachmentVoice AttachmentType = "voice"
	AttachmentFile  AttachmentType = "file"
	AttachmentGIF   AttachmentType = "gif"
)

// Attachment represents a binary file attached to a message.
type Attachment struct {
	Type           AttachmentType `json:"type"`
	URL            string         `json:"url,omitempty"`  // HTTP(S) or data URL
	Path           string         `json:"path,omitempty"` // workspace-local filesystem path
	PlatformKey    string         `json:"platform_key,omitempty"`
	SourcePlatform string         `json:"source_platform,omitempty"`
	ContentHash    string         `json:"content_hash,omitempty"`
	Base64         string         `json:"base64,omitempty"` // data URL for agent delivery
	Name           string         `json:"name,omitempty"`
	Size           int64          `json:"size,omitempty"`
	Mime           string         `json:"mime,omitempty"`
	DurationMs     int64          `json:"duration_ms,omitempty"`
	Width          int            `json:"width,omitempty"`
	Height         int            `json:"height,omitempty"`
	ThumbnailURL   string         `json:"thumbnail_url,omitempty"`
	Caption        string         `json:"caption,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

// Reference returns the strongest available attachment reference.
// URL is preferred for cross-platform portability, then local Path, then platform key.
func (a Attachment) Reference() string {
	if strings.TrimSpace(a.URL) != "" {
		return strings.TrimSpace(a.URL)
	}
	if strings.TrimSpace(a.Path) != "" {
		return strings.TrimSpace(a.Path)
	}
	return strings.TrimSpace(a.PlatformKey)
}

// HasReference reports whether URL, Path, or platform key is available.
func (a Attachment) HasReference() bool {
	return a.Reference() != ""
}

// Action describes an interactive button or link in a message.
type Action struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	URL   string `json:"url,omitempty"`
	// Row groups buttons into keyboard rows for renderers that support grids
	// (e.g. Telegram inline keyboards). Buttons sharing a Row render together;
	// rows appear in ascending numeric order. Renderers without grid support
	// ignore this field. 0 is the default (single row, prior behavior).
	Row int `json:"row,omitempty"`
}

// ThreadRef references a conversation thread by ID.
type ThreadRef struct {
	ID string `json:"id"`
}

// ReplyRef points to a message being replied to.
type ReplyRef struct {
	Target           string       `json:"target,omitempty"`
	MessageID        string       `json:"message_id,omitempty"`
	Sender           string       `json:"sender,omitempty"`
	Preview          string       `json:"preview,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	AttachmentsKnown bool         `json:"attachments_known,omitempty"`
}

// ForwardRef describes the structured origin of a forwarded message.
type ForwardRef struct {
	MessageID          string `json:"message_id,omitempty"`
	FromUserID         string `json:"from_user_id,omitempty"`
	FromConversationID string `json:"from_conversation_id,omitempty"`
	Sender             string `json:"sender,omitempty"`
	Date               int64  `json:"date,omitempty"`
	AttachmentsKnown   bool   `json:"attachments_known,omitempty"`
}

// Message is the unified message structure used across all channels.
//
// Thread routing should flow through Conversation.ThreadID at the envelope
// level. Thread remains for API compatibility with existing callers that still
// attach thread scope to the message body. Forward is populated inbound
// (Telegram, Misskey) and persisted to history but no adapter Send currently
// honors it on outbound — explicitly forwarding an existing message requires a
// dedicated future feature.
type Message struct {
	ID          string         `json:"id,omitempty"`
	Format      MessageFormat  `json:"format,omitempty"`
	Text        string         `json:"text,omitempty"`
	Parts       []MessagePart  `json:"parts,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Actions     []Action       `json:"actions,omitempty"`
	Thread      *ThreadRef     `json:"thread,omitempty"`
	Reply       *ReplyRef      `json:"reply,omitempty"`
	Forward     *ForwardRef    `json:"forward,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// IsEmpty reports whether the message carries no content.
func (m Message) IsEmpty() bool {
	return strings.TrimSpace(m.Text) == "" &&
		len(m.Parts) == 0 &&
		len(m.Attachments) == 0 &&
		len(m.Actions) == 0
}

// PlainText extracts the plain text representation of the message.
func (m Message) PlainText() string {
	if strings.TrimSpace(m.Text) != "" {
		text := strings.TrimSpace(m.Text)
		if len(m.Parts) == 0 {
			return text
		}
		return appendTextSection(text, RenderPartsAsPlain(m.Parts))
	}
	if len(m.Parts) == 0 {
		return ""
	}
	return strings.TrimSpace(RenderPartsAsPlain(m.Parts))
}

// BindingCriteria specifies conditions for matching a user-channel binding.
type BindingCriteria struct {
	SubjectID  string
	Attributes map[string]string
}

// Attribute returns the trimmed value for the given key, or empty string if absent.
func (c BindingCriteria) Attribute(key string) string {
	if c.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(c.Attributes[key])
}

// BindingCriteriaFromIdentity creates BindingCriteria from a channel Identity.
func BindingCriteriaFromIdentity(identity Identity) BindingCriteria {
	return BindingCriteria{
		SubjectID:  strings.TrimSpace(identity.SubjectID),
		Attributes: identity.Attributes,
	}
}

// ChannelConfig holds the configuration for a bot's channel integration.
// Disabled: true means the channel is stopped (not connected); false means enabled.
type ChannelConfig struct {
	ID               string         `json:"id"`
	TeamID           string         `json:"team_id"`
	BotID            string         `json:"bot_id"`
	ChannelType      ChannelType    `json:"channel_type"`
	Credentials      map[string]any `json:"credentials"`
	ExternalIdentity string         `json:"external_identity"`
	SelfIdentity     map[string]any `json:"self_identity"`
	Routing          map[string]any `json:"routing"`
	Disabled         bool           `json:"disabled"`
	VerifiedAt       time.Time      `json:"verified_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// ChannelIdentityBinding represents a channel identity's binding to a specific channel type.
type ChannelIdentityBinding struct {
	ID                string         `json:"id"`
	ChannelType       ChannelType    `json:"channel_type"`
	ChannelIdentityID string         `json:"channel_identity_id"`
	Config            map[string]any `json:"config"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// UpsertConfigRequest is the input for creating or updating a channel configuration.
// Disabled: true to stop the channel, false to enable it. Omitted is treated as false (enabled).
type UpsertConfigRequest struct {
	Credentials      map[string]any `json:"credentials"`
	ExternalIdentity string         `json:"external_identity,omitempty"`
	SelfIdentity     map[string]any `json:"self_identity,omitempty"`
	Routing          map[string]any `json:"routing,omitempty"`
	Disabled         *bool          `json:"disabled,omitempty"`
	VerifiedAt       *time.Time     `json:"verified_at,omitempty"`
}

// UpsertChannelIdentityConfigRequest is the input for creating or updating a channel-identity binding.
type UpsertChannelIdentityConfigRequest struct {
	Config map[string]any `json:"config"`
}

// UpdateChannelStatusRequest is the input for enabling/disabling a bot channel config.
type UpdateChannelStatusRequest struct {
	Disabled bool `json:"disabled"`
}

// SendRequest is the input for sending an outbound message through a channel.
type SendRequest struct {
	Target            string  `json:"target,omitempty"`
	ChannelIdentityID string  `json:"channel_identity_id,omitempty"`
	Message           Message `json:"message"`
}

// ReactRequest is the input for adding or removing an emoji reaction on a message.
type ReactRequest struct {
	Target    string `json:"target"`
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
	Remove    bool   `json:"remove,omitempty"`
}

// Well-known ChannelType values for platforms with special handling in the core channel package.
// Adapter packages define their own identical constant as their package-local Type.
const (
	ChannelTypeTelegram ChannelType = "telegram"
	ChannelTypeFeishu   ChannelType = "feishu"
	ChannelTypeDingtalk ChannelType = "dingtalk"
	ChannelTypeMatrix   ChannelType = "matrix"
	ChannelTypeDiscord  ChannelType = "discord"
	ChannelTypeQQ       ChannelType = "qq"
	ChannelTypeWecom    ChannelType = "wecom"
	ChannelTypeWeixin   ChannelType = "weixin"
	ChannelTypeWeChatOA ChannelType = "wechatoa"
	ChannelTypeLocal    ChannelType = "local"
	ChannelTypeSlack    ChannelType = "slack"
	ChannelTypeLine     ChannelType = "line"
)

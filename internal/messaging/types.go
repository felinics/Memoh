package messaging

import (
	"context"
	"io"
	"regexp"
	"strings"
	"time"

	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/media"
)

// Platform is the delivery platform identifier used by Agent-facing
// messaging ports. Channel adapters translate it to their own ChannelType.
type Platform string

func (p Platform) String() string {
	return string(p)
}

type MessageFormat string

const (
	MessageFormatPlain    MessageFormat = "plain"
	MessageFormatMarkdown MessageFormat = "markdown"
	MessageFormatRich     MessageFormat = "rich"
)

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

type MessageTextStyle string

const (
	MessageStyleBold          MessageTextStyle = "bold"
	MessageStyleItalic        MessageTextStyle = "italic"
	MessageStyleStrikethrough MessageTextStyle = "strikethrough"
	MessageStyleCode          MessageTextStyle = "code"
	MessageStyleUnderline     MessageTextStyle = "underline"
	MessageStyleSpoiler       MessageTextStyle = "spoiler"
)

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

type AttachmentType string

const (
	AttachmentImage AttachmentType = "image"
	AttachmentAudio AttachmentType = "audio"
	AttachmentVideo AttachmentType = "video"
	AttachmentVoice AttachmentType = "voice"
	AttachmentFile  AttachmentType = "file"
	AttachmentGIF   AttachmentType = "gif"
)

type Attachment struct {
	Type           AttachmentType `json:"type"`
	URL            string         `json:"url,omitempty"`
	Path           string         `json:"path,omitempty"`
	PlatformKey    string         `json:"platform_key,omitempty"`
	SourcePlatform string         `json:"source_platform,omitempty"`
	ContentHash    string         `json:"content_hash,omitempty"`
	Base64         string         `json:"base64,omitempty"`
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

type Action struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	URL   string `json:"url,omitempty"`
	Row   int    `json:"row,omitempty"`
}

type ThreadRef struct {
	ID string `json:"id"`
}

type ReplyRef struct {
	Target           string       `json:"target,omitempty"`
	MessageID        string       `json:"message_id,omitempty"`
	Sender           string       `json:"sender,omitempty"`
	Preview          string       `json:"preview,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	AttachmentsKnown bool         `json:"attachments_known,omitempty"`
}

type ForwardRef struct {
	MessageID          string `json:"message_id,omitempty"`
	FromUserID         string `json:"from_user_id,omitempty"`
	FromConversationID string `json:"from_conversation_id,omitempty"`
	Sender             string `json:"sender,omitempty"`
	Date               int64  `json:"date,omitempty"`
	AttachmentsKnown   bool   `json:"attachments_known,omitempty"`
}

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

func (m Message) IsEmpty() bool {
	return strings.TrimSpace(m.Text) == "" &&
		len(m.Parts) == 0 &&
		len(m.Attachments) == 0 &&
		len(m.Actions) == 0
}

type SendRequest struct {
	Target            string  `json:"target,omitempty"`
	ChannelIdentityID string  `json:"channel_identity_id,omitempty"`
	Message           Message `json:"message"`
}

type ReactRequest struct {
	Target    string `json:"target"`
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
	Remove    bool   `json:"remove,omitempty"`
}

type Sender interface {
	Send(ctx context.Context, botID string, platform Platform, req SendRequest) error
}

type Reactor interface {
	React(ctx context.Context, botID string, platform Platform, req ReactRequest) error
}

type ChannelTypeResolver interface {
	ParseChannelType(raw string) (Platform, error)
}

type AssetResolver interface {
	Stat(ctx context.Context, botID, contentHash string) (media.Asset, error)
	Open(ctx context.Context, botID, contentHash string) (io.ReadCloser, media.Asset, error)
	Ingest(ctx context.Context, input media.IngestInput) (media.Asset, error)
	GetByStorageKey(ctx context.Context, botID, storageKey string) (media.Asset, error)
	AccessPath(ctx context.Context, asset media.Asset) string
	IngestContainerFile(ctx context.Context, botID, containerPath string) (media.Asset, error)
}

// AttachmentPromoter lets the Channel boundary convert container-local
// attachment references into durable outbound assets.
type AttachmentPromoter interface {
	PromoteAttachments(ctx context.Context, botID string, platform Platform, msg Message) (Message, error)
}

type Contact struct {
	RouteID                string
	Platform               string
	ConversationType       string
	ReplyTarget            string
	ExternalConversationID string
	Metadata               map[string]any
	UpdatedAt              time.Time
}

type ContactReader interface {
	ListContacts(ctx context.Context, botID string) ([]Contact, error)
}

func BundleFromAttachment(att Attachment) attachmentpkg.Bundle {
	return attachmentpkg.Bundle{
		Type:           string(att.Type),
		Base64:         att.Base64,
		Path:           att.Path,
		URL:            att.URL,
		PlatformKey:    att.PlatformKey,
		SourcePlatform: att.SourcePlatform,
		ContentHash:    att.ContentHash,
		Name:           att.Name,
		Mime:           att.Mime,
		Size:           att.Size,
		DurationMs:     att.DurationMs,
		Width:          att.Width,
		Height:         att.Height,
		ThumbnailURL:   att.ThumbnailURL,
		Caption:        att.Caption,
		Metadata:       att.Metadata,
	}.Normalize()
}

func AttachmentFromBundle(bundle attachmentpkg.Bundle) Attachment {
	kind := AttachmentType(bundle.Type)
	if kind == "" {
		kind = AttachmentFile
	}
	return Attachment{
		Type:           kind,
		URL:            bundle.URL,
		Path:           bundle.Path,
		PlatformKey:    bundle.PlatformKey,
		SourcePlatform: bundle.SourcePlatform,
		ContentHash:    bundle.ContentHash,
		Base64:         bundle.Base64,
		Name:           bundle.Name,
		Size:           bundle.Size,
		Mime:           bundle.Mime,
		DurationMs:     bundle.DurationMs,
		Width:          bundle.Width,
		Height:         bundle.Height,
		ThumbnailURL:   bundle.ThumbnailURL,
		Caption:        bundle.Caption,
		Metadata:       bundle.Metadata,
	}
}

var markdownPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\*\*[^*]+\*\*`),
	regexp.MustCompile(`\*[^*]+\*`),
	regexp.MustCompile(`~~[^~]+~~`),
	regexp.MustCompile("`[^`]+`"),
	regexp.MustCompile("```[\\s\\S]*```"),
	regexp.MustCompile(`\[.+\]\(.+\)`),
	regexp.MustCompile(`(?m)^#{1,6}\s`),
	regexp.MustCompile(`(?m)^[-*]\s`),
	regexp.MustCompile(`(?m)^\d+\.\s`),
}

func ContainsMarkdown(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, pattern := range markdownPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

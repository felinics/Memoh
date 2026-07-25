// Package backup defines Chat-owned history backup application contracts.
package backup

import (
	"context"
	"encoding/json"
	"time"
)

// ExportReader reads a persistence-neutral Chat history snapshot.
type ExportReader interface {
	Export(context.Context, ExportRequest) (Snapshot, error)
}

// ImportWriter owns all Chat history import transactions. A coordinator first
// calls Import, passes its ID maps to Channel observation import, then calls
// BindEventReferences with Channel's event IDs. Each call is independently
// transactional and retryable. On failure after Import commits, compensate
// Channel first and Chat second; a replace import cannot restore replaced data.
type ImportWriter interface {
	Import(context.Context, ImportRequest) (ImportResult, error)
	BindEventReferences(context.Context, BindEventReferencesRequest) error
	Compensate(context.Context, ImportReceipt) error
}

// SummaryReader reads counts used by backup previews.
type SummaryReader interface {
	Summary(context.Context, string) (Summary, error)
}

type ExportRequest struct {
	BotID         string
	IncludeAssets bool
}

type Snapshot struct {
	Sessions []Session
	Messages []Message
	Assets   []Asset
}

type Session struct {
	ID              string          `json:"id"`
	BotID           string          `json:"bot_id"`
	RouteID         *string         `json:"route_id"`
	ChannelType     *string         `json:"channel_type"`
	Type            string          `json:"type"`
	SessionMode     string          `json:"session_mode"`
	RuntimeType     string          `json:"runtime_type"`
	RuntimeMetadata json.RawMessage `json:"runtime_metadata"`
	Title           string          `json:"title"`
	Metadata        json.RawMessage `json:"metadata"`
	ParentSessionID *string         `json:"parent_session_id"`
	CreatedByUserID *string         `json:"created_by_user_id"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	DeletedAt       *time.Time      `json:"deleted_at"`
}

type Message struct {
	ID                      string          `json:"id"`
	BotID                   string          `json:"bot_id"`
	SessionID               *string         `json:"session_id"`
	SenderChannelIdentityID *string         `json:"sender_channel_identity_id"`
	SenderUserID            *string         `json:"sender_user_id"`
	ExternalMessageID       *string         `json:"external_message_id"`
	SourceReplyToMessageID  *string         `json:"source_reply_to_message_id"`
	Role                    string          `json:"role"`
	Content                 json.RawMessage `json:"content"`
	Metadata                json.RawMessage `json:"metadata"`
	Usage                   json.RawMessage `json:"usage"`
	SessionMode             string          `json:"session_mode"`
	RuntimeType             string          `json:"runtime_type"`
	EventID                 *string         `json:"event_id"`
	DisplayText             *string         `json:"display_text"`
	CreatedAt               time.Time       `json:"created_at"`
	TurnID                  *string         `json:"turn_id"`
	TurnPosition            *int64          `json:"turn_position"`
	TurnMessageSeq          *int64          `json:"turn_message_seq"`
	TurnVisible             bool            `json:"turn_visible"`
	TurnSupersededByTurnID  *string         `json:"turn_superseded_by_turn_id"`
	TurnSupersededAt        *time.Time      `json:"turn_superseded_at"`
	TurnSupersededReason    *string         `json:"turn_superseded_reason"`
	SenderDisplayName       *string         `json:"sender_display_name"`
	SenderAvatarURL         *string         `json:"sender_avatar_url"`
	Platform                *string         `json:"platform"`
}

type Asset struct {
	RelID       string          `json:"rel_id"`
	MessageID   string          `json:"message_id"`
	Role        string          `json:"role"`
	Ordinal     int32           `json:"ordinal"`
	ContentHash string          `json:"content_hash"`
	Name        string          `json:"name"`
	Metadata    json.RawMessage `json:"metadata"`
}

type ImportRequest struct {
	BotID              string
	ActorUserID        string
	Replace            bool
	Sessions           []Session
	Messages           []Message
	Assets             []Asset
	RouteIDs           map[string]string
	UserIDs            map[string]string
	ChannelIdentityIDs map[string]string
}

type ImportResult struct {
	SessionIDs      map[string]string
	MessageIDs      map[string]string
	EventReferences []EventReference
	Receipt         ImportReceipt
}

type EventReference struct {
	MessageID  string
	OldEventID string
}

type ImportReceipt struct {
	BotID      string
	SessionIDs []string
	MessageIDs []string
	Replace    bool
}

type BindEventReferencesRequest struct {
	BotID    string
	Bindings []EventBinding
}

type EventBinding struct {
	MessageID string
	EventID   string
}

type Summary struct {
	Sessions int
	Messages int
	Assets   int
}

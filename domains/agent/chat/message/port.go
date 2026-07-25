package message

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound                = errors.New("message record not found")
	ErrTurnSequenceConflict    = errors.New("history turn message sequence conflict")
	ErrTransactionsUnsupported = errors.New("message transactions are unsupported")
)

type SessionSnapshot struct {
	ID             string
	BotID          string
	ParentThreadID string
	Type           string
	SessionMode    string
	RuntimeType    string
}

// Record is the normalized message row written by the Message owner.
type Record struct {
	ID                      string
	BotID                   string
	SessionID               string
	SenderChannelIdentityID string
	SenderUserID            string
	SenderDisplayName       string
	SenderAvatarURL         string
	ExternalMessageID       string
	SourceReplyToMessageID  string
	Role                    string
	Content                 []byte
	Metadata                map[string]any
	Usage                   []byte
	SessionMode             string
	RuntimeType             string
	ModelID                 string
	EventID                 string
	DisplayText             string
}

type HistoryTurnCreate struct {
	ID                 string
	BotID              string
	SessionID          string
	RequestMessageID   string
	AssistantMessageID string
}

type HistoryTurnReplace struct {
	SessionID          string
	OldTurnID          string
	RequestMessageID   string
	AssistantMessageID string
	SupersededAt       time.Time
	Reason             string
}

type ListScope string

const (
	ListAll                ListScope = "all"
	ListSince              ListScope = "since"
	ListActiveSince        ListScope = "active_since"
	ListLatest             ListScope = "latest"
	ListBefore             ListScope = "before"
	ListSession            ListScope = "session"
	ListSessionSince       ListScope = "session_since"
	ListSessionActiveSince ListScope = "session_active_since"
	ListSessionLatest      ListScope = "session_latest"
	ListSessionLatestUI    ListScope = "session_latest_ui"
	ListSessionBefore      ListScope = "session_before"
)

type ListQuery struct {
	Scope     ListScope
	BotID     string
	SessionID string
	Since     time.Time
	Before    time.Time
	Limit     int32
}

type AssetLink struct {
	MessageID   string
	Role        string
	Ordinal     int32
	ContentHash string
	Name        string
	Metadata    map[string]any
}

type MessageCursor struct {
	TurnPosition   int64
	TurnMessageSeq int64
	CreatedAt      time.Time
	MessageID      string
}

// MessageWriterStore owns active Message row writes and atomic fast paths.
type MessageWriterStore interface {
	CreateMessage(context.Context, Record) (Message, error)
	CreateMessageWithHistoryTurn(context.Context, Record, string) (Message, error)
	CreateMessageInHistoryTurnByRequest(context.Context, Record, string) (Message, error)
	CreateToolTailRound(context.Context, []Record, string) ([]Message, error)
	SupportsAtomicDirectWrites() bool
	DeleteMessages(context.Context, []string) error
	DeleteMessagesByBot(context.Context, string) error
	DeleteMessagesBySession(context.Context, string) error
	GetSessionSnapshot(context.Context, string) (SessionSnapshot, error)
	UpdateSessionMetadata(context.Context, string, map[string]any) error
	UpdateSessionMetadataWithFence(context.Context, string, string, int64, map[string]any) error
}

// HistoryTurnStore owns turn allocation, linking, and replacement.
type HistoryTurnStore interface {
	CreateHistoryTurn(context.Context, HistoryTurnCreate) (HistoryTurn, error)
	BindHistoryTurnAssistantByRequest(context.Context, string, string, string) (HistoryTurn, error)
	AppendMessageToHistoryTurnByRequest(context.Context, string, string, string) error
	AppendMessageToLatestHistoryTurn(context.Context, string, string) error
	LinkMessageToHistoryTurn(context.Context, string, string, int64) error
	LockHistoryTurnAppendByRequest(context.Context, string, string) error
	ReplaceHistoryTurn(context.Context, HistoryTurnReplace) (HistoryTurn, error)
}

// MessageQueryStore owns Message and HistoryTurn read models.
type MessageQueryStore interface {
	ListMessages(context.Context, ListQuery) ([]Message, error)
	GetVisibleMessageCursor(context.Context, string, string) (MessageCursor, error)
	ListMessagesBeforeCursor(context.Context, string, MessageCursor, int32) ([]Message, error)
	LocateMessages(context.Context, string, string, int32, int32) (LocateResult, error)
	GetMessage(context.Context, string, string) (Message, error)
	ListVisibleMessagesFrom(context.Context, string, string) ([]Message, error)
	GetVisibleHistoryTurn(context.Context, string, string) (HistoryTurn, error)
	GetLatestVisibleHistoryTurn(context.Context, string) (HistoryTurn, error)
}

// AssetLinkStore owns message-asset links and their read projection.
type AssetLinkStore interface {
	CreateAssetLink(context.Context, AssetLink) error
	ListAssetLinks(context.Context, []string) (map[string][]MessageAsset, error)
}

// Persistence is the owner-specific bundle passed through transaction closures.
type Persistence interface {
	MessageWriterStore
	HistoryTurnStore
	MessageQueryStore
	AssetLinkStore
	InTransaction(context.Context, func(Persistence) error) error
	InRuntimeFenceTransaction(context.Context, string, string, func(Persistence) error) error
}

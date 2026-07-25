package history

import (
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
)

const CollectorHistoryRecords = "history_records"

type SourceKind string

const (
	SourceDBMessage     SourceKind = "db_message"
	SourceCompactionLog SourceKind = "compaction_log"
)

type Lifecycle string

const (
	LifecyclePersisted     Lifecycle = "persisted"
	LifecycleActiveSummary Lifecycle = "active_summary"
)

type ScopeFallback struct {
	ChatID           string
	ConversationType string
	ConversationName string
	ReplyTarget      string
}

type HistoryRecord struct {
	Ref        fragment.ContextRef
	Kind       fragment.Kind
	SourceKind SourceKind
	Lifecycle  Lifecycle

	ModelMessage agentdomain.ModelMessage
	Assets       []MediaRef
	Metadata     map[string]any

	Scope      fragment.Scope
	Provenance fragment.Provenance

	DBMessageID       string
	ExternalMessageID string
	EventID           string
	SessionID         string
	SessionIDKnown    bool
	BotID             string

	SenderChannelIdentityID string
	SenderUserID            string
	SenderDisplayName       string
	Platform                string
	SourceReplyToMessageID  string

	CompactID string
	CreatedAt time.Time

	UsageInputTokens  *int
	UsageOutputTokens *int

	// Required marks a record that must survive trimming/compaction because it
	// is pinned by a retry/edit request's required-history constraint.
	Required bool

	Coverage *fragment.SummaryCoverage
}

type MediaRef struct {
	ContentHash string
	Role        string
	Ordinal     int
	Name        string
	Metadata    map[string]any
}

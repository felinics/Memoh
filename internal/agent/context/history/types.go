package historyfrag

import (
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
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
	Ref        contextfrag.ContextRef
	Kind       contextfrag.Kind
	SourceKind SourceKind
	Lifecycle  Lifecycle

	ModelMessage turn.ModelMessage
	// Synthetic marks records derived at assembly time (workspace transition
	// markers) rather than loaded from durable history: they are re-derived
	// from the surviving records after every trim decision instead of being
	// persisted or trimmed on their own.
	Synthetic bool
	Assets    []MediaRef
	Metadata  map[string]any

	Scope      contextfrag.Scope
	Provenance contextfrag.Provenance

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

	Coverage *contextfrag.SummaryCoverage
}

type MediaRef struct {
	ContentHash string
	Role        string
	Ordinal     int
	Name        string
	Metadata    map[string]any
}

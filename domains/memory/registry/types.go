package registry

import (
	"context"
	"time"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

const (
	// ToolSearchMemory is the stable agent tool name for memory search.
	ToolSearchMemory = "search_memory"

	// DefaultBuiltinProviderID is the virtual provider selected when a bot has no
	// persisted memory_provider_id. Registries materialize it per team.
	DefaultBuiltinProviderID = "__builtin_default__"

	ProviderBuiltin    = "builtin"
	ProviderMem0       = "mem0"
	ProviderOpenViking = "openviking"
)

// Instance is the public runtime surface for one memory provider.
// Consumers define narrower interfaces against these methods as needed.
type Instance interface {
	Type() string
	OnBeforeChat(ctx context.Context, req BeforeChatRequest) (*BeforeChatResult, error)
	OnAfterChat(ctx context.Context, req AfterChatRequest) error
	Add(ctx context.Context, req AddRequest) (SearchResponse, error)
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
	GetAll(ctx context.Context, req GetAllRequest) (SearchResponse, error)
	Update(ctx context.Context, req UpdateRequest) (memorydomain.Item, error)
	Delete(ctx context.Context, memoryID string) (DeleteResponse, error)
	DeleteBatch(ctx context.Context, memoryIDs []string) (DeleteResponse, error)
	DeleteAll(ctx context.Context, req DeleteAllRequest) (DeleteResponse, error)
	Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (CompactResult, error)
	Usage(ctx context.Context, filters map[string]any) (UsageResponse, error)
}

type BeforeChatRequest struct {
	Query  string
	BotID  string
	ChatID string
}

type BeforeChatResult struct {
	ContextText    string
	RetrievalMode  string
	FallbackReason string
}

type AfterChatRequest struct {
	BotID             string
	Messages          []Message
	UserID            string
	ChannelIdentityID string
	DisplayName       string
	TimezoneLocation  *time.Location
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AddRequest struct {
	Message          string         `json:"message,omitempty"`
	Messages         []Message      `json:"messages,omitempty"`
	BotID            string         `json:"bot_id,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
	RunID            string         `json:"run_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	Filters          map[string]any `json:"filters,omitempty"`
	Infer            *bool          `json:"infer,omitempty"`
	EmbeddingEnabled *bool          `json:"embedding_enabled,omitempty"`
}

type SearchRequest struct {
	Query            string         `json:"query"`
	BotID            string         `json:"bot_id,omitempty"`
	AgentID          string         `json:"agent_id,omitempty"`
	RunID            string         `json:"run_id,omitempty"`
	Limit            int            `json:"limit,omitempty"`
	Filters          map[string]any `json:"filters,omitempty"`
	Sources          []string       `json:"sources,omitempty"`
	EmbeddingEnabled *bool          `json:"embedding_enabled,omitempty"`
	NoStats          bool           `json:"no_stats,omitempty"`
}

type UpdateRequest struct {
	MemoryID         string `json:"memory_id"`
	Memory           string `json:"memory"`
	EmbeddingEnabled *bool  `json:"embedding_enabled,omitempty"`
}

type GetAllRequest struct {
	BotID   string         `json:"bot_id,omitempty"`
	AgentID string         `json:"agent_id,omitempty"`
	RunID   string         `json:"run_id,omitempty"`
	Limit   int            `json:"limit,omitempty"`
	Filters map[string]any `json:"filters,omitempty"`
	NoStats bool           `json:"no_stats,omitempty"`
}

type DeleteAllRequest struct {
	BotID   string         `json:"bot_id,omitempty"`
	AgentID string         `json:"agent_id,omitempty"`
	RunID   string         `json:"run_id,omitempty"`
	Filters map[string]any `json:"filters,omitempty"`
}

type SearchResponse struct {
	Results        []memorydomain.Item `json:"results"`
	Relations      []any               `json:"relations,omitempty"`
	RetrievalMode  string              `json:"retrieval_mode,omitempty"`
	FallbackReason string              `json:"fallback_reason,omitempty"`
}

type DeleteResponse struct {
	Message string `json:"message"`
}

type CompactResult struct {
	BeforeCount int                 `json:"before_count"`
	AfterCount  int                 `json:"after_count"`
	Ratio       float64             `json:"ratio"`
	Results     []memorydomain.Item `json:"results"`
}

type MemoryCompactCapability struct {
	Semantic     bool   `json:"semantic"`
	Archive      bool   `json:"archive,omitempty"`
	RebuildIndex bool   `json:"rebuild_index,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type UsageResponse struct {
	Count                 int   `json:"count"`
	TotalTextBytes        int64 `json:"total_text_bytes"`
	AvgTextBytes          int64 `json:"avg_text_bytes"`
	EstimatedStorageBytes int64 `json:"estimated_storage_bytes"`
}

type RebuildResult struct {
	FsCount       int `json:"fs_count"`
	StorageCount  int `json:"storage_count"`
	MissingCount  int `json:"missing_count"`
	RestoredCount int `json:"restored_count"`
}

type HealthStatus struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type MemoryStatusResponse struct {
	ProviderType      string                  `json:"provider_type,omitempty"`
	MemoryMode        string                  `json:"memory_mode,omitempty"`
	Compact           MemoryCompactCapability `json:"compact"`
	CanManualSync     bool                    `json:"can_manual_sync"`
	SourceDir         string                  `json:"source_dir,omitempty"`
	OverviewPath      string                  `json:"overview_path,omitempty"`
	MarkdownFileCount int                     `json:"markdown_file_count"`
	SourceCount       int                     `json:"source_count"`
	EdgeCount         int                     `json:"edge_count"`
	IndexedCount      int                     `json:"indexed_count"`
	VectorIndex       string                  `json:"vector_index,omitempty"`
	Encoder           *HealthStatus           `json:"encoder,omitempty"`
	Pgvector          *HealthStatus           `json:"pgvector,omitempty"`
	Degraded          bool                    `json:"degraded"`
	RetryQueueDepth   int                     `json:"retry_queue_depth"`
}

type MemoryVersionProvider interface {
	MemoryVersion(ctx context.Context, botID string) string
}

type SourceSyncProvider interface {
	Status(ctx context.Context, botID string) (MemoryStatusResponse, error)
	Rebuild(ctx context.Context, botID string) (RebuildResult, error)
}

type MarkdownIngestProvider interface {
	IngestFromMarkdown(ctx context.Context, botID string) (IngestResult, error)
}

type IngestResult struct {
	Ingested int `json:"ingested"`
	Skipped  int `json:"skipped"`
}

type SemanticCompactProvider interface {
	SemanticCompactCapability() MemoryCompactCapability
}

package adapters

import (
	"context"
	"time"
)

// BeforeChatRequest is passed to OnBeforeChat before sending to the agent gateway.
type BeforeChatRequest struct {
	Query  string
	BotID  string
	ChatID string
}

// BeforeChatResult contains memory context to inject into the conversation.
type BeforeChatResult struct {
	ContextText    string // formatted text to inject as a user message
	RetrievalMode  string // graph, file_fallback, mem0, etc.
	FallbackReason string // non-empty when the provider degraded to another retrieval path
	ResultCount    int    // number of memory items represented in ContextText; zero when item metadata is unavailable
	ResultRefs     []string
}

// AfterChatRequest is passed to OnAfterChat after receiving the gateway response.
type AfterChatRequest struct {
	BotID             string
	Messages          []Message
	UserID            string
	ChannelIdentityID string
	DisplayName       string
	TimezoneLocation  *time.Location
}

// LLM is the interface for LLM operations needed by memory service.
type LLM interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResponse, error)
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
	Compact(ctx context.Context, req CompactRequest) (CompactResponse, error)
}

type Message struct {
	Role            string `json:"role"`
	Content         string `json:"content"`
	SourceMessageID string `json:"-"`
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
	SourceMessageIDs []string       `json:"source_message_ids,omitempty"`
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
	MemoryID         string   `json:"memory_id"`
	Memory           string   `json:"memory"`
	EmbeddingEnabled *bool    `json:"embedding_enabled,omitempty"`
	SourceMessageIDs []string `json:"source_message_ids,omitempty"`
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

type MemoryItem struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	Hash      string         `json:"hash,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Score     float64        `json:"score,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	BotID     string         `json:"bot_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	// SourceMessageIDs is internal provenance. Public HTTP responses must not
	// expose raw session/message locators; tool projections authorize and render
	// them explicitly at the request boundary.
	SourceMessageIDs []string `json:"-"`
}

type SearchResponse struct {
	Results        []MemoryItem `json:"results"`
	Relations      []any        `json:"relations,omitempty"`
	RetrievalMode  string       `json:"retrieval_mode,omitempty"`
	FallbackReason string       `json:"fallback_reason,omitempty"`
}

type DeleteResponse struct {
	Message string `json:"message"`
}

type ExtractRequest struct {
	BotID            string         `json:"bot_id,omitempty"`
	Messages         []Message      `json:"messages"`
	Filters          map[string]any `json:"filters,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	TimezoneLocation *time.Location `json:"-"`
}

type ExtractResponse struct {
	Facts                []string   `json:"facts"`
	FactSourceMessageIDs [][]string `json:"-"`
}

type CandidateMemory struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	CreatedAt string         `json:"created_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type DecideRequest struct {
	BotID      string            `json:"bot_id,omitempty"`
	Facts      []string          `json:"facts"`
	Candidates []CandidateMemory `json:"candidates"`
	Filters    map[string]any    `json:"filters,omitempty"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

type DecisionAction struct {
	Event             string `json:"event"`
	ID                string `json:"id,omitempty"`
	Text              string `json:"text"`
	OldMemory         string `json:"old_memory,omitempty"`
	SourceFactIndices []int  `json:"source_fact_indices,omitempty"`
}

type DecideResponse struct {
	Actions []DecisionAction `json:"actions"`
}

type CompactRequest struct {
	BotID       string            `json:"bot_id,omitempty"`
	Memories    []CandidateMemory `json:"memories"`
	TargetCount int               `json:"target_count"`
	DecayDays   int               `json:"decay_days,omitempty"`
}

type CompactResponse struct {
	Facts []string `json:"facts"`
}

type CompactResult struct {
	BeforeCount int          `json:"before_count"`
	AfterCount  int          `json:"after_count"`
	Ratio       float64      `json:"ratio"`
	Results     []MemoryItem `json:"results"`
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
	// Degraded reports that the semantic seed index is behind the wiki store
	// (failed upserts are queued for retry); graph recall still works.
	Degraded        bool `json:"degraded"`
	RetryQueueDepth int  `json:"retry_queue_depth"`
}

// Memory provider admin types.
type ProviderType string

const (
	ProviderBuiltin    ProviderType = "builtin"
	ProviderMem0       ProviderType = "mem0"
	ProviderOpenViking ProviderType = "openviking"
)

type ProviderCreateRequest struct {
	Name     string         `json:"name"`
	Provider ProviderType   `json:"provider"`
	Config   map[string]any `json:"config,omitempty"`
}

type ProviderUpdateRequest struct {
	Name   *string        `json:"name,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

type ProviderGetResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Config    map[string]any `json:"config,omitempty"`
	IsDefault bool           `json:"is_default"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type ProviderConfigSchema struct {
	Fields map[string]ProviderFieldSchema `json:"fields"`
}

type ProviderFieldSchema struct {
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Example     any    `json:"example,omitempty"`
}

type ProviderMeta struct {
	Provider     string               `json:"provider"`
	DisplayName  string               `json:"display_name"`
	ConfigSchema ProviderConfigSchema `json:"config_schema"`
}

type ProviderCollectionStatus struct {
	Name   string       `json:"name"`
	Exists bool         `json:"exists"`
	Points int          `json:"points"`
	Health HealthStatus `json:"health"`
}

type ProviderStatusResponse struct {
	ProviderType     string                     `json:"provider_type"`
	MemoryMode       string                     `json:"memory_mode,omitempty"`
	EmbeddingModelID string                     `json:"embedding_model_id,omitempty"`
	Collections      []ProviderCollectionStatus `json:"collections,omitempty"`
}

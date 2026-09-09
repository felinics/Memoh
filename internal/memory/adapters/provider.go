package adapters

import (
	"context"

	"github.com/felinics/memoh/internal/mcp"
)

const ToolSearchMemory = "search_memory"

// DefaultBuiltinProviderID is the virtual provider selected when a bot has no
// persisted memory_provider_id. Registries materialize it per team.
const DefaultBuiltinProviderID = "__builtin_default__"

// Provider is the unified interface for memory systems. Each provider type
// (builtin, mem0, openviking, etc.) implements this independently with its
// own storage, retrieval, and tool logic.
type Provider interface {
	// Type returns the provider type identifier (e.g. "builtin", "mem0").
	Type() string

	// --- Conversation Hooks ---

	OnBeforeChat(ctx context.Context, req BeforeChatRequest) (*BeforeChatResult, error)
	OnAfterChat(ctx context.Context, req AfterChatRequest) error

	// --- MCP Tools ---

	ListTools(ctx context.Context, session mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error)
	CallTool(ctx context.Context, session mcp.ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error)

	// --- CRUD ---

	Add(ctx context.Context, req AddRequest) (SearchResponse, error)
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
	GetAll(ctx context.Context, req GetAllRequest) (SearchResponse, error)
	Update(ctx context.Context, req UpdateRequest) (MemoryItem, error)
	Delete(ctx context.Context, memoryID string) (DeleteResponse, error)
	DeleteBatch(ctx context.Context, memoryIDs []string) (DeleteResponse, error)
	DeleteAll(ctx context.Context, req DeleteAllRequest) (DeleteResponse, error)

	// --- Lifecycle ---

	Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (CompactResult, error)
	Usage(ctx context.Context, filters map[string]any) (UsageResponse, error)
}

// MemoryVersionProvider is implemented by providers that can expose a cheap
// cache-busting version for the bot's memory source of truth.
type MemoryVersionProvider interface {
	MemoryVersion(ctx context.Context, botID string) string
}

// SourceSyncProvider is implemented by providers that can report runtime status
// and rebuild derived storage from a canonical source of truth.
type SourceSyncProvider interface {
	Status(ctx context.Context, botID string) (MemoryStatusResponse, error)
	Rebuild(ctx context.Context, botID string) (RebuildResult, error)
}

// MarkdownIngestProvider is implemented by providers whose canonical source of
// truth is the DB but which also accept agent-authored Markdown files as input.
// IngestFromMarkdown reads /data/memory/*.md and upserts them as DB nodes,
// closing the gap left by the DB→file derived-view sync (which only writes
// files FROM nodes). Providers that treat files as the source of truth (e.g.
// the legacy file runtime) do not implement this.
type MarkdownIngestProvider interface {
	IngestFromMarkdown(ctx context.Context, botID string) (IngestResult, error)
}

// IngestResult reports the outcome of a file→DB memory ingest pass.
type IngestResult struct {
	// Ingested is the number of memory nodes written to the store (inserts +
	// updates; re-ingesting an unchanged file counts as an update).
	Ingested int `json:"ingested"`
	// Skipped is the number of source items that parsed to empty content or
	// failed to persist.
	Skipped int `json:"skipped"`
}

// SemanticCompactProvider is implemented by providers that can apply Memoh's
// semantic memory compact contract under the selected bot scope.
type SemanticCompactProvider interface {
	SemanticCompactCapability() MemoryCompactCapability
}

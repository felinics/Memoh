package builtin

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memseg "github.com/memohai/memoh/domains/memory/internal/segment"
	storefs "github.com/memohai/memoh/domains/memory/internal/store/fs"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
	runtimedomain "github.com/memohai/memoh/domains/runtime"
)

// fileRuntime implements a file-backed memory runtime. It serves markdown files
// directly as the source of truth with lexical search and no derived index. It
// is no longer a user-selectable mode: it survives as the graphRuntime's
// reliability fallback (graph_runtime.searchFileFallback) and as the
// __builtin_default__ provider when no database-backed wiki store is available
// (e.g. during bootstrap). Its lexical scorer fileRuntimeScore is also reused
// by the graph cache.
type fileRuntime struct {
	store memoryStore
}

// NewFileRuntime returns the file-only Runtime. Used for the bootstrap default
// provider when no wiki store is wired; not exposed as a memory_mode option.
func NewFileRuntime(store *storefs.Service) Runtime {
	return newFileRuntime(store)
}

func newFileRuntime(store memoryStore) *fileRuntime {
	if store == nil {
		return nil
	}
	return &fileRuntime{store: store}
}

func (r *fileRuntime) Add(ctx context.Context, req memprovider.AddRequest) (memprovider.SearchResponse, error) {
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return memprovider.SearchResponse{}, err
	}
	text := runtimeText(req.Message, req.Messages)
	if text == "" {
		return memprovider.SearchResponse{}, errors.New("message is required")
	}
	now := time.Now().UTC()
	item := memorydomain.Item{
		ID:        runtimeMemoryID(botID, now),
		Memory:    text,
		Hash:      runtimeHash(text),
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
		Metadata:  req.Metadata,
		BotID:     botID,
	}
	itemsToPersist := []memorydomain.Item{storeItemFromMemoryItem(item)}
	if err := r.store.PersistMemories(ctx, botID, itemsToPersist, req.Filters); err != nil {
		return memprovider.SearchResponse{}, err
	}
	return memprovider.SearchResponse{Results: []memorydomain.Item{item}, RetrievalMode: "file"}, nil
}

func (r *fileRuntime) Search(ctx context.Context, req memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return memprovider.SearchResponse{}, err
	}
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.SearchResponse{}, err
	}
	query := strings.ToLower(strings.TrimSpace(req.Query))
	results := make([]memorydomain.Item, 0, len(items))
	for _, item := range items {
		score := fileRuntimeScore(query, item.Memory)
		if query != "" && score <= 0 {
			continue
		}
		item.BotID = botID
		item.Score = score
		results = append(results, memoryItemFromStore(item))
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].UpdatedAt > results[j].UpdatedAt
		}
		return results[i].Score > results[j].Score
	})
	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}
	return memprovider.SearchResponse{Results: results, RetrievalMode: "file"}, nil
}

func (r *fileRuntime) GetAll(ctx context.Context, req memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return memprovider.SearchResponse{}, err
	}
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.SearchResponse{}, err
	}
	for i := range items {
		items[i].BotID = botID
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	if req.Limit > 0 && len(items) > req.Limit {
		items = items[:req.Limit]
	}
	return memprovider.SearchResponse{Results: memoryItemsFromStore(items), RetrievalMode: "file"}, nil
}

func (r *fileRuntime) Update(ctx context.Context, req memprovider.UpdateRequest) (memorydomain.Item, error) {
	memoryID := strings.TrimSpace(req.MemoryID)
	if memoryID == "" {
		return memorydomain.Item{}, errors.New("memory_id is required")
	}
	botID := runtimeBotIDFromMemoryID(memoryID)
	if botID == "" {
		return memorydomain.Item{}, errors.New("invalid memory_id")
	}
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memorydomain.Item{}, err
	}
	var existing *memorydomain.Item
	for i := range items {
		if strings.TrimSpace(items[i].ID) == memoryID {
			item := items[i]
			existing = &item
			break
		}
	}
	if existing == nil {
		return memorydomain.Item{}, errors.New("memory not found")
	}
	text := strings.TrimSpace(req.Memory)
	if text == "" {
		return memorydomain.Item{}, errors.New("memory is required")
	}
	if err := r.store.RemoveMemories(ctx, botID, []string{memoryID}); err != nil {
		return memorydomain.Item{}, err
	}
	existing.Memory = text
	existing.Hash = runtimeHash(text)
	existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	itemsToPersist := []memorydomain.Item{*existing}
	if err := r.store.PersistMemories(ctx, botID, itemsToPersist, nil); err != nil {
		return memorydomain.Item{}, err
	}
	item := memoryItemFromStore(*existing)
	item.BotID = botID
	return item, nil
}

func (r *fileRuntime) Delete(ctx context.Context, memoryID string) (memprovider.DeleteResponse, error) {
	return r.DeleteBatch(ctx, []string{memoryID})
}

func (r *fileRuntime) DeleteBatch(ctx context.Context, memoryIDs []string) (memprovider.DeleteResponse, error) {
	grouped := map[string][]string{}
	for _, id := range memoryIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		botID := runtimeBotIDFromMemoryID(id)
		if botID == "" {
			continue
		}
		grouped[botID] = append(grouped[botID], id)
	}
	for botID, ids := range grouped {
		if err := r.store.RemoveMemories(ctx, botID, ids); err != nil {
			return memprovider.DeleteResponse{}, err
		}
	}
	return memprovider.DeleteResponse{Message: "Memories deleted successfully!"}, nil
}

func (r *fileRuntime) DeleteAll(ctx context.Context, req memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return memprovider.DeleteResponse{}, err
	}
	if err := r.store.RemoveAllMemories(ctx, botID); err != nil {
		return memprovider.DeleteResponse{}, err
	}
	return memprovider.DeleteResponse{Message: "All memories deleted successfully!"}, nil
}

func (*fileRuntime) Compact(_ context.Context, _ map[string]any, _ float64, _ int) (memprovider.CompactResult, error) {
	return memprovider.CompactResult{}, errors.New("file runtime compact is disabled; use graph runtime")
}

func (r *fileRuntime) Usage(ctx context.Context, filters map[string]any) (memprovider.UsageResponse, error) {
	botID, err := runtimeBotID("", filters)
	if err != nil {
		return memprovider.UsageResponse{}, err
	}
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.UsageResponse{}, err
	}
	var usage memprovider.UsageResponse
	usage.Count = len(items)
	for _, item := range items {
		usage.TotalTextBytes += int64(len(item.Memory))
	}
	if usage.Count > 0 {
		usage.AvgTextBytes = usage.TotalTextBytes / int64(usage.Count)
	}
	usage.EstimatedStorageBytes = usage.TotalTextBytes
	return usage, nil
}

// Mode returns the internal identifier for the file runtime. It is used as a
// fallback component (graphRuntime degrades to it when the wiki store is
// unavailable) and as the __builtin_default__ provider when no DB is wired.
// It is no longer a user-selectable memory_mode.
func (*fileRuntime) Mode() string {
	return "file"
}

func (r *fileRuntime) Status(ctx context.Context, botID string) (memprovider.MemoryStatusResponse, error) {
	fileCount, err := r.store.CountMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.MemoryStatusResponse{}, err
	}
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.MemoryStatusResponse{}, err
	}
	return memprovider.MemoryStatusResponse{
		ProviderType:      BuiltinType,
		MemoryMode:        "file",
		CanManualSync:     false,
		SourceDir:         path.Join(runtimedomain.DefaultDataMount, "memory"),
		OverviewPath:      path.Join(runtimedomain.DefaultDataMount, "MEMORY.md"),
		MarkdownFileCount: fileCount,
		SourceCount:       len(items),
	}, nil
}

func (r *fileRuntime) Rebuild(ctx context.Context, botID string) (memprovider.RebuildResult, error) {
	items, err := r.store.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return memprovider.RebuildResult{}, err
	}
	if err := r.store.SyncOverview(ctx, botID); err != nil {
		return memprovider.RebuildResult{}, err
	}
	return memprovider.RebuildResult{
		FsCount:      len(items),
		StorageCount: len(items),
	}, nil
}

// fileRuntimeScore scores a candidate memory body against a query. It delegates
// to segment.LexicalScore so CJK text is segmented via gse (Chinese has no
// inter-word spaces, so whitespace-only splitting collapsed a whole sentence
// into one token that never matched). The graph cache reuses this scorer via
// graphLexicalScore; both now share the same CJK-aware implementation.
func fileRuntimeScore(query, memory string) float64 {
	return memseg.LexicalScore(query, memory)
}

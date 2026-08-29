//nolint:sloglint // graph runtime ops logs use inline key/value pairs for readability
package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/config"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
	"github.com/felinics/memoh/internal/memory/wikistore"
)

// ModeGraph is the memory mode identifier for the graph runtime. It is the
// only supported mode: PG memory nodes/edges are the source of truth, with
// Markdown as a derived agent-facing view.
const ModeGraph = "graph"

// graphRuntime implements Runtime over the PostgreSQL wiki graph. The wiki
// store (wikistore.Store) is authoritative; the filesystem store (memoryStore)
// holds the derived Markdown view the agent reads.
type graphRuntime struct {
	store    wikistore.Store
	fs       memoryStore
	cache    *graphCache
	syncer   *graphSync
	semantic *pgvectorIndex
	retry    *semanticRetryQueue
	logger   *slog.Logger
}

// NewGraphRuntime constructs a graphRuntime. wikiStore is required; fs is the
// derived-view filesystem store (may be nil in tests, which disables sync).
func NewGraphRuntime(logger *slog.Logger, wikiStore wikistore.Store, fs memoryStore) *graphRuntime {
	if logger == nil {
		logger = slog.Default()
	}
	return &graphRuntime{
		store:  wikiStore,
		fs:     fs,
		cache:  newGraphCache(),
		syncer: newGraphSync(fs, logger),
		retry:  newSemanticRetryQueue(logger),
		logger: logger.With("runtime", "graph"),
	}
}

// SetSemanticIndex wires an optional Postgres pgvector seed index. It never
// owns the memory source of truth; failures only degrade to graph lexical recall.
func (r *graphRuntime) SetSemanticIndex(ctx context.Context, index *pgvectorIndex) {
	if index == nil {
		return
	}
	r.semantic = index
	// Failed upserts go to a small per-runtime retry queue; the background
	// loop drains it so the seed index converges without failing writes.
	r.retry.start(ctx, index)
}

func (*graphRuntime) Mode() string { return string(ModeGraph) }

func (r *graphRuntime) Close() error {
	if r == nil {
		return nil
	}
	if r.retry != nil {
		r.retry.stop()
	}
	return nil
}

func (r *graphRuntime) MemoryVersion(_ context.Context, botID string) string {
	if r == nil || r.cache == nil {
		return ""
	}
	return r.cache.version(strings.TrimSpace(botID))
}

// ---- helpers ----

func (r *graphRuntime) syncAndInvalidate(ctx context.Context, botID string) {
	// Regenerate the derived Markdown view from the authoritative PG nodes.
	// Best-effort: PG is the source of truth, so a sync failure is logged, not
	// propagated to the caller.
	if r.syncer == nil || r.fs == nil {
		r.cache.invalidate(botID)
		return
	}
	nodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		r.logger.Warn("graph: list nodes for sync failed", "bot_id", botID, "err", err)
	} else if err := r.syncer.syncMarkdownFromNodes(ctx, botID, nodes); err != nil {
		r.logger.Warn("graph: markdown sync failed", "bot_id", botID, "err", err)
	}
	if _, err := r.store.RebuildDerivedEdges(ctx, botID); err != nil {
		r.logger.Debug("graph: rebuild derived edges failed", "bot_id", botID, "err", err)
	}
	r.cache.invalidate(botID)
}

func (r *graphRuntime) semanticUpsertBestEffort(botID string, n migrate.NodeSpec) {
	if r.semantic == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), semanticEmbedTimeout)
	defer cancel()
	if err := r.semantic.Upsert(ctx, botID, n.ID, n.Body, n.Hash); err != nil {
		r.logger.Debug("graph: pgvector upsert failed; queued for retry", "bot_id", botID, "node_id", n.ID, "err", err)
		r.retry.enqueue(semanticRetryEntry{botID: botID, nodeID: n.ID, body: n.Body, hash: n.Hash})
		return
	}
	// A fresh successful write supersedes any stale pending retry for the node.
	r.retry.discard(botID, []string{n.ID})
}

// ---- Runtime: CRUD ----

func (r *graphRuntime) Add(ctx context.Context, req adapters.AddRequest) (adapters.SearchResponse, error) {
	if r.store == nil {
		return adapters.SearchResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return adapters.SearchResponse{}, err
	}
	text := runtimeText(req.Message, req.Messages)
	if text == "" {
		return adapters.SearchResponse{}, errors.New("graph runtime: message is required")
	}
	now := time.Now().UTC()
	spec := memoryItemToNodeSpec(adapters.MemoryItem{
		ID:               runtimeMemoryID(botID, now),
		Memory:           text,
		Hash:             runtimeHash(text),
		CreatedAt:        now.Format(time.RFC3339),
		UpdatedAt:        now.Format(time.RFC3339),
		Metadata:         req.Metadata,
		SourceMessageIDs: adapters.NormalizeSourceRefs(req.SourceMessageIDs),
	}, botID)

	saved, err := r.store.UpsertNode(ctx, spec)
	if err != nil {
		return adapters.SearchResponse{}, fmt.Errorf("graph runtime: upsert node: %w", err)
	}
	r.semanticUpsertBestEffort(botID, saved) //nolint:contextcheck // async semantic upsert uses its own bounded context
	r.syncAndInvalidate(ctx, botID)
	return adapters.SearchResponse{Results: []adapters.MemoryItem{nodeSpecToMemoryItem(saved)}}, nil
}

func (r *graphRuntime) Search(ctx context.Context, req adapters.SearchRequest) (adapters.SearchResponse, error) {
	if r.store == nil {
		return adapters.SearchResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return adapters.SearchResponse{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	// Primary path: graph seed-then-expand over the cached PG graph.
	resp, graphErr := r.searchGraph(ctx, botID, req.Query, limit)
	if graphErr == nil {
		return resp, nil
	}

	// Reliability fallback: degrade to file-lexical over the derived Markdown.
	r.logger.Warn("graph search failed, falling back to file lexical", "bot_id", botID, "err", graphErr)
	fallback, err := r.searchFileFallback(ctx, botID, req.Query, limit)
	if fallback.FallbackReason == "" {
		fallback.FallbackReason = "graph_error"
	}
	return fallback, err
}

// searchGraph runs seed-then-expand: lexical-score nodes -> top-K seeds ->
// BFS expand along edges -> merge -> populate Relations.
func (r *graphRuntime) searchGraph(ctx context.Context, botID, query string, limit int) (adapters.SearchResponse, error) {
	graph, err := r.cache.getOrBuild(ctx, botID, r.store)
	if err != nil {
		return adapters.SearchResponse{}, err
	}
	nodes := graph.nodeSlice()

	overfetch := limit * 3
	if overfetch < 10 {
		overfetch = 10
	}

	// 1. Seed: pgvector semantic seeds when configured, plus lexical seeds.
	type seed struct {
		id    string
		score float64
	}
	seedScores := map[string]float64{}
	if r.semantic != nil {
		if semanticSeeds, semanticErr := r.semantic.SearchSeeds(ctx, botID, query, overfetch); semanticErr != nil {
			r.logger.Debug("graph: pgvector seed search failed, using lexical seeds", "bot_id", botID, "err", semanticErr)
		} else {
			for id, score := range semanticSeeds {
				if _, ok := graph.nodes[id]; ok {
					seedScores[id] = max64(seedScores[id], score)
				}
			}
		}
	}
	for _, n := range nodes {
		s := graphLexicalScore(query, n.Body)
		if s <= 0 && strings.TrimSpace(query) != "" {
			continue
		}
		seedScores[n.ID] = max64(seedScores[n.ID], s)
	}
	seeds := make([]seed, 0, len(seedScores))
	for id, score := range seedScores {
		seeds = append(seeds, seed{id: id, score: score})
	}
	sort.Slice(seeds, func(i, j int) bool { return seeds[i].score > seeds[j].score })
	if len(seeds) > overfetch {
		seeds = seeds[:overfetch]
	}

	// 2. Expand: BFS depth 2 from seeds, weighting neighbors.
	const decay = 0.6
	type hit struct {
		score float64
	}
	scores := map[string]hit{}
	hitEdges := map[string]bool{}
	addEdge := func(a, b, rel string) {
		src, dst := a, b
		if dst < src {
			src, dst = dst, src
		}
		hitEdges[src+"\x00"+dst+"\x00"+rel] = true
	}
	for _, s := range seeds {
		scores[s.id] = hit{score: max64(scores[s.id].score, s.score)}
		// depth 1
		for _, nb := range graph.neighbors(s.id) {
			ns := float64(s.score) * float64(nb.weight) * decay
			scores[nb.node.ID] = hit{score: max64(scores[nb.node.ID].score, ns)}
			addEdge(s.id, nb.node.ID, string(nb.rel))
			// depth 2
			for _, nb2 := range graph.neighbors(nb.node.ID) {
				if nb2.node.ID == s.id {
					continue
				}
				ns2 := ns * decay
				scores[nb2.node.ID] = hit{score: max64(scores[nb2.node.ID].score, ns2)}
				addEdge(nb.node.ID, nb2.node.ID, string(nb2.rel))
			}
		}
	}

	// 3. Merge + sort + truncate.
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return scores[ids[i]].score > scores[ids[j]].score })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	results := make([]adapters.MemoryItem, 0, len(ids))
	for _, id := range ids {
		n, ok := graph.nodes[id]
		if !ok {
			continue
		}
		item := nodeSpecToMemoryItem(n)
		item.Score = scores[id].score
		results = append(results, item)
	}

	// 4. Relations (hit edges) for explain.
	relations := make([]any, 0, len(hitEdges))
	for k := range hitEdges {
		parts := strings.SplitN(k, "\x00", 3)
		if len(parts) == 3 {
			relations = append(relations, map[string]any{"from": parts[0], "to": parts[1], "rel": parts[2]})
		}
	}
	return adapters.SearchResponse{Results: results, Relations: relations, RetrievalMode: "graph"}, nil
}

// searchFileFallback is the reliability fallback: read the derived Markdown via
// the bridge and score lexically, exactly like fileRuntime. Used when the PG
// graph is unavailable.
func (r *graphRuntime) searchFileFallback(ctx context.Context, botID, query string, limit int) (adapters.SearchResponse, error) {
	if r.fs == nil {
		return adapters.SearchResponse{}, nil
	}
	items, err := r.fs.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return adapters.SearchResponse{}, fmt.Errorf("graph runtime: file fallback read: %w", err)
	}
	q := strings.ToLower(strings.TrimSpace(query))
	results := make([]adapters.MemoryItem, 0, len(items))
	for _, it := range items {
		score := graphLexicalScore(q, it.Memory)
		if q != "" && score <= 0 {
			continue
		}
		m := memoryItemFromStore(it)
		m.BotID = botID
		m.Score = score
		results = append(results, m)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].UpdatedAt > results[j].UpdatedAt
		}
		return results[i].Score > results[j].Score
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return adapters.SearchResponse{Results: results, RetrievalMode: "file_fallback"}, nil
}

func (r *graphRuntime) GetAll(ctx context.Context, req adapters.GetAllRequest) (adapters.SearchResponse, error) {
	if r.store == nil {
		return adapters.SearchResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return adapters.SearchResponse{}, err
	}
	nodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		// Fallback to derived files if the store is unavailable.
		r.logger.Warn("graph GetAll failed, falling back to files", "bot_id", botID, "err", err)
		fallback, fallbackErr := r.searchFileFallback(ctx, botID, "", req.Limit)
		if fallback.FallbackReason == "" {
			fallback.FallbackReason = "graph_error"
		}
		return fallback, fallbackErr
	}
	out := make([]adapters.MemoryItem, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeSpecToMemoryItem(n))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return adapters.SearchResponse{Results: out, RetrievalMode: "graph"}, nil
}

func (r *graphRuntime) Update(ctx context.Context, req adapters.UpdateRequest) (adapters.MemoryItem, error) {
	if r.store == nil {
		return adapters.MemoryItem{}, errors.New("graph runtime: wiki store not configured")
	}
	memoryID := strings.TrimSpace(req.MemoryID)
	if memoryID == "" {
		return adapters.MemoryItem{}, errors.New("graph runtime: memory_id is required")
	}
	text := strings.TrimSpace(req.Memory)
	if text == "" {
		return adapters.MemoryItem{}, errors.New("graph runtime: memory is required")
	}
	botID := runtimeBotIDFromMemoryID(memoryID)
	if botID == "" {
		return adapters.MemoryItem{}, errors.New("graph runtime: invalid memory_id")
	}
	existing, storedID, err := r.resolveNodeByMemoryID(ctx, botID, memoryID)
	if err != nil {
		return adapters.MemoryItem{}, fmt.Errorf("graph runtime: get node: %w", err)
	}
	existing.ID = memoryID
	existing.Body = text
	existing.Hash = runtimeHash(text)
	existing.SourceMessageIDs = adapters.MergeSourceRefs(existing.SourceMessageIDs, req.SourceMessageIDs)
	saved, err := r.store.UpsertNode(ctx, existing)
	if err != nil {
		return adapters.MemoryItem{}, fmt.Errorf("graph runtime: update node: %w", err)
	}
	if storedID != memoryID {
		if err := r.store.DeleteNode(ctx, botID, storedID); err != nil {
			return adapters.MemoryItem{}, fmt.Errorf("graph runtime: delete legacy node: %w", err)
		}
		if r.semantic != nil {
			r.discardSemanticNodes(ctx, botID, []string{storedID})
		}
	}
	r.semanticUpsertBestEffort(botID, saved) //nolint:contextcheck // async semantic upsert uses its own bounded context
	r.syncAndInvalidate(ctx, botID)
	return nodeSpecToMemoryItem(saved), nil
}

func (r *graphRuntime) Delete(ctx context.Context, memoryID string) (adapters.DeleteResponse, error) {
	return r.DeleteBatch(ctx, []string{memoryID})
}

func (r *graphRuntime) DeleteBatch(ctx context.Context, memoryIDs []string) (adapters.DeleteResponse, error) {
	if r.store == nil {
		return adapters.DeleteResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	seen := map[string]bool{}
	deletedByBot := map[string][]string{}
	for _, rawID := range memoryIDs {
		memoryID := strings.TrimSpace(rawID)
		if memoryID == "" || seen[memoryID] {
			continue
		}
		seen[memoryID] = true
		botID := runtimeBotIDFromMemoryID(memoryID)
		if botID == "" {
			continue
		}
		_, storedID, err := r.resolveNodeByMemoryID(ctx, botID, memoryID)
		if err != nil {
			if !errors.Is(err, wikistore.ErrNodeNotFound) {
				return adapters.DeleteResponse{}, fmt.Errorf("graph runtime: get node for delete: %w", err)
			}
			storedID = memoryID
		}
		if err := r.store.DeleteNode(ctx, botID, storedID); err != nil {
			return adapters.DeleteResponse{}, fmt.Errorf("graph runtime: delete node: %w", err)
		}
		deletedByBot[botID] = append(deletedByBot[botID], storedID)
		if storedID != memoryID {
			deletedByBot[botID] = append(deletedByBot[botID], memoryID)
		}
		r.syncAndInvalidate(ctx, botID)
	}
	for botID, ids := range deletedByBot {
		r.retry.discard(botID, ids)
	}
	if r.semantic != nil {
		semanticCtx, cancel := context.WithTimeout(ctx, semanticEmbedTimeout)
		defer cancel()
		for botID, ids := range deletedByBot {
			if err := r.semantic.DeleteNodes(semanticCtx, botID, ids); err != nil {
				r.logger.Debug("graph: pgvector delete failed", "bot_id", botID, "err", err)
			}
		}
	}
	return adapters.DeleteResponse{Message: "Memories deleted successfully!"}, nil
}

func (r *graphRuntime) resolveNodeByMemoryID(ctx context.Context, botID, memoryID string) (migrate.NodeSpec, string, error) {
	memoryID = strings.TrimSpace(memoryID)
	node, err := r.store.GetNode(ctx, botID, memoryID)
	if err == nil {
		return node, memoryID, nil
	}
	if !errors.Is(err, wikistore.ErrNodeNotFound) {
		return migrate.NodeSpec{}, "", err
	}
	legacyID := runtimeLocalMemoryID(memoryID)
	if legacyID == "" || legacyID == memoryID {
		return migrate.NodeSpec{}, "", err
	}
	node, legacyErr := r.store.GetNode(ctx, botID, legacyID)
	if legacyErr != nil {
		if errors.Is(legacyErr, wikistore.ErrNodeNotFound) {
			return migrate.NodeSpec{}, "", err
		}
		return migrate.NodeSpec{}, "", legacyErr
	}
	return node, legacyID, nil
}

func (r *graphRuntime) discardSemanticNodes(ctx context.Context, botID string, nodeIDs []string) {
	if r.semantic == nil || len(nodeIDs) == 0 {
		return
	}
	semanticCtx, cancel := context.WithTimeout(ctx, semanticEmbedTimeout)
	defer cancel()
	if err := r.semantic.DeleteNodes(semanticCtx, botID, nodeIDs); err != nil {
		r.logger.Debug("graph: pgvector delete failed", "bot_id", botID, "err", err)
	}
}

func (r *graphRuntime) DeleteAll(ctx context.Context, req adapters.DeleteAllRequest) (adapters.DeleteResponse, error) {
	if r.store == nil {
		return adapters.DeleteResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID(req.BotID, req.Filters)
	if err != nil {
		return adapters.DeleteResponse{}, err
	}
	if err := r.store.DeleteAllNodes(ctx, botID); err != nil {
		return adapters.DeleteResponse{}, fmt.Errorf("graph runtime: delete all nodes: %w", err)
	}
	if r.fs != nil {
		if err := r.fs.RemoveAllMemories(ctx, botID); err != nil {
			r.logger.Warn("graph: remove derived markdown failed", "bot_id", botID, "err", err)
		}
	}
	r.retry.discardBot(botID)
	if r.semantic != nil {
		auxCtx, cancel := context.WithTimeout(ctx, semanticEmbedTimeout)
		defer cancel()
		if err := r.semantic.DeleteBot(auxCtx, botID); err != nil {
			r.logger.Debug("graph: pgvector delete bot failed", "bot_id", botID, "err", err)
		}
	}
	r.cache.invalidate(botID)
	return adapters.DeleteResponse{Message: "All memories deleted successfully!"}, nil
}

// ---- Runtime: usage / status / rebuild ----

func (r *graphRuntime) Usage(ctx context.Context, filters map[string]any) (adapters.UsageResponse, error) {
	if r.store == nil {
		return adapters.UsageResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	botID, err := runtimeBotID("", filters)
	if err != nil {
		return adapters.UsageResponse{}, err
	}
	nodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		return adapters.UsageResponse{}, fmt.Errorf("graph runtime: usage list: %w", err)
	}
	var usage adapters.UsageResponse
	usage.Count = len(nodes)
	for _, n := range nodes {
		usage.TotalTextBytes += int64(len(n.Body))
	}
	if usage.Count > 0 {
		usage.AvgTextBytes = usage.TotalTextBytes / int64(usage.Count)
	}
	usage.EstimatedStorageBytes = usage.TotalTextBytes
	return usage, nil
}

func (r *graphRuntime) Status(ctx context.Context, botID string) (adapters.MemoryStatusResponse, error) {
	if r.store == nil {
		return adapters.MemoryStatusResponse{}, errors.New("graph runtime: wiki store not configured")
	}
	nodeCount, _ := r.store.CountNodes(ctx, botID)
	edgeCount, _ := r.store.CountEdges(ctx, botID)
	resp := adapters.MemoryStatusResponse{
		ProviderType:  BuiltinType,
		MemoryMode:    string(ModeGraph),
		CanManualSync: true,
		SourceDir:     path.Join(config.DefaultDataMount, "memory"),
		OverviewPath:  path.Join(config.DefaultDataMount, "MEMORY.md"),
		SourceCount:   nodeCount,
		EdgeCount:     edgeCount,
		IndexedCount:  0,
	}
	if r.semantic != nil {
		resp.VectorIndex = r.semantic.Name()
		if err := r.semantic.Health(ctx); err != nil {
			resp.Pgvector = &adapters.HealthStatus{Error: err.Error()}
		} else {
			resp.Pgvector = &adapters.HealthStatus{OK: true}
			if count, err := r.semantic.Count(ctx, botID); err == nil {
				resp.IndexedCount = count
			}
		}
		resp.RetryQueueDepth = r.retry.depth(botID)
		resp.Degraded = resp.RetryQueueDepth > 0 || resp.Pgvector == nil || !resp.Pgvector.OK
	}
	if r.fs != nil {
		if fc, err := r.fs.CountMemoryFiles(ctx, botID); err == nil {
			resp.MarkdownFileCount = fc
		}
	}
	return resp, nil
}

func (r *graphRuntime) Rebuild(ctx context.Context, botID string) (adapters.RebuildResult, error) {
	if r.store == nil {
		return adapters.RebuildResult{}, errors.New("graph runtime: wiki store not configured")
	}
	// Ingest agent-authored /data/memory/*.md BEFORE the destructive
	// derived-view rebuild so their content becomes DB nodes and survives the
	// regeneration. Without this, a rebuild would silently drop any file the
	// agent wrote directly that has not yet been ingested.
	if r.fs != nil {
		if _, err := r.IngestMarkdownFiles(ctx, botID); err != nil {
			r.logger.Warn("graph: rebuild ingest failed", "bot_id", botID, "err", err)
		}
	}
	nodes, err := r.store.ListNodes(ctx, botID)
	if err != nil {
		return adapters.RebuildResult{}, fmt.Errorf("graph runtime: rebuild list: %w", err)
	}
	r.syncAndInvalidate(ctx, botID)
	count, _ := r.store.CountNodes(ctx, botID)
	result := adapters.RebuildResult{FsCount: len(nodes), StorageCount: count}
	if r.semantic != nil {
		r.retry.discardBot(botID)
		if err := r.semantic.DeleteBot(ctx, botID); err != nil {
			r.logger.Debug("graph: pgvector rebuild clear failed", "bot_id", botID, "err", err)
		}
		for _, node := range nodes {
			r.semanticUpsertBestEffort(botID, node) //nolint:contextcheck // async semantic upsert uses its own bounded context
		}
		if indexed, err := r.semantic.Count(ctx, botID); err == nil {
			result.StorageCount = indexed
		}
	}
	return result, nil
}

// ---- node/memory item conversion ----

// memoryItemToNodeSpec derives a NodeSpec from a MemoryItem, classifying the
// layer from metadata (defaulting to note) and pulling profile_ref/topic out.
func memoryItemToNodeSpec(item adapters.MemoryItem, botID string) migrate.NodeSpec {
	body := strings.TrimSpace(item.Memory)
	layer := migrate.LayerNote
	if raw, ok := item.Metadata["layer"].(string); ok && raw != "" {
		switch migrate.MemoryLayer(strings.ToLower(strings.TrimSpace(raw))) {
		case migrate.LayerPreference, migrate.LayerIdentity, migrate.LayerContext,
			migrate.LayerExperience, migrate.LayerActivity, migrate.LayerPersona, migrate.LayerNote:
			layer = migrate.MemoryLayer(raw)
		}
	}
	profileRef := metadataStringVal(item.Metadata, "profile_ref")
	if profileRef == "" {
		profileRef = metadataStringVal(item.Metadata, "profile_user_id")
	}
	return migrate.NodeSpec{
		ID:               strings.TrimSpace(item.ID),
		BotID:            botID,
		Body:             body,
		Hash:             strings.TrimSpace(item.Hash),
		Layer:            layer,
		Subject:          metadataStringVal(item.Metadata, "subject"),
		Confidence:       metadataFloatVal(item.Metadata, "confidence", 0.5),
		Metadata:         item.Metadata,
		SourceMessageIDs: adapters.NormalizeSourceRefs(item.SourceMessageIDs),
		ProfileRef:       profileRef,
		Topic:            metadataStringVal(item.Metadata, "topic"),
		CapturedAt:       parseGraphTime(item.CreatedAt),
	}
}

func metadataStringVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func metadataFloatVal(m map[string]any, key string, def float32) float32 {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		f := float32(n)
		if f < 0 || f > 1 {
			return def
		}
		return f
	case float32:
		if n < 0 || n > 1 {
			return def
		}
		return n
	}
	return def
}

func parseGraphTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

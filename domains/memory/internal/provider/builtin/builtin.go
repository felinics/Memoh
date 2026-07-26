package builtin

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
)

const (
	BuiltinType = "builtin"

	sharedMemoryNamespace = "bot"
)

// BuiltinProvider wraps the existing Service as a Provider.
type BuiltinProvider struct {
	service Runtime
	llm     memprovider.LLM
	logger  *slog.Logger
	packer  contextPackerConfig
}

// Runtime is the runtime memory backend required by the builtin provider.
// It is intentionally defined as an interface to decouple provider wiring from
// concrete service structs in the memory package.
type Runtime interface {
	Add(ctx context.Context, req memprovider.AddRequest) (memprovider.SearchResponse, error)
	Search(ctx context.Context, req memprovider.SearchRequest) (memprovider.SearchResponse, error)
	GetAll(ctx context.Context, req memprovider.GetAllRequest) (memprovider.SearchResponse, error)
	Update(ctx context.Context, req memprovider.UpdateRequest) (memorydomain.Item, error)
	Delete(ctx context.Context, memoryID string) (memprovider.DeleteResponse, error)
	DeleteBatch(ctx context.Context, memoryIDs []string) (memprovider.DeleteResponse, error)
	DeleteAll(ctx context.Context, req memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error)
	Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (memprovider.CompactResult, error)
	Usage(ctx context.Context, filters map[string]any) (memprovider.UsageResponse, error)
	Mode() string
	Status(ctx context.Context, botID string) (memprovider.MemoryStatusResponse, error)
	Rebuild(ctx context.Context, botID string) (memprovider.RebuildResult, error)
}

type llmCompactRuntime interface {
	CompactWithLLM(ctx context.Context, filters map[string]any, ratio float64, decayDays int, llm memprovider.LLM) (memprovider.CompactResult, error)
}

func NewBuiltinProvider(log *slog.Logger, service Runtime) *BuiltinProvider {
	if log == nil {
		log = slog.Default()
	}
	logger := log.With(slog.String("provider", BuiltinType))
	return &BuiltinProvider{
		service: service,
		logger:  logger,
		packer:  defaultPackerConfig,
	}
}

// SetLLM injects the LLM client used for Extract/Decide in memory formation.
func (p *BuiltinProvider) SetLLM(llm memprovider.LLM) {
	p.llm = llm
}

// Close releases runtime-owned resources such as the semantic retry worker.
// The process-level pgvector Store owns its shared connection pool.
func (p *BuiltinProvider) Close() error {
	if p == nil || p.service == nil {
		return nil
	}
	if closer, ok := p.service.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// SetPackerConfig overrides the default context packing configuration.
// Zero-valued fields fall back to defaults.
func (p *BuiltinProvider) SetPackerConfig(cfg contextPackerConfig) {
	if cfg.TargetItems > 0 {
		p.packer.TargetItems = cfg.TargetItems
	}
	if cfg.MaxTotalChars > 0 {
		p.packer.MaxTotalChars = cfg.MaxTotalChars
	}
	if cfg.MinItemChars > 0 {
		p.packer.MinItemChars = cfg.MinItemChars
	}
	if cfg.MaxItemChars > 0 {
		p.packer.MaxItemChars = cfg.MaxItemChars
	}
	if cfg.OverfetchRatio > 0 {
		p.packer.OverfetchRatio = cfg.OverfetchRatio
	}
}

// ApplyProviderConfig reads context packing knobs from a provider config map
// and applies any non-zero values to the provider's packer configuration.
func (p *BuiltinProvider) ApplyProviderConfig(providerConfig map[string]any) {
	p.SetPackerConfig(contextPackerConfig{
		TargetItems:   intFromConfig(providerConfig, "context_target_items"),
		MaxTotalChars: intFromConfig(providerConfig, "context_max_total_chars"),
	})
}

func intFromConfig(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func (*BuiltinProvider) Type() string { return BuiltinType }

func (p *BuiltinProvider) MemoryVersion(ctx context.Context, botID string) string {
	if p == nil || p.service == nil {
		return ""
	}
	versioned, ok := p.service.(interface {
		MemoryVersion(context.Context, string) string
	})
	if !ok {
		return ""
	}
	return versioned.MemoryVersion(ctx, botID)
}

func (p *BuiltinProvider) SemanticCompactCapability() memprovider.MemoryCompactCapability {
	if p.service == nil {
		return memprovider.MemoryCompactCapability{Reason: "memory runtime not configured"}
	}
	if p.llm == nil {
		return memprovider.MemoryCompactCapability{Reason: "semantic compact requires a configured LLM"}
	}
	if _, ok := p.service.(llmCompactRuntime); !ok {
		return memprovider.MemoryCompactCapability{Reason: "selected memory runtime does not support semantic compact"}
	}
	mode := strings.TrimSpace(p.service.Mode())
	return memprovider.MemoryCompactCapability{
		Semantic:     true,
		Archive:      mode != ModeGraph,
		RebuildIndex: mode == "graph",
	}
}

func memorySourceLabel(item memorydomain.Item) string {
	var parts []string
	if item.Metadata != nil {
		if name, ok := item.Metadata["profile_display_name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				parts = append(parts, name)
			}
		}
	}
	if ts := strings.TrimSpace(item.CreatedAt); ts != "" {
		if len(ts) > 10 {
			ts = ts[:10]
		}
		parts = append(parts, ts)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// --- Conversation Hooks ---

func (p *BuiltinProvider) OnBeforeChat(ctx context.Context, req memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	if p.service == nil {
		return nil, nil
	}
	if strings.TrimSpace(req.Query) == "" || strings.TrimSpace(req.BotID) == "" {
		return nil, nil
	}

	fetchLimit := overfetchLimit(p.packer)
	resp, err := p.service.Search(ctx, memprovider.SearchRequest{
		Query: req.Query,
		BotID: req.BotID,
		Limit: fetchLimit,
		Filters: map[string]any{
			"namespace": sharedMemoryNamespace,
			"scopeId":   req.BotID,
			"bot_id":    req.BotID,
		},
		NoStats: true,
	})
	if err != nil {
		p.logger.WarnContext(ctx, "memory search for context failed", slog.Any("error", err))
		return nil, err
	}

	candidates := deduplicateAndSort(resp.Results)
	if len(candidates) == 0 {
		return nil, nil
	}

	packed := packContext(candidates, p.packer)
	if len(packed.Items) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("<memory-context>\nRelevant memory context (use when helpful):\n")
	for _, entry := range packed.Items {
		sb.WriteString("- ")
		if label := memorySourceLabel(entry.Item); label != "" {
			sb.WriteString("[")
			sb.WriteString(label)
			sb.WriteString("] ")
		}
		sb.WriteString(entry.Snippet)
		sb.WriteString("\n")
	}
	sb.WriteString("</memory-context>")
	payload := strings.TrimSpace(sb.String())
	if payload == "" {
		return nil, nil
	}
	retrievalMode := strings.TrimSpace(resp.RetrievalMode)
	if retrievalMode == "" {
		retrievalMode = strings.TrimSpace(p.service.Mode())
	}
	return &memprovider.BeforeChatResult{
		ContextText:    payload,
		RetrievalMode:  retrievalMode,
		FallbackReason: strings.TrimSpace(resp.FallbackReason),
	}, nil
}

func (p *BuiltinProvider) OnAfterChat(ctx context.Context, req memprovider.AfterChatRequest) error {
	if p.service == nil {
		return nil
	}
	botID := strings.TrimSpace(req.BotID)
	if botID == "" {
		return nil
	}
	if len(req.Messages) == 0 {
		return nil
	}

	if p.llm != nil {
		result := runFormation(ctx, p.logger, p.llm, p.service, req)
		p.logger.DebugContext(ctx, "memory formation completed",
			slog.String("bot_id", botID),
			slog.Int("extracted", result.ExtractedFacts),
			slog.Int("added", result.Added),
			slog.Int("updated", result.Updated),
			slog.Int("deleted", result.Deleted),
			slog.Int("skipped", result.Skipped),
		)
		return nil
	}

	// Fallback: no LLM configured, store raw transcript (legacy path).
	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	metadata := memport.BuildProfileMetadata(req.UserID, req.ChannelIdentityID, req.DisplayName)
	if _, err := p.service.Add(ctx, memprovider.AddRequest{
		Messages: req.Messages,
		BotID:    botID,
		Metadata: metadata,
		Filters:  filters,
	}); err != nil {
		p.logger.WarnContext(ctx, "store memory failed", slog.String("bot_id", botID), slog.Any("error", err))
	}
	return nil
}

// --- CRUD ---

func (p *BuiltinProvider) Add(ctx context.Context, req memprovider.AddRequest) (memprovider.SearchResponse, error) {
	if p.service == nil {
		return memprovider.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Add(ctx, req)
}

func (p *BuiltinProvider) Search(ctx context.Context, req memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	if p.service == nil {
		return memprovider.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Search(ctx, req)
}

func (p *BuiltinProvider) GetAll(ctx context.Context, req memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	if p.service == nil {
		return memprovider.SearchResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.GetAll(ctx, req)
}

func (p *BuiltinProvider) Update(ctx context.Context, req memprovider.UpdateRequest) (memorydomain.Item, error) {
	if p.service == nil {
		return memorydomain.Item{}, errors.New("memory runtime not configured")
	}
	return p.service.Update(ctx, req)
}

func (p *BuiltinProvider) Delete(ctx context.Context, memoryID string) (memprovider.DeleteResponse, error) {
	if p.service == nil {
		return memprovider.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Delete(ctx, memoryID)
}

func (p *BuiltinProvider) DeleteBatch(ctx context.Context, memoryIDs []string) (memprovider.DeleteResponse, error) {
	if p.service == nil {
		return memprovider.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.DeleteBatch(ctx, memoryIDs)
}

func (p *BuiltinProvider) DeleteAll(ctx context.Context, req memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	if p.service == nil {
		return memprovider.DeleteResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.DeleteAll(ctx, req)
}

func (p *BuiltinProvider) Compact(ctx context.Context, filters map[string]any, ratio float64, decayDays int) (memprovider.CompactResult, error) {
	if p.service == nil {
		return memprovider.CompactResult{}, errors.New("memory runtime not configured")
	}
	capability := p.SemanticCompactCapability()
	if !capability.Semantic {
		reason := strings.TrimSpace(capability.Reason)
		if reason == "" {
			reason = "semantic compact is not available"
		}
		return memprovider.CompactResult{}, errors.New(reason)
	}
	return p.service.(llmCompactRuntime).CompactWithLLM(ctx, filters, ratio, decayDays, p.llm)
}

func (p *BuiltinProvider) Usage(ctx context.Context, filters map[string]any) (memprovider.UsageResponse, error) {
	if p.service == nil {
		return memprovider.UsageResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Usage(ctx, filters)
}

func (p *BuiltinProvider) Status(ctx context.Context, botID string) (memprovider.MemoryStatusResponse, error) {
	if p.service == nil {
		return memprovider.MemoryStatusResponse{}, errors.New("memory runtime not configured")
	}
	return p.service.Status(ctx, botID)
}

func (p *BuiltinProvider) Rebuild(ctx context.Context, botID string) (memprovider.RebuildResult, error) {
	if p.service == nil {
		return memprovider.RebuildResult{}, errors.New("memory runtime not configured")
	}
	return p.service.Rebuild(ctx, botID)
}

// markdownIngestor is the optional Runtime capability for ingesting agent-
// authored Markdown files back into the DB as nodes. Only the graph runtime
// implements it (the file runtime treats files as the source of truth).
type markdownIngestor interface {
	IngestMarkdownFiles(ctx context.Context, botID string) (IngestResult, error)
}

// IngestFromMarkdown implements memprovider.MarkdownIngestProvider. It imports
// /data/memory/*.md into the wiki store so agent-authored files become
// searchable DB nodes (and survive the next derived-view rebuild).
func (p *BuiltinProvider) IngestFromMarkdown(ctx context.Context, botID string) (memprovider.IngestResult, error) {
	if p.service == nil {
		return memprovider.IngestResult{}, errors.New("memory runtime not configured")
	}
	ing, ok := p.service.(markdownIngestor)
	if !ok {
		return memprovider.IngestResult{}, errors.New("selected memory runtime does not support markdown ingest")
	}
	res, err := ing.IngestMarkdownFiles(ctx, botID)
	if err != nil {
		return memprovider.IngestResult{}, err
	}
	return memprovider.IngestResult{Ingested: res.Ingested, Skipped: res.Skipped}, nil
}

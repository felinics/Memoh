package tool

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memprovider "github.com/memohai/memoh/domains/memory/registry"
)

const (
	sharedMemoryNamespace  = "bot"
	defaultMemoryToolLimit = 8
	maxMemoryToolLimit     = 50
)

type MemoryProvider struct {
	registry *memprovider.Registry
	settings BotSettingsReader
	logger   *slog.Logger
}

func NewMemoryProvider(log *slog.Logger, registry *memprovider.Registry, settingsSvc BotSettingsReader) *MemoryProvider {
	if log == nil {
		log = slog.Default()
	}
	return &MemoryProvider{
		registry: registry,
		settings: settingsSvc,
		logger:   log.With(slog.String("tool", "memory")),
	}
}

func (*MemoryProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	ref, ok := available.Ref(ToolSearchMemory())
	if !ok {
		return ""
	}
	return usageSection("Long-term memory", []string{
		"Use " + ref + " to recall durable user preferences, prior conversations, project context, and other long-term facts beyond the current context window.",
		"When retrieved memory conflicts with the latest user message or visible context, treat the latest user message and current context as authoritative.",
	})
}

func (p *MemoryProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	provider := p.resolveProvider(ctx, session.BotID)
	if provider == nil {
		return nil, nil
	}
	return []sdk.Tool{{
		Name:        ToolSearchMemory().String(),
		Description: "Search for memories relevant to the current chat",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The query to search memories",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of memory results",
				},
			},
			"required": []string{"query"},
		},
		Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
			return p.search(ctx.Context, provider, session.BotID, inputAsMap(input))
		},
	}}, nil
}

func (p *MemoryProvider) search(ctx context.Context, provider memprovider.Instance, botID string, arguments map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(arguments, "query"))
	if query == "" {
		return map[string]any{"ok": false, "error": "query is required"}, nil
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return map[string]any{"ok": false, "error": "bot_id is required"}, nil
	}
	limit := defaultMemoryToolLimit
	if v, ok := arguments["limit"]; ok && v != nil {
		switch n := v.(type) {
		case float64:
			limit = int(n)
		case int:
			limit = n
		case json.Number:
			if parsed, err := n.Int64(); err == nil {
				limit = int(parsed)
			}
		}
	}
	if limit <= 0 {
		limit = defaultMemoryToolLimit
	}
	if limit > maxMemoryToolLimit {
		limit = maxMemoryToolLimit
	}

	resp, err := provider.Search(ctx, memprovider.SearchRequest{
		Query: query,
		BotID: botID,
		Limit: limit,
		Filters: map[string]any{
			"namespace": sharedMemoryNamespace,
			"scopeId":   botID,
			"bot_id":    botID,
		},
		NoStats: true,
	})
	if err != nil {
		p.logger.WarnContext(ctx, "memory search tool failed", slog.Any("error", err))
		return map[string]any{"ok": false, "error": "memory search failed"}, nil
	}

	allResults := dedupeMemoryItems(resp.Results)
	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})
	if len(allResults) > limit {
		allResults = allResults[:limit]
	}
	results := make([]map[string]any, 0, len(allResults))
	for _, item := range allResults {
		results = append(results, map[string]any{
			"id":     item.ID,
			"memory": item.Memory,
			"score":  item.Score,
		})
	}
	return map[string]any{
		"query":   query,
		"total":   len(results),
		"results": results,
	}, nil
}

func (p *MemoryProvider) resolveProvider(ctx context.Context, botID string) memprovider.Instance {
	if p.registry == nil || p.settings == nil {
		return nil
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil
	}
	providerID := strings.TrimSpace(botSettings.MemoryProviderID)
	if providerID == "" {
		return nil
	}
	prov, err := p.registry.Get(ctx, providerID)
	if err != nil {
		return nil
	}
	return prov
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func dedupeMemoryItems(items []memorydomain.Item) []memorydomain.Item {
	if len(items) == 0 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]memorydomain.Item, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Memory)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, item)
	}
	return out
}

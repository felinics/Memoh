package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

const maxVisibleMemorySourceRefs = memprovider.MaxSourceRefsPerToolResult

// MemorySettingsReader returns bot settings for memory provider resolution.
type MemorySettingsReader interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
}

type MemoryProvider struct {
	registry *memprovider.Registry
	settings MemorySettingsReader
	sessions SessionLister
	logger   *slog.Logger
}

func NewMemoryProvider(log *slog.Logger, registry *memprovider.Registry, settingsSvc MemorySettingsReader, sessions SessionLister) *MemoryProvider {
	if log == nil {
		log = slog.Default()
	}
	return &MemoryProvider{
		registry: registry,
		settings: settingsSvc,
		sessions: sessions,
		logger:   log.With(slog.String("tool", "memory")),
	}
}

func (*MemoryProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	ref, ok := available.Ref(ToolSearchMemory())
	if !ok {
		return ""
	}
	parts := []string{
		"Use " + ref + " to recall durable user preferences, prior conversations, project context, and other long-term facts beyond the current context window.",
		"When retrieved memory conflicts with the latest user message or visible context, treat the latest user message and current context as authoritative.",
	}
	if historyRef, historyOK := available.Ref(ToolGetMessages()); historyOK {
		parts = append(parts, "When "+ref+" returns `source_refs`, verify exact supporting messages with "+historyRef+" by passing both `session_id` and `message_id` from a ref.")
	}
	return usageSection("Long-term memory", parts)
}

func (p *MemoryProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	provider := p.resolveProvider(ctx, session.BotID)
	if provider == nil {
		return nil, nil
	}
	mcpSession := toMCPSession(session)
	descriptors, err := provider.ListTools(ctx, mcpSession)
	if err != nil {
		return nil, nil
	}
	var tools []sdk.Tool
	for _, desc := range descriptors {
		desc := desc
		prov := provider
		sess := mcpSession
		tools = append(tools, sdk.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			Parameters:  desc.InputSchema,
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				result, err := prov.CallTool(ctx.Context, sess, desc.Name, args)
				if err != nil {
					return nil, err
				}
				output := normalizeToolResult(result)
				if desc.Name == ToolSearchMemory().String() {
					output = p.filterSourceRefs(ctx.Context, session, output)
				}
				return output, nil
			},
		})
	}
	return tools, nil
}

func (p *MemoryProvider) filterSourceRefs(ctx context.Context, session SessionContext, output any) any {
	_, allowed, err := visibleHistorySessions(ctx, p.sessions, session)
	if err != nil {
		p.logger.Warn("memory source ref scope lookup failed", slog.Any("error", err))
	}
	return filterSourceRefsValue(output, allowed)
}

func filterSourceRefsValue(value any, allowed map[string]struct{}) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "source_refs" {
				if filtered := visibleSourceRefs(item, allowed); len(filtered) > 0 {
					typed[key] = filtered
				} else {
					delete(typed, key)
				}
				continue
			}
			typed[key] = filterSourceRefsValue(item, allowed)
		}
		return typed
	case []map[string]any:
		for i := range typed {
			typed[i] = filterSourceRefsValue(typed[i], allowed).(map[string]any)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = filterSourceRefsValue(typed[i], allowed)
		}
		return typed
	default:
		return value
	}
}

func visibleSourceRefs(value any, allowed map[string]struct{}) []map[string]any {
	var refs []map[string]any
	switch typed := value.(type) {
	case []map[string]any:
		refs = typed
	case []any:
		refs = make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if ref, ok := item.(map[string]any); ok {
				refs = append(refs, ref)
			}
		}
	default:
		return nil
	}
	filtered := make([]map[string]any, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		sessionID, _ := ref["session_id"].(string)
		messageID, _ := ref["message_id"].(string)
		sessionID = strings.TrimSpace(sessionID)
		messageID = strings.TrimSpace(messageID)
		if !historySessionVisible(allowed, sessionID) || messageID == "" {
			continue
		}
		key := memprovider.EncodeSourceRef(sessionID, messageID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, map[string]any{"session_id": sessionID, "message_id": messageID})
	}
	if len(filtered) > maxVisibleMemorySourceRefs {
		filtered = filtered[len(filtered)-maxVisibleMemorySourceRefs:]
	}
	return filtered
}

func (p *MemoryProvider) resolveProvider(ctx context.Context, botID string) memprovider.Provider {
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

func toMCPSession(s SessionContext) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:                    s.BotID,
		ChatID:                   s.ChatID,
		SessionID:                s.SessionID,
		SessionType:              s.SessionType,
		ChannelIdentityID:        s.ChannelIdentityID,
		SessionToken:             s.SessionToken,
		CurrentPlatform:          s.CurrentPlatform,
		ReplyTarget:              s.ReplyTarget,
		ConversationType:         s.ConversationType,
		IsSubagent:               s.IsSubagent,
		ReasoningStoredEffort:    s.ReasoningStoredEffort,
		ReasoningRequestedEffort: s.ReasoningRequestedEffort,
	}
}

// normalizeToolResult extracts structuredContent from MCP-style results
// so the LLM sees clean data instead of the MCP wrapper.
func normalizeToolResult(result map[string]any) any {
	if result == nil {
		return map[string]any{"ok": true}
	}
	if sc, ok := result["structuredContent"]; ok && sc != nil {
		return sc
	}
	if content, ok := result["content"]; ok {
		if items, ok := content.([]map[string]any); ok && len(items) == 1 {
			if text, ok := items[0]["text"].(string); ok {
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					return parsed
				}
				return text
			}
		}
	}
	return result
}

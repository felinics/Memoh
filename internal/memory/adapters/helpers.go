package adapters

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/memory/migrate"
)

// TruncateSnippet truncates a string to n runes, appending "..." if truncated.
func TruncateSnippet(s string, n int) string {
	trimmed := strings.TrimSpace(s)
	runes := []rune(trimmed)
	if len(runes) <= n {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:n])) + "..."
}

// DeduplicateItems removes duplicate MemoryItems by ID.
func DeduplicateItems(items []MemoryItem) []MemoryItem {
	if len(items) == 0 {
		return items
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]MemoryItem, 0, len(items))
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
		result = append(result, item)
	}
	return result
}

// StringFromConfig extracts a trimmed string value from a config map.
func StringFromConfig(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	v, ok := config[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func MergeMetadata(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// SharedMemoryNamespace is the namespace bot-shared memory lives in. Every
// writer — formation, the management API, and the agent-facing write tools —
// must scope to it, or the write lands somewhere recall never looks.
const SharedMemoryNamespace = "bot"

// BotScopeFilters is the canonical scope for one bot's shared memory.
func BotScopeFilters(botID string) map[string]any {
	return map[string]any{
		"namespace": SharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
}

// MemoryLayers is the layer vocabulary a memory write may declare. It is
// derived from the node vocabulary rather than restated, so a tool schema and
// the store it writes into cannot drift apart.
func MemoryLayers() []string {
	return []string{
		string(migrate.LayerPreference),
		string(migrate.LayerIdentity),
		string(migrate.LayerContext),
		string(migrate.LayerExperience),
		string(migrate.LayerActivity),
		string(migrate.LayerPersona),
		string(migrate.LayerNote),
	}
}

// NormalizeMemoryLayer accepts only the declared vocabulary. An unknown layer
// is dropped rather than rejected, so the node falls back to its own default
// instead of failing a write over a cosmetic field.
func NormalizeMemoryLayer(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, layer := range MemoryLayers() {
		if raw == layer {
			return layer
		}
	}
	return ""
}

func BuildProfileMetadata(userID, channelIdentityID, displayName string) map[string]any {
	userID = strings.TrimSpace(userID)
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	displayName = strings.TrimSpace(displayName)
	if userID == "" && channelIdentityID == "" && displayName == "" {
		return nil
	}
	out := map[string]any{}
	if userID != "" {
		out["profile_user_id"] = userID
		out["profile_ref"] = fmt.Sprintf("user:%s", userID)
	} else if channelIdentityID != "" {
		out["profile_ref"] = fmt.Sprintf("channel_identity:%s", channelIdentityID)
	}
	if channelIdentityID != "" {
		out["profile_channel_identity_id"] = channelIdentityID
	}
	if displayName != "" {
		out["profile_display_name"] = displayName
	}
	return out
}

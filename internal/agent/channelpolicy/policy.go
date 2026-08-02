package channelpolicy

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
)

const (
	TelegramPlatform = "telegram"

	TelegramToolCallsEnabledMetadataKey = "telegram_tool_calls_enabled"
	TelegramEnabledToolsMetadataKey     = "telegram_enabled_tools"
	TelegramSkillsEnabledMetadataKey    = "telegram_skills_enabled"
	TelegramMessageMetadataModeKey      = "telegram_message_metadata_mode"

	MessageMetadataCompact = "compact"
	MessageMetadataFull    = "full"
)

// Policy controls the model-facing projection for one channel. Absence of an
// explicit tool list preserves the legacy behavior where every available tool
// is enabled. An explicitly empty list disables every tool without disabling
// tool-call support on the model itself.
type Policy struct {
	Platform               string
	ToolCallsConfigured    bool
	ToolCallsEnabled       bool
	EnabledToolsConfigured bool
	EnabledTools           []string
	SkillsConfigured       bool
	SkillsEnabled          bool
	MessageMetadataMode    string
}

// Default returns the policy used when a bot has no channel-specific metadata.
func Default(platform string) Policy {
	platform = strings.ToLower(strings.TrimSpace(platform))
	mode := MessageMetadataFull
	if platform == TelegramPlatform {
		mode = MessageMetadataCompact
	}
	return Policy{
		Platform:            platform,
		ToolCallsEnabled:    true,
		SkillsEnabled:       true,
		MessageMetadataMode: mode,
	}
}

// Parse decodes the small Telegram policy stored in bots.metadata. Invalid
// values fail open to the legacy tool set and the compact Telegram projection.
func Parse(platform string, payload []byte) Policy {
	policy := Default(platform)
	if policy.Platform != TelegramPlatform || len(payload) == 0 {
		return policy
	}
	var metadata map[string]any
	if err := json.Unmarshal(payload, &metadata); err != nil || metadata == nil {
		return policy
	}
	if raw, ok := metadata[TelegramEnabledToolsMetadataKey]; ok {
		if items, valid := stringSlice(raw); valid {
			policy.EnabledToolsConfigured = true
			policy.EnabledTools = items
		}
	}
	if enabled, ok := metadata[TelegramToolCallsEnabledMetadataKey].(bool); ok {
		policy.ToolCallsConfigured = true
		policy.ToolCallsEnabled = enabled
	}
	if enabled, ok := metadata[TelegramSkillsEnabledMetadataKey].(bool); ok {
		policy.SkillsConfigured = true
		policy.SkillsEnabled = enabled
	}
	if mode, ok := metadata[TelegramMessageMetadataModeKey].(string); ok {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case MessageMetadataFull:
			policy.MessageMetadataMode = MessageMetadataFull
		case MessageMetadataCompact:
			policy.MessageMetadataMode = MessageMetadataCompact
		}
	}
	return policy
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, true
}

// AllowsTool reports whether a discovered tool remains visible to the model.
func (p Policy) AllowsTool(name string) bool {
	if p.Platform != TelegramPlatform {
		return true
	}
	if !p.ToolCallsAllowed() {
		return false
	}
	name = strings.TrimSpace(name)
	if isSkillTool(name) {
		return p.SkillsAllowed()
	}
	if !p.EnabledToolsConfigured {
		return true
	}
	for _, item := range p.EnabledTools {
		if strings.TrimSpace(item) == name {
			return true
		}
	}
	return false
}

// ToolCacheKey distinguishes explicit allowlists without changing the order in
// which providers expose their schemas to the model.
func (p Policy) ToolCacheKey() string {
	if p.Platform != TelegramPlatform {
		return "all"
	}
	if !p.ToolCallsAllowed() {
		return "off"
	}
	skillKey := "skills:on"
	if !p.SkillsAllowed() {
		skillKey = "skills:off"
	}
	if !p.EnabledToolsConfigured {
		return skillKey + "|all"
	}
	return skillKey + "|allow:" + strings.Join(p.EnabledTools, "\x1f")
}

// ToolCallsAllowed preserves legacy/directly-constructed policies unless the
// Telegram master switch was explicitly configured off.
func (p Policy) ToolCallsAllowed() bool {
	return p.Platform != TelegramPlatform || !p.ToolCallsConfigured || p.ToolCallsEnabled
}

// SkillsAllowed preserves legacy/directly-constructed policies unless the
// Telegram Skills switch was explicitly configured off.
func (p Policy) SkillsAllowed() bool {
	return p.Platform != TelegramPlatform || !p.SkillsConfigured || p.SkillsEnabled
}

func isSkillTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "list_skills", "use_skill":
		return true
	default:
		return false
	}
}

type botMetadataQueries interface {
	GetBotByID(ctx context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error)
}

// Resolver loads a bot's channel policy for tool gateways that do not pass
// through the native application RunConfig (notably ACP runtimes).
type Resolver struct {
	queries botMetadataQueries
}

func NewResolver(queries botMetadataQueries) *Resolver {
	return &Resolver{queries: queries}
}

func (r *Resolver) Resolve(ctx context.Context, botID, platform string) (Policy, error) {
	policy := Default(platform)
	if r == nil || r.queries == nil || policy.Platform != TelegramPlatform {
		return policy, nil
	}
	id, err := db.ParseUUID(botID)
	if err != nil {
		return policy, err
	}
	row, err := r.queries.GetBotByID(ctx, id)
	if err != nil {
		return policy, err
	}
	return Parse(platform, row.Metadata), nil
}

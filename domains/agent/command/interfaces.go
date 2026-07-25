package command

import (
	"context"

	"github.com/memohai/memoh/domains/agent/chat/usage"
	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/setting"
)

// Skill represents a single skill loaded from a bot's container.
type Skill struct {
	Name        string
	Description string
}

// SkillLoader loads skills for a bot.
type SkillLoader interface {
	LoadSkills(ctx context.Context, botID string) ([]Skill, error)
}

// RuntimeSkillLister lists the bot's runtime-usable skills — the same safe
// catalog the Web slash picker shows (unique, enabled, loadable as model
// context). Optional capability of SkillLoader implementations: when present,
// /skill list renders these entries with tap-to-activate buttons instead of
// the raw full listing, so IM and Web expose one identical skill surface.
type RuntimeSkillLister interface {
	ListRuntimeSkills(ctx context.Context, botID string) ([]Skill, error)
}

// FSEntry represents a file or directory in a container filesystem.
type FSEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// ContainerFS provides read-only access to a bot's container filesystem.
type ContainerFS interface {
	ListDir(ctx context.Context, botID, path string) ([]FSEntry, error)
	ReadFile(ctx context.Context, botID, path string) (string, error)
}

var ErrNotFound = usage.ErrNotFound

type (
	CacheStats   = usage.CacheStats
	UsageByDay   = usage.Daily
	UsageByModel = usage.Model
)

// CommandQueries is retained as the composition-root type for slash-command usage reads.
type CommandQueries = usage.CommandReader

// AccessEvaluator checks whether the current channel context may trigger chat.
type AccessEvaluator interface {
	Evaluate(ctx context.Context, req acl.EvaluateRequest) (bool, error)
}

// BotSettings is the narrow settings surface used by slash commands.
type BotSettings interface {
	GetBot(context.Context, string) (setting.Settings, error)
	GetCommandUILanguage(context.Context, string) (string, error)
	UpsertBot(context.Context, string, setting.UpsertRequest) (setting.Settings, error)
}

// BotProfileReader supplies bot ownership for member-role resolution.
type BotProfileReader interface {
	Get(context.Context, string) (bot.Bot, error)
}

package command

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
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

// CommandQueries captures the sqlc methods used by slash commands.
// dbstore.Queries satisfies this interface directly.
type CommandQueries interface {
	GetLatestSessionIDByBot(ctx context.Context, botID pgtype.UUID) (pgtype.UUID, error)
	CountMessagesBySession(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	GetLatestAssistantUsage(ctx context.Context, sessionID pgtype.UUID) (int64, error)
	GetSessionCacheStats(ctx context.Context, sessionID pgtype.UUID) (dbsqlc.GetSessionCacheStatsRow, error)
	GetSessionUsedSkills(ctx context.Context, sessionID pgtype.UUID) ([]string, error)
	GetTokenUsageByDayAndType(ctx context.Context, arg dbsqlc.GetTokenUsageByDayAndTypeParams) ([]dbsqlc.GetTokenUsageByDayAndTypeRow, error)
	GetTokenUsageByModel(ctx context.Context, arg dbsqlc.GetTokenUsageByModelParams) ([]dbsqlc.GetTokenUsageByModelRow, error)
}

// AccessEvaluator checks whether the current channel context may trigger chat.
type AccessEvaluator interface {
	Evaluate(ctx context.Context, req acl.EvaluateRequest) (bool, error)
}

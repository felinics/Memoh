package setting

import (
	"github.com/jackc/pgx/v5/pgxpool"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	settingpostgres "github.com/memohai/memoh/domains/api/internal/postgres/bot/setting"
	"github.com/memohai/memoh/domains/runtime/workspace"
)

// Persistence is the composition-facing settings store surface.
type Persistence interface {
	settingpersistence.Store
	workspace.BotRuntimeSettingsReader
}

// NewPostgresPersistence builds API-owned settings persistence.
func NewPostgresPersistence(pool *pgxpool.Pool) Persistence {
	return settingpostgres.NewStore(pool)
}

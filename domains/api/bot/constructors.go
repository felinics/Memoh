package bot

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	botpostgres "github.com/memohai/memoh/domains/api/internal/postgres/bot"
	"github.com/memohai/memoh/domains/runtime/workspace"
)

// SessionLocker serializes Agent session writes against an API-owned bot.
type SessionLocker = botpostgres.SessionLocker

// ExclusiveLocker serializes whole-bot operations in the caller's transaction.
type ExclusiveLocker = botpostgres.ExclusiveLocker

// Persistence is the composition-facing bot store surface.
type Persistence interface {
	botpersistence.BotStore
	botpersistence.GrantStore
	botpersistence.HeartbeatReader
	botpersistence.ActivityWriter
	workspace.BotProfileStore
	workspace.BotOwnerReader
}

// NewPostgresPersistence builds bot persistence that also satisfies workspace
// profile ports.
func NewPostgresPersistence(pool *pgxpool.Pool) Persistence {
	return botpostgres.NewStoreFromPool(pool)
}

// NewSessionLocker constructs a pool-bound bot session locker.
func NewSessionLocker(pool *pgxpool.Pool) *SessionLocker {
	return botpostgres.NewSessionLocker(pool)
}

// NewSessionLockerFromTx constructs a transaction-bound bot session locker.
func NewSessionLockerFromTx(tx pgx.Tx) *SessionLocker {
	return botpostgres.NewSessionLockerFromTx(tx)
}

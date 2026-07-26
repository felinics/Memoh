// Package assembly is the Agent domain's public composition entry.
//
// It exists for one reason the Go compiler forces: several Agent persistence
// ports are declared in the same package their PostgreSQL adapter implements
// (message, thread, compaction, backup). A constructor colocated with those
// ports would close an import cycle, so the seam lives one package over.
// Everything here is a thin constructor — no business policy, no profile
// selection, no process startup.
package assembly

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/message"
	"github.com/memohai/memoh/domains/agent/chat/thread"
	chatpostgres "github.com/memohai/memoh/domains/agent/internal/postgres/chat"
	backuppostgres "github.com/memohai/memoh/domains/agent/internal/postgres/chat/backup"
	compactionpostgres "github.com/memohai/memoh/domains/agent/internal/postgres/compaction"
)

// BotSessionWriteLocker locks the API-owned bot row (FOR KEY SHARE) before
// Agent session/message writes.
//
// Owner: API (api.bots). Agent must not query that table directly, so
// composition supplies the implementation.
type BotSessionWriteLocker interface {
	LockBotForSessionWrite(context.Context, pgtype.UUID) (pgtype.UUID, error)
}

// BotSessionWriteLockerFromTx binds a BotSessionWriteLocker to an open
// transaction, so the bot lock and the Agent writes it guards share one
// transaction.
type BotSessionWriteLockerFromTx func(tx pgx.Tx) BotSessionWriteLocker

// BotExclusiveLocker exclusively locks the API-owned bot row for backup import
// and compensate sequencing.
type BotExclusiveLocker interface {
	LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error
}

// RouteSessionCoordinator preserves Agent session lock ordering around route mutations.
type RouteSessionCoordinator = chatpostgres.RouteSessionCoordinator

// NewRouteSessionCoordinator constructs the Agent-owned PostgreSQL coordinator.
func NewRouteSessionCoordinator(pool *pgxpool.Pool) *RouteSessionCoordinator {
	return chatpostgres.NewRouteSessionCoordinator(pool)
}

// NewPostgresMessagePersistence constructs Agent-owned message persistence
// backed by PostgreSQL.
func NewPostgresMessagePersistence(
	pool *pgxpool.Pool,
	locker BotSessionWriteLocker,
	bindLocker BotSessionWriteLockerFromTx,
) message.Persistence {
	return chatpostgres.NewMessageStoreWithPool(
		locker,
		func(tx pgx.Tx) chatpostgres.BotSessionWriteLocker { return bindLocker(tx) },
		pool,
	)
}

// NewPostgresObservedRouteReader constructs the Agent-owned observation
// projection Channel reads when enriching routes.
func NewPostgresObservedRouteReader(pool *pgxpool.Pool) message.ObservedRouteReader {
	return chatpostgres.NewObservedRouteReader(pool)
}

// NewPostgresThreadStore constructs Agent-owned thread persistence backed by
// PostgreSQL.
func NewPostgresThreadStore(pool *pgxpool.Pool) thread.Store {
	return chatpostgres.NewThreadStoreWithPool(pool)
}

// CompactionStore bundles the compaction ports one adapter satisfies, so a
// single constructed adapter serves both the compaction service and artifact
// lineage reads.
type CompactionStore interface {
	compaction.CompactionStore
	compaction.ArtifactStore
}

// NewPostgresCompactionStore constructs Agent-owned compaction persistence
// backed by PostgreSQL.
func NewPostgresCompactionStore(pool *pgxpool.Pool) CompactionStore {
	return compactionpostgres.NewStoreFromDB(pool)
}

// ChatBackupStore bundles the Chat backup ports one adapter satisfies.
type ChatBackupStore interface {
	chatbackup.ExportReader
	chatbackup.ImportWriter
	chatbackup.SummaryReader
}

// NewPostgresChatBackupStore constructs Chat-owned history backup persistence
// backed by PostgreSQL.
func NewPostgresChatBackupStore(pool *pgxpool.Pool, botLock BotExclusiveLocker) (ChatBackupStore, error) {
	return backuppostgres.New(pool, botLock)
}

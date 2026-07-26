package assembly

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	channelbackup "github.com/memohai/memoh/domains/channel/backup"
	backuppostgres "github.com/memohai/memoh/domains/channel/internal/postgres/backup"
)

// BotExclusiveLocker exclusively locks the API-owned bot row for backup import
// and compensate sequencing.
//
// Owner: API (api.bots). Channel must not query that table directly, so
// composition supplies the implementation.
type BotExclusiveLocker interface {
	LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error
}

// BackupStore bundles the Channel backup ports one adapter satisfies.
type BackupStore interface {
	channelbackup.ExportReader
	channelbackup.ImportWriter
}

// NewPostgresBackupStore constructs Channel-owned observation backup
// persistence backed by PostgreSQL.
func NewPostgresBackupStore(pool *pgxpool.Pool, botLock BotExclusiveLocker) (BackupStore, error) {
	return backuppostgres.New(pool, botLock)
}

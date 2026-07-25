package assembly

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/api/access"
	"github.com/memohai/memoh/domains/api/bot"
	accesspostgres "github.com/memohai/memoh/domains/api/internal/access/postgres"
	botpostgres "github.com/memohai/memoh/domains/api/internal/bot/postgres"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	settingpostgres "github.com/memohai/memoh/domains/api/internal/setting/postgres"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

// NewBotPersistence builds bot persistence that also satisfies workspace profile ports.
func NewBotPersistence(pool *pgxpool.Pool) BotPersistence {
	return botpostgres.NewStoreFromPool(pool)
}

// BotPersistence is the composition-facing bot store surface.
type BotPersistence interface {
	bot.BotStore
	bot.GrantStore
	bot.HeartbeatReader
	bot.ActivityWriter
	workspace.BotProfileStore
	workspace.BotOwnerReader
}

// BotSessionLocker serializes Agent session writes against an API-owned bot.
type BotSessionLocker struct {
	queries *apisqlc.Queries
}

// NewBotSessionLocker constructs a pool-bound bot session locker.
func NewBotSessionLocker(pool *pgxpool.Pool) *BotSessionLocker {
	return &BotSessionLocker{queries: apisqlc.New(pool)}
}

// NewBotSessionLockerFromTx constructs a transaction-bound bot session locker.
func NewBotSessionLockerFromTx(tx pgx.Tx) *BotSessionLocker {
	return &BotSessionLocker{queries: apisqlc.New(tx)}
}

func (l *BotSessionLocker) LockBotForSessionWrite(ctx context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return l.queries.LockBotForSessionWrite(ctx, id)
}

// BotExclusiveLocker serializes whole-bot operations in their caller's transaction.
type BotExclusiveLocker struct{}

func (BotExclusiveLocker) LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	_, err = apisqlc.New(tx).LockBotExclusive(ctx, id)
	return err
}

// NewBotUserReader adapts Platform account persistence to the bots UserReader port.
func NewBotUserReader(store account.Store) bot.UserReader {
	return botUserReader{store: store}
}

type botUserReader struct {
	store account.Store
}

func (r botUserReader) GetUser(ctx context.Context, userID string) (bot.UserRecord, error) {
	if r.store == nil {
		return bot.UserRecord{}, bot.ErrOwnerUserNotFound
	}
	row, err := r.store.GetByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, account.ErrAccountNotFound) {
			return bot.UserRecord{}, bot.ErrOwnerUserNotFound
		}
		return bot.UserRecord{}, err
	}
	return bot.UserRecord{
		ID:          row.ID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		AvatarURL:   row.AvatarURL,
	}, nil
}

// NewBotContainerReader adapts Runtime container persistence to the bots ContainerReader port.
func NewBotContainerReader(store workspace.ContainerStore) bot.ContainerReader {
	return botContainerReader{store: store}
}

type botContainerReader struct {
	store workspace.ContainerStore
}

func (r botContainerReader) GetContainerByBotID(ctx context.Context, botID string) (bot.ContainerRecord, error) {
	if r.store == nil {
		return bot.ContainerRecord{}, bot.ErrContainerNotFound
	}
	row, err := r.store.FindContainer(ctx, botID)
	if err != nil {
		if errors.Is(err, workspace.ErrContainerNotFound) || errors.Is(err, workspace.ErrRecordNotFound) {
			return bot.ContainerRecord{}, bot.ErrContainerNotFound
		}
		return bot.ContainerRecord{}, err
	}
	return bot.ContainerRecord{
		ContainerID: row.ContainerID,
		Namespace:   row.Namespace,
		Image:       row.Image,
		Status:      row.Status,
	}, nil
}

// NewSettingPersistence builds API-owned settings persistence from a PostgreSQL pool.
func NewSettingPersistence(pool *pgxpool.Pool) SettingPersistence {
	return settingpostgres.NewStore(pool)
}

// SettingPersistence is the composition-facing settings store surface.
type SettingPersistence interface {
	setting.Store
	workspace.BotRuntimeSettingsReader
}

// NewChannelAccessStore builds Channel Access persistence.
func NewChannelAccessStore(pool *pgxpool.Pool) access.Store {
	return accesspostgres.NewStore(apisqlc.New(pool))
}

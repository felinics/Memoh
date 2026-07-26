package workspace

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type botProjectionQueries interface {
	GetContainerByBotID(context.Context, pgtype.UUID) (dbsqlc.RuntimeContainer, error)
}

// BotProjection is the Runtime-owned durable workspace view exposed to Core.
type BotProjection struct {
	ContainerID string
	Namespace   string
	Image       string
	Status      string
}

// BotProjectionStore keeps Runtime-owned container SQL out of API/Bots.
type BotProjectionStore struct {
	queries botProjectionQueries
}

func NewBotProjectionStore(queries botProjectionQueries) *BotProjectionStore {
	return &BotProjectionStore{queries: queries}
}

func (s *BotProjectionStore) Find(ctx context.Context, botID string) (BotProjection, bool, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return BotProjection{}, false, err
	}
	row, err := s.queries.GetContainerByBotID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return BotProjection{}, false, nil
	}
	if err != nil {
		return BotProjection{}, false, err
	}
	return BotProjection{
		ContainerID: row.ContainerID,
		Namespace:   row.Namespace,
		Image:       row.Image,
		Status:      row.Status,
	}, true, nil
}

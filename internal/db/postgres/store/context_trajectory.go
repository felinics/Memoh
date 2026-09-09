package postgresstore

import (
	"context"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func (q *Queries) AppendContextTrajectoryEvent(ctx context.Context, arg sqlc.AppendContextTrajectoryEventParams) (int64, error) {
	var sequence int64
	err := q.InTx(ctx, func(tx dbstore.Queries) error {
		if _, err := tx.(*Queries).LockBotForSessionWrite(ctx, arg.BotID); err != nil {
			return err
		}
		var err error
		sequence, err = tx.(*Queries).Queries.AppendContextTrajectoryEvent(ctx, arg)
		return err
	})
	return sequence, err
}

package postgresstore

import (
	"context"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// ClaimBotDependencyOperation preserves the shared Store result type while
// sqlc models the atomic claim/invalidation CTE as its own identical row type.
func (q *Queries) ClaimBotDependencyOperation(ctx context.Context, in dbsqlc.ClaimBotDependencyOperationParams) (dbsqlc.BotDependencyInstallation, error) {
	row, err := q.Queries.ClaimBotDependencyOperation(ctx, in)
	return dbsqlc.BotDependencyInstallation(row), err
}

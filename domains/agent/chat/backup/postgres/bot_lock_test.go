package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// exclusiveBotLock is a test fixture that preserves the import FOR UPDATE
// semantics previously inlined in production code.
type exclusiveBotLock struct{}

func (exclusiveBotLock) LockBotExclusive(ctx context.Context, tx pgx.Tx, botID string) error {
	var locked string
	if err := tx.QueryRow(ctx, `
		SELECT id::text
		FROM api.bots
		WHERE team_id = iam.memoh_current_team_id() AND id = $1
		FOR UPDATE
	`, botID).Scan(&locked); err != nil {
		return fmt.Errorf("lock chat import bot: %w", err)
	}
	return nil
}

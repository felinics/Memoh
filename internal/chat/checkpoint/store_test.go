package checkpoint

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// sessionWithoutCheckpoint serves a session that has no active checkpoint.
type sessionWithoutCheckpoint struct {
	dbstore.Queries
}

func (sessionWithoutCheckpoint) GetActiveHistoryCheckpoint(context.Context, pgtype.UUID) (sqlc.BotHistoryCheckpoint, error) {
	return sqlc.BotHistoryCheckpoint{}, pgx.ErrNoRows
}

func TestActiveOfASessionWithoutACheckpointIsZero(t *testing.T) {
	got, err := NewStore(nil, sessionWithoutCheckpoint{}).Active(context.Background(), testSessionID)
	if err != nil || got.ID != "" || len(got.Items) != 0 {
		t.Fatalf("Active = %+v, %v, want the zero checkpoint", got, err)
	}
}

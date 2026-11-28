package workspace

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type stubDisplaySettingsQueries struct {
	dbstore.Queries
	row dbsqlc.GetSettingsByBotIDRow
	err error
}

func (q *stubDisplaySettingsQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (dbsqlc.GetSettingsByBotIDRow, error) {
	return q.row, q.err
}

func TestBotDisplayEnabledDistinguishesMissingRowFromDBError(t *testing.T) {
	t.Parallel()

	const botID = "11111111-1111-1111-1111-111111111111"

	t.Run("nil queries returns false nil", func(t *testing.T) {
		t.Parallel()
		mgr := &Manager{}
		enabled, err := mgr.BotDisplayEnabled(context.Background(), botID)
		if err != nil || enabled {
			t.Fatalf("BotDisplayEnabled() = (%v, %v), want (false, nil)", enabled, err)
		}
	})

	t.Run("missing settings row returns false nil", func(t *testing.T) {
		t.Parallel()
		mgr := &Manager{queries: &stubDisplaySettingsQueries{err: pgx.ErrNoRows}}
		enabled, err := mgr.BotDisplayEnabled(context.Background(), botID)
		if err != nil || enabled {
			t.Fatalf("BotDisplayEnabled() = (%v, %v), want (false, nil)", enabled, err)
		}
	})

	t.Run("enabled row returns true nil", func(t *testing.T) {
		t.Parallel()
		mgr := &Manager{queries: &stubDisplaySettingsQueries{row: dbsqlc.GetSettingsByBotIDRow{DisplayEnabled: true}}}
		enabled, err := mgr.BotDisplayEnabled(context.Background(), botID)
		if err != nil || !enabled {
			t.Fatalf("BotDisplayEnabled() = (%v, %v), want (true, nil)", enabled, err)
		}
	})

	t.Run("db error propagates instead of collapsing to false", func(t *testing.T) {
		t.Parallel()
		dbErr := errors.New("postgres connection lost")
		mgr := &Manager{queries: &stubDisplaySettingsQueries{err: dbErr}}
		enabled, err := mgr.BotDisplayEnabled(context.Background(), botID)
		if enabled || !errors.Is(err, dbErr) {
			t.Fatalf("BotDisplayEnabled() = (%v, %v), want (false, %v)", enabled, err, dbErr)
		}
	})
}

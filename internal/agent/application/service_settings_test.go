package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/agent/channelpolicy"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

type runtimeInfoTestQueries struct {
	dbstore.Queries
	row sqlc.GetBotByIDRow
	err error
}

func (q *runtimeInfoTestQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.row, q.err
}

func TestLoadBotRuntimeInfoFailsClosedWhenMetadataCannotLoad(t *testing.T) {
	t.Parallel()

	service := &Service{
		queries: &runtimeInfoTestQueries{err: errors.New("database unavailable")},
		logger:  slog.Default(),
	}
	_, _, policy := service.loadBotRuntimeInfo(
		context.Background(), "00000000-0000-0000-0000-000000000001", channelpolicy.TelegramPlatform,
	)
	if policy.AllowsTool("send") || policy.AllowsBackendTool("send_telegram_sticker") {
		t.Fatalf("metadata failure exposed Telegram tools: %#v", policy)
	}
}

func TestLoadBotRuntimeInfoUsesStoredTelegramPolicy(t *testing.T) {
	t.Parallel()

	service := &Service{
		queries: &runtimeInfoTestQueries{row: sqlc.GetBotByIDRow{
			Name:     "bot",
			Metadata: []byte(`{"telegram_enabled_tools":["send"]}`),
		}},
		logger: slog.Default(),
	}
	_, _, policy := service.loadBotRuntimeInfo(
		context.Background(), "00000000-0000-0000-0000-000000000001", channelpolicy.TelegramPlatform,
	)
	if !policy.AllowsTool("send") || policy.AllowsTool("web_search") {
		t.Fatalf("stored policy was not applied: %#v", policy)
	}
}

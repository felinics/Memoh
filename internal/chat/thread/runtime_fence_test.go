package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type runtimeFenceSessionQueries struct {
	dbstore.Queries
	titleParams    *dbsqlc.UpdateSessionTitleWithRuntimeFenceParams
	metadataParams *dbsqlc.UpdateSessionMetadataWithRuntimeFenceParams
	err            error
}

func (q *runtimeFenceSessionQueries) UpdateSessionTitleWithRuntimeFence(_ context.Context, arg dbsqlc.UpdateSessionTitleWithRuntimeFenceParams) (dbsqlc.BotSession, error) {
	q.titleParams = &arg
	return runtimeFenceSessionRow(arg.ID, arg.BotID), q.err
}

func (q *runtimeFenceSessionQueries) UpdateSessionMetadataWithRuntimeFence(_ context.Context, arg dbsqlc.UpdateSessionMetadataWithRuntimeFenceParams) (dbsqlc.BotSession, error) {
	q.metadataParams = &arg
	return runtimeFenceSessionRow(arg.ID, arg.BotID), q.err
}

func TestSessionUpdatesUseRuntimeFence(t *testing.T) {
	t.Parallel()

	const (
		botID     = "11111111-1111-1111-1111-111111111111"
		sessionID = "22222222-2222-2222-2222-222222222222"
	)
	queries := &runtimeFenceSessionQueries{}
	service := NewService(nil, queries, nil)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 9})

	if _, err := service.UpdateTitle(ctx, sessionID, "fenced title"); err != nil {
		t.Fatalf("UpdateTitle() error = %v", err)
	}
	if _, err := service.UpdateMetadata(ctx, sessionID, map[string]any{"fenced": true}); err != nil {
		t.Fatalf("UpdateMetadata() error = %v", err)
	}
	if queries.titleParams == nil || queries.titleParams.RuntimeFencingToken != 9 {
		t.Fatalf("title fence params = %#v", queries.titleParams)
	}
	if queries.metadataParams == nil || queries.metadataParams.RuntimeFencingToken != 9 {
		t.Fatalf("metadata fence params = %#v", queries.metadataParams)
	}
}

func TestSessionUpdateMapsMissingFenceToStale(t *testing.T) {
	t.Parallel()

	const (
		botID     = "11111111-1111-1111-1111-111111111111"
		sessionID = "22222222-2222-2222-2222-222222222222"
	)
	queries := &runtimeFenceSessionQueries{err: pgx.ErrNoRows}
	service := NewService(nil, queries, nil)
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 3})

	if _, err := service.UpdateMetadata(ctx, sessionID, map[string]any{}); !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("UpdateMetadata() error = %v, want ErrStale", err)
	}
}

func runtimeFenceSessionRow(sessionID, botID pgtype.UUID) dbsqlc.BotSession {
	return dbsqlc.BotSession{
		ID:              sessionID,
		BotID:           botID,
		Type:            TypeChat,
		SessionMode:     TypeChat,
		RuntimeType:     RuntimeModel,
		RuntimeMetadata: []byte(`{}`),
		Metadata:        []byte(`{}`),
	}
}

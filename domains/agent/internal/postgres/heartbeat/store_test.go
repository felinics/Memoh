package heartbeat

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

const (
	heartbeatBotID     = "71111111-1111-4111-8111-111111111111"
	heartbeatOwnerID   = "72222222-2222-4222-8222-222222222222"
	heartbeatSessionID = "73333333-3333-4333-8333-333333333333"
	heartbeatLogID     = "74444444-4444-4444-8444-444444444444"
	heartbeatModelID   = "75555555-5555-4555-8555-555555555555"
)

type queriesStub struct {
	queries
	createLog   func(context.Context, dbsqlc.CreateHeartbeatLogParams) (dbsqlc.CreateHeartbeatLogRow, error)
	completeLog func(context.Context, dbsqlc.CompleteHeartbeatLogParams) (dbsqlc.AgentBotHeartbeatLog, error)
	countLogs   func(context.Context, pgtype.UUID) (int64, error)
	listLogs    func(context.Context, dbsqlc.ListHeartbeatLogsByBotParams) ([]dbsqlc.ListHeartbeatLogsByBotRow, error)
	deleteLogs  func(context.Context, pgtype.UUID) error
}

func (s *queriesStub) CreateHeartbeatLog(ctx context.Context, arg dbsqlc.CreateHeartbeatLogParams) (dbsqlc.CreateHeartbeatLogRow, error) {
	return s.createLog(ctx, arg)
}

func (s *queriesStub) CompleteHeartbeatLog(ctx context.Context, arg dbsqlc.CompleteHeartbeatLogParams) (dbsqlc.AgentBotHeartbeatLog, error) {
	return s.completeLog(ctx, arg)
}

func (s *queriesStub) CountHeartbeatLogsByBot(ctx context.Context, id pgtype.UUID) (int64, error) {
	return s.countLogs(ctx, id)
}

func (s *queriesStub) ListHeartbeatLogsByBot(ctx context.Context, arg dbsqlc.ListHeartbeatLogsByBotParams) ([]dbsqlc.ListHeartbeatLogsByBotRow, error) {
	return s.listLogs(ctx, arg)
}

func (s *queriesStub) DeleteHeartbeatLogsByBot(ctx context.Context, id pgtype.UUID) error {
	return s.deleteLogs(ctx, id)
}

type botReaderStub struct {
	listEnabled func(context.Context) ([]persistence.BotRecord, error)
	getBot      func(context.Context, string) (persistence.BotRecord, error)
}

func (s botReaderStub) ListEnabledBots(ctx context.Context) ([]persistence.BotRecord, error) {
	return s.listEnabled(ctx)
}

func (s botReaderStub) GetBot(ctx context.Context, id string) (persistence.BotRecord, error) {
	return s.getBot(ctx, id)
}

func TestStoreMapsEnabledAndBotRecords(t *testing.T) {
	store := newStore(&queriesStub{}, botReaderStub{
		listEnabled: func(context.Context) ([]persistence.BotRecord, error) {
			return []persistence.BotRecord{{
				ID: heartbeatBotID, OwnerUserID: heartbeatOwnerID,
				HeartbeatEnabled: true, HeartbeatInterval: 30,
			}}, nil
		},
		getBot: func(_ context.Context, id string) (persistence.BotRecord, error) {
			if id != heartbeatBotID {
				t.Fatalf("bot id = %s", id)
			}
			return persistence.BotRecord{
				ID: id, OwnerUserID: heartbeatOwnerID, Status: "ready",
				HeartbeatEnabled: true, HeartbeatInterval: 45,
			}, nil
		},
	})

	rows, err := store.ListEnabledBots(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != heartbeatBotID || rows[0].HeartbeatInterval != 30 {
		t.Fatalf("ListEnabledBots() = %+v, %v", rows, err)
	}
	bot, err := store.GetBot(t.Context(), heartbeatBotID)
	if err != nil || bot.OwnerUserID != heartbeatOwnerID || bot.Status != "ready" || bot.HeartbeatInterval != 45 {
		t.Fatalf("GetBot() = %+v, %v", bot, err)
	}
}

func TestStoreMapsLogLifecycleAndPage(t *testing.T) {
	started := time.Date(2026, 7, 23, 4, 5, 6, 0, time.UTC)
	completed := started.Add(time.Minute)
	store := newStore(&queriesStub{
		createLog: func(_ context.Context, arg dbsqlc.CreateHeartbeatLogParams) (dbsqlc.CreateHeartbeatLogRow, error) {
			if arg.BotID.String() != heartbeatBotID || arg.SessionID.String() != heartbeatSessionID {
				t.Fatalf("create params = %+v", arg)
			}
			return dbsqlc.CreateHeartbeatLogRow{ID: heartbeatUUID(t, heartbeatLogID)}, nil
		},
		completeLog: func(_ context.Context, arg dbsqlc.CompleteHeartbeatLogParams) (dbsqlc.AgentBotHeartbeatLog, error) {
			if arg.ID.String() != heartbeatLogID || arg.ModelID.String() != heartbeatModelID || string(arg.Usage) != `{"tokens":3}` {
				t.Fatalf("complete params = %+v", arg)
			}
			return dbsqlc.AgentBotHeartbeatLog{}, nil
		},
		countLogs: func(_ context.Context, id pgtype.UUID) (int64, error) {
			if id.String() != heartbeatBotID {
				t.Fatalf("count id = %s", id.String())
			}
			return 1, nil
		},
		listLogs: func(_ context.Context, arg dbsqlc.ListHeartbeatLogsByBotParams) ([]dbsqlc.ListHeartbeatLogsByBotRow, error) {
			if arg.BotID.String() != heartbeatBotID || arg.Limit != 10 || arg.Offset != 20 {
				t.Fatalf("list params = %+v", arg)
			}
			return []dbsqlc.ListHeartbeatLogsByBotRow{{
				ID: heartbeatUUID(t, heartbeatLogID), BotID: arg.BotID,
				SessionID: heartbeatUUID(t, heartbeatSessionID), Status: "ok", Usage: []byte(`{"tokens":3}`),
				StartedAt: heartbeatTimestamp(started), CompletedAt: heartbeatTimestamp(completed),
			}}, nil
		},
		deleteLogs: func(_ context.Context, id pgtype.UUID) error {
			if id.String() != heartbeatBotID {
				t.Fatalf("delete id = %s", id.String())
			}
			return nil
		},
	}, botReaderStub{})

	logID, err := store.CreateLog(t.Context(), persistence.CreateLogCommand{BotID: heartbeatBotID, SessionID: heartbeatSessionID})
	if err != nil || logID != heartbeatLogID {
		t.Fatalf("CreateLog() = %q, %v", logID, err)
	}
	if err := store.CompleteLog(t.Context(), persistence.CompleteLogCommand{ID: heartbeatLogID, Usage: []byte(`{"tokens":3}`), ModelID: heartbeatModelID}); err != nil {
		t.Fatalf("CompleteLog() error = %v", err)
	}
	count, err := store.CountLogsByBot(t.Context(), heartbeatBotID)
	if err != nil || count != 1 {
		t.Fatalf("CountLogsByBot() = %d, %v", count, err)
	}
	rows, err := store.ListLogsByBot(t.Context(), persistence.LogPage{BotID: heartbeatBotID, Limit: 10, Offset: 20})
	if err != nil || len(rows) != 1 || rows[0].StartedAt != started || rows[0].CompletedAt != completed {
		t.Fatalf("ListLogsByBot() = %+v, %v", rows, err)
	}
	if err := store.DeleteLogsByBot(t.Context(), heartbeatBotID); err != nil {
		t.Fatalf("DeleteLogsByBot() error = %v", err)
	}
}

func TestStoreRejectsInvalidUUIDBeforeQuery(t *testing.T) {
	called := false
	store := newStore(&queriesStub{}, botReaderStub{getBot: func(context.Context, string) (persistence.BotRecord, error) {
		called = true
		return persistence.BotRecord{}, nil
	}})
	if _, err := store.GetBot(t.Context(), "bad"); err == nil {
		t.Fatal("expected UUID error")
	}
	if called {
		t.Fatal("query called for invalid UUID")
	}
}

func heartbeatUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("scan UUID: %v", err)
	}
	return id
}

func heartbeatTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

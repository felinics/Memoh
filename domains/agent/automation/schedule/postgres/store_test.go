package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/automation/schedule"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

const (
	testScheduleID = "11111111-1111-4111-8111-111111111111"
	testBotID      = "22222222-2222-4222-8222-222222222222"
	testSessionID  = "33333333-3333-4333-8333-333333333333"
	testOwnerID    = "44444444-4444-4444-8444-444444444444"
	testLogID      = "55555555-5555-4555-8555-555555555555"
	testModelID    = "66666666-6666-4666-8666-666666666666"
)

type queriesStub struct {
	queries
	create       func(context.Context, dbsqlc.CreateScheduleParams) (dbsqlc.AgentSchedule, error)
	get          func(context.Context, pgtype.UUID) (dbsqlc.AgentSchedule, error)
	listEnabled  func(context.Context) ([]dbsqlc.AgentSchedule, error)
	createLog    func(context.Context, dbsqlc.CreateScheduleLogParams) (dbsqlc.CreateScheduleLogRow, error)
	completeLog  func(context.Context, dbsqlc.CompleteScheduleLogParams) (dbsqlc.AgentScheduleLog, error)
	listSchedule func(context.Context, dbsqlc.ListScheduleLogsByScheduleParams) ([]dbsqlc.ListScheduleLogsByScheduleRow, error)
}

func (s *queriesStub) CreateSchedule(ctx context.Context, arg dbsqlc.CreateScheduleParams) (dbsqlc.AgentSchedule, error) {
	return s.create(ctx, arg)
}

func (s *queriesStub) GetScheduleByID(ctx context.Context, id pgtype.UUID) (dbsqlc.AgentSchedule, error) {
	return s.get(ctx, id)
}

func (s *queriesStub) ListEnabledSchedules(ctx context.Context) ([]dbsqlc.AgentSchedule, error) {
	return s.listEnabled(ctx)
}

func (s *queriesStub) CreateScheduleLog(ctx context.Context, arg dbsqlc.CreateScheduleLogParams) (dbsqlc.CreateScheduleLogRow, error) {
	return s.createLog(ctx, arg)
}

func (s *queriesStub) CompleteScheduleLog(ctx context.Context, arg dbsqlc.CompleteScheduleLogParams) (dbsqlc.AgentScheduleLog, error) {
	return s.completeLog(ctx, arg)
}

func (s *queriesStub) ListScheduleLogsBySchedule(ctx context.Context, arg dbsqlc.ListScheduleLogsByScheduleParams) ([]dbsqlc.ListScheduleLogsByScheduleRow, error) {
	return s.listSchedule(ctx, arg)
}

type botReaderStub struct {
	getBot func(context.Context, string) (schedule.BotRecord, error)
}

func (s botReaderStub) GetBot(ctx context.Context, id string) (schedule.BotRecord, error) {
	return s.getBot(ctx, id)
}

func TestStoreCreateMapsCommandAndRecord(t *testing.T) {
	now := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	maxCalls := 9
	store := newStore(&queriesStub{create: func(_ context.Context, arg dbsqlc.CreateScheduleParams) (dbsqlc.AgentSchedule, error) {
		if arg.BotID.String() != testBotID || !arg.MaxCalls.Valid || arg.MaxCalls.Int32 != 9 || !arg.Enabled {
			t.Fatalf("params = %+v", arg)
		}
		return dbsqlc.AgentSchedule{
			ID: testUUID(t, testScheduleID), Name: arg.Name, Description: arg.Description,
			Pattern: arg.Pattern, MaxCalls: arg.MaxCalls, CurrentCalls: 2,
			CreatedAt: timestamp(now), UpdatedAt: timestamp(now), Enabled: arg.Enabled,
			Command: arg.Command, BotID: arg.BotID,
		}, nil
	}}, botReaderStub{})

	record, err := store.Create(t.Context(), schedule.CreateCommand{
		Name: "daily", Description: "daily report", Pattern: "0 0 * * *",
		MaxCalls: &maxCalls, Enabled: true, Command: "run", BotID: testBotID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if record.ID != testScheduleID || record.BotID != testBotID || record.MaxCalls == nil || *record.MaxCalls != 9 || record.CurrentCalls != 2 || record.CreatedAt != now {
		t.Fatalf("record = %+v", record)
	}
}

func TestStoreMapsNotFoundAndRejectsInvalidUUID(t *testing.T) {
	called := false
	store := newStore(&queriesStub{get: func(context.Context, pgtype.UUID) (dbsqlc.AgentSchedule, error) {
		called = true
		return dbsqlc.AgentSchedule{}, pgx.ErrNoRows
	}}, botReaderStub{})
	if _, err := store.Get(t.Context(), testScheduleID); !errors.Is(err, schedule.ErrNotFound) {
		t.Fatalf("Get() error = %v", err)
	}
	called = false
	if _, err := store.Get(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("expected UUID error")
	}
	if called {
		t.Fatal("query called for invalid UUID")
	}
}

func TestStoreMapsBotAndEnabledSchedules(t *testing.T) {
	botCalled := false
	store := newStore(&queriesStub{
		listEnabled: func(context.Context) ([]dbsqlc.AgentSchedule, error) {
			return []dbsqlc.AgentSchedule{{ID: testUUID(t, testScheduleID), BotID: testUUID(t, testBotID)}}, nil
		},
	}, botReaderStub{getBot: func(_ context.Context, id string) (schedule.BotRecord, error) {
		botCalled = true
		if id != testBotID {
			t.Fatalf("bot id = %s", id)
		}
		return schedule.BotRecord{OwnerUserID: testOwnerID, Timezone: "Asia/Tokyo"}, nil
	}})
	records, err := store.ListEnabled(t.Context())
	if err != nil || len(records) != 1 || records[0].ID != testScheduleID {
		t.Fatalf("ListEnabled() = %+v, %v", records, err)
	}
	bot, err := store.GetBot(t.Context(), testBotID)
	if err != nil || bot.OwnerUserID != testOwnerID || bot.Timezone != "Asia/Tokyo" || !botCalled {
		t.Fatalf("GetBot() = %+v, %v", bot, err)
	}
}

func TestStoreMapsLogCommandsAndRows(t *testing.T) {
	started := time.Date(2026, 7, 23, 2, 3, 4, 0, time.UTC)
	completed := started.Add(time.Minute)
	store := newStore(&queriesStub{
		createLog: func(_ context.Context, arg dbsqlc.CreateScheduleLogParams) (dbsqlc.CreateScheduleLogRow, error) {
			if arg.ScheduleID.String() != testScheduleID || arg.BotID.String() != testBotID || arg.SessionID.String() != testSessionID {
				t.Fatalf("create params = %+v", arg)
			}
			return dbsqlc.CreateScheduleLogRow{ID: testUUID(t, testLogID)}, nil
		},
		completeLog: func(_ context.Context, arg dbsqlc.CompleteScheduleLogParams) (dbsqlc.AgentScheduleLog, error) {
			if arg.ID.String() != testLogID || arg.ModelID.String() != testModelID || string(arg.Usage) != `{"tokens":2}` || arg.Status != "ok" {
				t.Fatalf("complete params = %+v", arg)
			}
			return dbsqlc.AgentScheduleLog{}, nil
		},
		listSchedule: func(_ context.Context, arg dbsqlc.ListScheduleLogsByScheduleParams) ([]dbsqlc.ListScheduleLogsByScheduleRow, error) {
			if arg.ScheduleID.String() != testScheduleID || arg.Limit != 20 || arg.Offset != 40 {
				t.Fatalf("list params = %+v", arg)
			}
			return []dbsqlc.ListScheduleLogsByScheduleRow{{
				ID: testUUID(t, testLogID), ScheduleID: arg.ScheduleID, BotID: testUUID(t, testBotID),
				SessionID: testUUID(t, testSessionID), Status: "ok", Usage: []byte(`{"tokens":2}`),
				StartedAt: timestamp(started), CompletedAt: timestamp(completed),
			}}, nil
		},
	}, botReaderStub{})

	logID, err := store.CreateLog(t.Context(), schedule.CreateLogCommand{ScheduleID: testScheduleID, BotID: testBotID, SessionID: testSessionID})
	if err != nil || logID != testLogID {
		t.Fatalf("CreateLog() = %q, %v", logID, err)
	}
	if err := store.CompleteLog(t.Context(), schedule.CompleteLogCommand{ID: testLogID, Status: "ok", Usage: []byte(`{"tokens":2}`), ModelID: testModelID}); err != nil {
		t.Fatalf("CompleteLog() error = %v", err)
	}
	rows, err := store.ListLogsBySchedule(t.Context(), schedule.LogPage{ID: testScheduleID, Limit: 20, Offset: 40})
	if err != nil || len(rows) != 1 || rows[0].ID != testLogID || rows[0].StartedAt != started || rows[0].CompletedAt != completed {
		t.Fatalf("ListLogsBySchedule() = %+v, %v", rows, err)
	}
}

func testUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		t.Fatalf("scan UUID: %v", err)
	}
	return id
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

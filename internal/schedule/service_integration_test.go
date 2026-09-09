package schedule_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/boot"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/schedule"
)

func setupScheduleIntegrationTest(t *testing.T) (*schedule.Service, dbstore.Queries, *pgxpool.Pool, *mockTriggerer, func()) {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip integration test: TEST_POSTGRES_DSN is not set")
	}

	ctx := context.Background()
	pool, err := db.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip integration test: cannot connect to database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("skip integration test: database ping failed: %v", err)
	}

	queries := postgresstore.NewQueries(sqlc.New(pool))
	mock := &mockTriggerer{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &boot.RuntimeConfig{JwtSecret: "integration-test-jwt-secret"}
	svc := schedule.NewService(logger, queries, mock, nil, nil, cfg)

	return svc, queries, pool, mock, func() { pool.Close() }
}

type mockTriggerer struct {
	called  bool
	botID   string
	payload schedule.TriggerPayload
	token   string
}

func (m *mockTriggerer) TriggerSchedule(_ context.Context, botID string, payload schedule.TriggerPayload, token string) (schedule.TriggerResult, error) {
	m.called = true
	m.botID = botID
	m.payload = payload
	m.token = token
	return schedule.TriggerResult{Status: "ok"}, nil
}

func createUserBotAndSchedule(ctx context.Context, t *testing.T, queries dbstore.Queries) (ownerUserID, botID, scheduleID string) {
	t.Helper()

	userRow, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		IsActive: true,
		Metadata: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	ownerUserID = userRow.ID.String()

	pgOwnerID, err := db.ParseUUID(ownerUserID)
	if err != nil {
		t.Fatalf("parse owner uuid: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{"source": "schedule-integration-test"})
	botRow, err := queries.CreateBot(ctx, sqlc.CreateBotParams{
		OwnerUserID: pgOwnerID,
		Name:        "schedule-test-bot",
		DisplayName: pgtype.Text{String: "schedule-test-bot", Valid: true},
		AvatarUrl:   pgtype.Text{},
		IsActive:    true,
		Metadata:    meta,
		Status:      "ready",
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botID = botRow.ID.String()

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		t.Fatalf("parse bot uuid: %v", err)
	}
	schedRow, err := queries.CreateSchedule(ctx, sqlc.CreateScheduleParams{
		Name:        "integration-daily",
		Description: "daily job for integration test",
		Pattern:     "0 0 * * *",
		MaxCalls:    pgtype.Int4{Valid: false},
		Enabled:     true,
		Command:     "run daily report",
		BotID:       pgBotID,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	scheduleID = schedRow.ID.String()
	return ownerUserID, botID, scheduleID
}

func cleanupScheduleTestData(ctx context.Context, t *testing.T, queries dbstore.Queries, pool *pgxpool.Pool, ownerUserID, botID, scheduleID string) {
	t.Helper()
	schedID, _ := db.ParseUUID(scheduleID)
	_ = queries.DeleteSchedule(ctx, schedID)
	botUUID, _ := db.ParseUUID(botID)
	_ = queries.DeleteBotByID(ctx, botUUID)
	userUUID, _ := db.ParseUUID(ownerUserID)
	_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userUUID)
}

func TestIntegrationTrigger_CallsTriggererWithCorrectPayload(t *testing.T) {
	svc, queries, pool, mock, cleanup := setupScheduleIntegrationTest(t)
	defer cleanup()

	ctx := context.Background()
	ownerUserID, botID, scheduleID := createUserBotAndSchedule(ctx, t, queries)
	defer cleanupScheduleTestData(ctx, t, queries, pool, ownerUserID, botID, scheduleID)

	err := svc.Trigger(ctx, scheduleID)
	if err != nil {
		t.Fatalf("Trigger failed: %v", err)
	}

	if !mock.called {
		t.Fatal("triggerer was not called")
	}
	if mock.botID != botID {
		t.Errorf("triggerer botID = %s, want %s", mock.botID, botID)
	}
	if mock.payload.ID != scheduleID {
		t.Errorf("payload.ID = %s, want %s", mock.payload.ID, scheduleID)
	}
	if mock.payload.Name != "integration-daily" {
		t.Errorf("payload.Name = %s, want integration-daily", mock.payload.Name)
	}
	if mock.payload.Command != "run daily report" {
		t.Errorf("payload.Command = %s, want run daily report", mock.payload.Command)
	}
	if mock.payload.OwnerUserID != ownerUserID {
		t.Errorf("payload.OwnerUserID = %s, want %s", mock.payload.OwnerUserID, ownerUserID)
	}
	if !strings.HasPrefix(mock.token, "Bearer ") {
		t.Errorf("token should have Bearer prefix, got: %s", mock.token)
	}
}

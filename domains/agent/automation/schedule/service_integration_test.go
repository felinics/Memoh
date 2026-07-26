package schedule_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	schedulepersistence "github.com/memohai/memoh/domains/agent/automation/schedule/persistence"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/automation/schedule"
	schedulepostgres "github.com/memohai/memoh/domains/agent/internal/postgres/schedule"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type integrationBotReader struct {
	pool *pgxpool.Pool
}

func (r integrationBotReader) GetBot(ctx context.Context, id string) (schedulepersistence.BotRecord, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return schedulepersistence.BotRecord{}, err
	}
	var ownerUserID pgtype.UUID
	var timezone pgtype.Text
	err = r.pool.QueryRow(ctx, `
		SELECT owner_user_id, timezone
		FROM api.bots
		WHERE team_id = iam.memoh_current_team_id() AND id = $1
	`, parsed).Scan(&ownerUserID, &timezone)
	if err != nil {
		return schedulepersistence.BotRecord{}, err
	}
	return schedulepersistence.BotRecord{
		OwnerUserID: ownerUserID.String(),
		Timezone:    db.TextToString(timezone),
	}, nil
}

func setupScheduleIntegrationTest(t *testing.T) (*schedule.Service, *agentsqlc.Queries, *pgxpool.Pool, *mockTriggerer, func()) {
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

	agentQueries := agentsqlc.New(pool)
	mock := &mockTriggerer{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := schedulepostgres.NewStore(agentQueries, integrationBotReader{pool: pool})
	svc := schedule.NewService(logger, store, mock, nil, "integration-test-jwt-secret", nil)

	return svc, agentQueries, pool, mock, func() { pool.Close() }
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

func createUserBotAndSchedule(ctx context.Context, t *testing.T, queries *agentsqlc.Queries, pool *pgxpool.Pool) (ownerUserID, botID, scheduleID string) {
	t.Helper()

	ownerUserID = uuid.NewString()
	botID = uuid.NewString()
	name := "schedule-test-bot"
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO iam.users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		)
		INSERT INTO iam.team_members (team_id, user_id, is_active)
		SELECT iam.memoh_current_team_id(), id, true FROM created_user
	`, ownerUserID, "sched-"+uuid.NewString()[:12]); err != nil {
		t.Fatalf("create user: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{"source": "schedule-integration-test"})
	if _, err := pool.Exec(ctx, `
		INSERT INTO api.bots (id, owner_user_id, name, display_name, is_active, metadata, status)
		VALUES ($1, $2, $3, $3, true, $4, 'ready')
	`, botID, ownerUserID, name, meta); err != nil {
		t.Fatalf("create bot: %v", err)
	}

	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		t.Fatalf("parse bot uuid: %v", err)
	}
	schedRow, err := queries.CreateSchedule(ctx, agentsqlc.CreateScheduleParams{
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

func cleanupScheduleTestData(ctx context.Context, t *testing.T, queries *agentsqlc.Queries, pool *pgxpool.Pool, ownerUserID, botID, scheduleID string) {
	t.Helper()
	schedID, _ := db.ParseUUID(scheduleID)
	_ = queries.DeleteSchedule(ctx, schedID)
	_, _ = pool.Exec(ctx, `DELETE FROM api.bots WHERE id = $1`, botID)
	_, _ = pool.Exec(ctx, `DELETE FROM iam.users WHERE id = $1`, ownerUserID)
}

func TestIntegrationTrigger_CallsTriggererWithCorrectPayload(t *testing.T) {
	svc, queries, pool, mock, cleanup := setupScheduleIntegrationTest(t)
	defer cleanup()

	ctx := context.Background()
	ownerUserID, botID, scheduleID := createUserBotAndSchedule(ctx, t, queries, pool)
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

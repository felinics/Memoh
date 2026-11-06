package schedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
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
	svc := schedule.NewService(logger, queries, mock, scheduleSessionCreator{pool}, nil, cfg)

	return svc, queries, pool, mock, func() { _ = svc.Shutdown(context.Background()); pool.Close() }
}

type mockTriggerer struct {
	called  bool
	botID   string
	payload schedule.TriggerPayload
	err     error
}

func (m *mockTriggerer) TriggerSchedule(_ context.Context, botID string, payload schedule.TriggerPayload) (schedule.TriggerResult, error) {
	m.called = true
	m.botID = botID
	m.payload = payload
	if m.err != nil {
		return schedule.TriggerResult{}, m.err
	}
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
		Name:        "schedule-test-" + uuid.NewString(),
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
		RunTarget:   schedule.RunTargetNewSession,
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
}

type scheduleSessionCreator struct{ pool *pgxpool.Pool }

func (s scheduleSessionCreator) CreateSession(ctx context.Context, botID, kind string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, "INSERT INTO bot_sessions(bot_id,type,title) VALUES ($1,$2,'Schedule QA') RETURNING id", botID, kind).Scan(&id)
	return id, err
}

func (s scheduleSessionCreator) CreateScheduleSession(ctx context.Context, spec schedule.SessionSpec) (string, error) {
	return s.CreateSession(ctx, spec.BotID, "schedule")
}

func TestIntegrationScheduleFireIdentityAndBudget(t *testing.T) {
	svc, q, pool, mock, cleanup := setupScheduleIntegrationTest(t)
	defer cleanup()
	ctx := t.Context()
	owner, bot, id := createUserBotAndSchedule(ctx, t, q)
	defer cleanupScheduleTestData(context.Background(), t, q, pool, owner, bot, id)
	if err := svc.Trigger(ctx, id); err != nil {
		t.Fatal(err)
	}
	first := mock.payload
	if first.FireID == "" {
		t.Fatal("missing persisted fire identity")
	}
	if _, err := pool.Exec(ctx, "UPDATE schedule SET run_target='existing_session',target_session_id=$2 WHERE id=$1", id, first.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Trigger(ctx, id); err != nil {
		t.Fatal(err)
	}
	if mock.payload.FireID == first.FireID || mock.payload.SessionID != first.SessionID {
		t.Fatalf("fire identity reused: %+v", mock.payload)
	}
	if _, err := pool.Exec(ctx, "UPDATE schedule SET max_run_seconds=7200 WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	loaded, err := svc.Get(ctx, id)
	if err != nil || loaded.MaxRunSeconds != 7200 {
		t.Fatalf("budget=%d err=%v", loaded.MaxRunSeconds, err)
	}
}

// A failed fire stores the catalog code of its cause and no text; a log row
// written by an earlier server still reads back with its message.
func TestIntegrationFailedFireLogStoresCode(t *testing.T) {
	svc, q, pool, mock, cleanup := setupScheduleIntegrationTest(t)
	defer cleanup()
	ctx := t.Context()
	owner, bot, id := createUserBotAndSchedule(ctx, t, q)
	defer cleanupScheduleTestData(context.Background(), t, q, pool, owner, bot, id)

	mock.err = errors.New("upstream said: secret detail")
	if err := svc.Trigger(ctx, id); err == nil {
		t.Fatal("Trigger succeeded, want the triggerer's failure")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO schedule_logs (schedule_id, bot_id, status, error_message, completed_at)
		SELECT id, bot_id, 'error', 'This scheduled run reached its execution limit.', now() FROM schedule WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	logs, _, err := svc.ListLogsBySchedule(ctx, id, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var failed, legacy *schedule.Log
	for i := range logs {
		switch {
		case logs[i].ErrorCode != "":
			failed = &logs[i]
		case logs[i].ErrorMessage != "":
			legacy = &logs[i]
		}
	}
	if failed == nil || failed.ErrorCode != "internal" || failed.ErrorMessage != "" || failed.Status != "error" {
		t.Fatalf("failed fire log = %+v, want code internal and no message", failed)
	}
	if legacy == nil || legacy.ErrorCode != "" {
		t.Fatalf("earlier server's log = %+v, want its message and no code", legacy)
	}
}

// A schedule's run list returns live sessions newest first with their titles,
// keeps a fire that failed before it had a session, drops fires whose session
// was soft- or hard-deleted, and pages by keyset without duplicates.
func TestIntegrationListRunSessions(t *testing.T) {
	svc, q, pool, _, cleanup := setupScheduleIntegrationTest(t)
	defer cleanup()
	ctx := t.Context()
	owner, bot, id := createUserBotAndSchedule(ctx, t, q)
	defer cleanupScheduleTestData(context.Background(), t, q, pool, owner, bot, id)

	newSession := func(title string) string {
		var sessionID string
		if err := pool.QueryRow(ctx, "INSERT INTO bot_sessions(bot_id,type,title) VALUES ($1,'schedule',$2) RETURNING id", bot, title).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		return sessionID
	}
	addLog := func(sessionID any, status string, minutesAgo int, completed bool) string {
		var logID string
		completedAt := "NULL"
		if completed {
			completedAt = "now() - make_interval(mins => $5) + interval '30 seconds'"
		}
		err := pool.QueryRow(ctx, `INSERT INTO schedule_logs (schedule_id, bot_id, session_id, status, started_at, completed_at)
			VALUES ($1, $2, $3, $4, now() - make_interval(mins => $5), `+completedAt+`) RETURNING id`,
			id, bot, sessionID, status, minutesAgo).Scan(&logID)
		if err != nil {
			t.Fatal(err)
		}
		return logID
	}

	running := addLog(newSession("newest run"), "ok", 1, false)
	failedEarly := addLog(nil, "error", 2, true)
	softDeleted := newSession("soft deleted")
	addLog(softDeleted, "ok", 3, true)
	if _, err := pool.Exec(ctx, "UPDATE bot_sessions SET deleted_at = now() WHERE id = $1", softDeleted); err != nil {
		t.Fatal(err)
	}
	addLog(nil, "ok", 4, true) // hard-deleted session: the FK nulled session_id
	older := addLog(newSession("older run"), "ok", 5, true)
	oldest := addLog(newSession("oldest run"), "error", 6, true)

	first, err := svc.ListRunSessions(ctx, bot, id, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want two items and a cursor", first)
	}
	if first.Items[0].LogID != running || first.Items[0].Title != "newest run" || first.Items[0].Status != schedule.RunStatusRunning || first.Items[0].CompletedAt != nil {
		t.Fatalf("newest item = %+v, want the running fire with its title", first.Items[0])
	}
	if first.Items[1].LogID != failedEarly || first.Items[1].SessionID != "" || first.Items[1].Status != schedule.RunStatusError {
		t.Fatalf("second item = %+v, want the fire that failed before it had a session", first.Items[1])
	}
	second, err := svc.ListRunSessions(ctx, bot, id, 2, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want the last two items and no cursor", second)
	}
	if second.Items[0].LogID != older || second.Items[0].Title != "older run" || second.Items[1].LogID != oldest || second.Items[1].Status != schedule.RunStatusError {
		t.Fatalf("second page items = %+v, want older then oldest without deleted sessions", second.Items)
	}

	otherOwner, otherBot, otherSchedule := createUserBotAndSchedule(ctx, t, q)
	defer cleanupScheduleTestData(context.Background(), t, q, pool, otherOwner, otherBot, otherSchedule)
	if _, err := svc.ListRunSessions(ctx, otherBot, id, 10, ""); !errors.Is(err, schedule.ErrScheduleNotFound) {
		t.Fatalf("schedule of another bot error = %v, want ErrScheduleNotFound", err)
	}
	if _, err := svc.ListRunSessions(ctx, bot, otherSchedule, 10, ""); !errors.Is(err, schedule.ErrScheduleNotFound) {
		t.Fatalf("another bot's schedule error = %v, want ErrScheduleNotFound", err)
	}
}

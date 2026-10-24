package botbackup

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// A restored session keeps each message's original time, and its context
// starts where the newest history that fits its budget does, as one the
// upgrade to history origins found.
func TestPostgresRestoreHistoryPlacesTheHistoryOrigin(t *testing.T) {
	ctx := context.Background()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the history restore PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}

	userID, providerID, modelID, botID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, userID)
	exec(`INSERT INTO providers (id, name) VALUES ($1::uuid, $1::text)`, providerID)
	// A 2000-token window: 2880 bytes of room hold two 1000-byte turns.
	exec(`INSERT INTO models (id, provider_id, model_id, name, type, config) VALUES ($1::uuid, $2, $1::text, $1::text, 'chat', '{"context_window": 2000}'::jsonb)`, modelID, providerID)
	exec(`INSERT INTO bots (id, owner_user_id, name, chat_model_id) VALUES ($1::uuid, $2, $1::text, $3)`, botID, userID, modelID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id=$1`, botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM models WHERE id=$1`, modelID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	sourceSession := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	sessions := []dbsqlc.ListSessionsByBotRow{{
		ID:          sourceSession,
		ChannelType: pgtype.Text{String: "local", Valid: true},
		Type:        "chat",
		SessionMode: "chat",
		RuntimeType: "model",
		Visibility:  "user",
		Title:       "Restored",
	}}
	start := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	var messages []dbsqlc.ListAllMessagesForBackupRow
	for position := int64(1); position <= 6; position++ {
		turnID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		for seq, role := range []string{"user", "assistant"} {
			// Stored as jsonb, {"role": "<role>", "content": "<text>"} is 500 bytes.
			content, _ := json.Marshal(map[string]string{"content": strings.Repeat("x", 500-27-len(role)), "role": role})
			messages = append(messages, dbsqlc.ListAllMessagesForBackupRow{
				ID:             pgtype.UUID{Bytes: uuid.New(), Valid: true},
				SessionID:      sourceSession,
				Role:           role,
				Content:        content,
				Metadata:       []byte(`{}`),
				SessionMode:    "chat",
				RuntimeType:    "model",
				CreatedAt:      pgtype.Timestamptz{Time: start.Add(time.Duration(len(messages)) * time.Second), Valid: true},
				TurnID:         turnID,
				TurnPosition:   pgtype.Int8{Int64: position, Valid: true},
				TurnMessageSeq: pgtype.Int8{Int64: int64(seq + 1), Valid: true},
				TurnVisible:    true,
			})
		}
	}
	entries := map[string]backupZipEntry{}
	for path, value := range map[string]any{"history/sessions.json": sessions, "history/messages.json": messages} {
		raw, err := marshalJSON(value)
		if err != nil {
			t.Fatal(err)
		}
		entries[path] = backupZipEntry{data: raw}
	}

	svc := &Service{logger: slog.Default(), db: pool, queries: postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))}
	if err := svc.restoreHistory(ctx, userID, botID, &importState{entries: entries, counts: map[Section]int{}}, false, false); err != nil {
		t.Fatalf("restore history: %v", err)
	}

	var restored string
	var origin, first, last pgtype.Timestamptz
	var omitted int64
	if err := pool.QueryRow(ctx, `
		SELECT id::text, history_origin_at, history_omitted_count, history_omitted_first_at, history_omitted_last_at
		FROM bot_sessions WHERE bot_id = $1 AND deleted_at IS NULL`, botID).Scan(&restored, &origin, &omitted, &first, &last); err != nil {
		t.Fatalf("read restored session: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT created_at, octet_length(content::text) FROM bot_history_messages WHERE session_id = $1 AND turn_visible ORDER BY created_at`, restored)
	if err != nil {
		t.Fatal(err)
	}
	var created []time.Time
	for rows.Next() {
		var at time.Time
		var size int
		if err := rows.Scan(&at, &size); err != nil {
			t.Fatal(err)
		}
		if size != 500 {
			t.Fatalf("restored row holds %d bytes, want 500", size)
		}
		created = append(created, at)
	}
	rows.Close()
	if len(created) != len(messages) {
		t.Fatalf("restored %d visible rows, want %d", len(created), len(messages))
	}
	for i, message := range messages {
		if !created[i].Equal(message.CreatedAt.Time) {
			t.Fatalf("restored row %d created at %v, want its original %v", i, created[i], message.CreatedAt.Time)
		}
	}
	if !origin.Valid || !origin.Time.Equal(created[8]) || omitted != 8 || !first.Time.Equal(created[0]) || !last.Time.Equal(created[7]) {
		t.Fatalf("restored origin = %v leaving out %d rows from %v to %v, want turn 5 at %v leaving out the 8 rows of turns 1-4", origin, omitted, first, last, created[8])
	}
}

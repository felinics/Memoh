//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestContextTrajectoryHistoryCleanup(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(conn)
	for _, action := range []string{"clear_session", "clear_bot", "delete_session", "delete_bot_sessions", "runtime_reset"} {
		t.Run(action, func(t *testing.T) {
			bot := mustParseLifecycleUUID(t, uuid.NewString())
			session := mustParseLifecycleUUID(t, uuid.NewString())
			otherSession := mustParseLifecycleUUID(t, uuid.NewString())
			_, err := conn.Exec(ctx, `
WITH principal AS (
 INSERT INTO users (username, is_active, metadata) VALUES ($5, true, '{}') RETURNING id
), membership AS (
 INSERT INTO team_members (team_id, user_id) SELECT $1, id FROM principal RETURNING user_id
), bot AS (
 INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
 SELECT $2, $1, user_id, 'trajectory-' || left($2::uuid::text, 8), 'ready', '{}' FROM membership RETURNING id
)
INSERT INTO bot_sessions (id, bot_id, team_id)
SELECT session.id, bot.id, $1 FROM bot, (VALUES ($3::uuid), ($4::uuid)) AS session(id)
`, team.DefaultTeamID, bot, session, otherSession, "trajectory-"+action)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []pgtype.UUID{session, otherSession} {
				_, err := queries.AppendContextTrajectoryEvent(ctx, sqlc.AppendContextTrajectoryEventParams{
					BotID: bot, SessionID: id, RunID: mustParseLifecycleUUID(t, uuid.NewString()),
					CaptureID: mustParseLifecycleUUID(t, uuid.NewString()), Sequence: 1,
					Event:         []byte(`{"stage":"trigger","blocks":[{"chunks":["shared"]}]}`),
					ContentHashes: []string{"shared"}, Contents: [][]byte{[]byte("complete shared content")},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			remaining := 0
			switch action {
			case "clear_session":
				err, remaining = queries.ClearHistoryBySession(ctx, session), 1
			case "clear_bot":
				err = queries.ClearHistoryByBot(ctx, bot)
			case "delete_session":
				err, remaining = queries.SoftDeleteSession(ctx, session), 1
			case "delete_bot_sessions":
				err = queries.SoftDeleteSessionsByBot(ctx, bot)
			case "runtime_reset":
				err = queries.ClearBotRuntimeData(ctx, bot)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"context_trajectory_events", "context_trajectory_contents"} {
				var count int
				if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE bot_id = $1", bot).Scan(&count); err != nil || count != remaining {
					t.Errorf("%s after %s = %d, expected %d: %v", table, action, count, remaining, err)
				}
				if remaining > 0 {
					if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE bot_id = $1 AND session_id = $2", bot, otherSession).Scan(&count); err != nil || count != 1 {
						t.Errorf("another session lost %s: %d, %v", table, count, err)
					}
				}
			}
		})
	}
}

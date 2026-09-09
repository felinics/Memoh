//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

func TestContextTrajectoryResetRaces(t *testing.T) {
	for _, action := range []string{"clear_session", "clear_bot", "delete_session", "delete_bot_sessions"} {
		for _, writerFirst := range []bool{true, false} {
			name := action + "/reset_first"
			if writerFirst {
				name = action + "/writer_first"
			}
			t.Run(name, func(t *testing.T) {
				pool := freshMigratedDB(t)
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				cfg := pool.Config()
				cfg.ConnConfig.RuntimeParams["application_name"] = "trajectory-race-" + uuid.NewString()
				cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
					_, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID)
					return err
				}
				scoped, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer scoped.Close()
				bot := mustParseLifecycleUUID(t, uuid.NewString())
				session := mustParseLifecycleUUID(t, uuid.NewString())
				_, err = scoped.Exec(ctx, `
WITH principal AS (
 INSERT INTO users (username, is_active, metadata) VALUES ('trajectory-race', true, '{}') RETURNING id
), membership AS (
 INSERT INTO team_members (team_id, user_id) SELECT $1, id FROM principal RETURNING user_id
), bot AS (
 INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
 SELECT $2, $1, user_id, 'trajectory-race', 'ready', '{}' FROM membership RETURNING id
)
INSERT INTO bot_sessions (id, bot_id, team_id) SELECT $3, bot.id, $1 FROM bot
`, team.DefaultTeamID, bot, session)
				if err != nil {
					t.Fatal(err)
				}
				queries := postgresstore.NewQueriesWithPool(scoped, sqlc.New(scoped))
				input := sqlc.AppendContextTrajectoryEventParams{
					BotID: bot, SessionID: session, RunID: mustParseLifecycleUUID(t, uuid.NewString()),
					CaptureID: mustParseLifecycleUUID(t, uuid.NewString()), Sequence: 1,
					Event:         []byte(`{"stage":"wire_request","blocks":[{"chunks":["body"]}]}`),
					ContentHashes: []string{"body"}, Contents: [][]byte{[]byte("complete request")},
				}
				reset := func(q *postgresstore.Queries) error {
					switch action {
					case "clear_bot":
						return q.ClearHistoryByBot(ctx, bot)
					case "delete_session":
						return q.SoftDeleteSession(ctx, session)
					case "delete_bot_sessions":
						return q.SoftDeleteSessionsByBot(ctx, bot)
					default:
						return q.ClearHistoryBySession(ctx, session)
					}
				}
				tx, err := scoped.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				first := postgresstore.NewQueries(sqlc.New(tx))
				done := make(chan error, 1)
				if writerFirst {
					if _, err := first.AppendContextTrajectoryEvent(ctx, input); err != nil {
						t.Fatal(err)
					}
					go func() { done <- reset(queries) }()
				} else {
					if err := reset(first); err != nil {
						t.Fatal(err)
					}
					go func() {
						_, err := queries.AppendContextTrajectoryEvent(ctx, input)
						done <- err
					}()
				}
				waiting := false
				for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
					err := scoped.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_stat_activity WHERE application_name = $1 AND wait_event_type = 'Lock'
)`, cfg.ConnConfig.RuntimeParams["application_name"]).Scan(&waiting)
					if err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if !waiting {
					t.Fatal("competing operation did not wait for ownership lock")
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				err = <-done
				if writerFirst && err != nil {
					t.Fatalf("reset after capture commit: %v", err)
				}
				if !writerFirst && !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("stale capture after reset = %v", err)
				}
				for _, table := range []string{"context_trajectory_events", "context_trajectory_contents"} {
					var count int
					if err := scoped.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
						t.Errorf("reset retained %s: %d, %v", table, count, err)
					}
				}
			})
		}
	}
}

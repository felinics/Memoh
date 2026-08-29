package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresHistoryMutationsAdvanceCompactionEpoch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, pgx.Tx, claimedCompactionFixture) error
	}{
		{
			name: "supersede turn",
			mutate: func(ctx context.Context, tx pgx.Tx, fixture claimedCompactionFixture) error {
				queries := dbsqlc.New(tx)
				_, err := queries.SupersedeHistoryTurn(ctx, dbsqlc.SupersedeHistoryTurnParams{
					SupersededByTurnID: mustTestUUID(t, uuid.NewString()),
					SupersededAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
					SupersededReason:   pgtype.Text{String: "test", Valid: true},
					OldTurnID:          mustTestUUID(t, fixture.turn.ID),
					SessionID:          mustTestUUID(t, postgresMessageTestSessionID),
				})
				return err
			},
		},
		{
			name: "hide turn",
			mutate: func(ctx context.Context, tx pgx.Tx, fixture claimedCompactionFixture) error {
				return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, fixture.turn.ID))
			},
		},
		{
			name: "delete source",
			mutate: func(ctx context.Context, tx pgx.Tx, fixture claimedCompactionFixture) error {
				return dbsqlc.New(tx).DeleteMessagesByIDs(ctx, []pgtype.UUID{mustTestUUID(t, fixture.assistant.ID)})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			tx := beginPostgresMessageTestTx(t, ctx)
			setupPostgresMessageTestFixtures(t, ctx, tx)
			fixture := setupClaimedCompactionFixture(t, ctx, tx)

			if err := test.mutate(ctx, tx, fixture); err != nil {
				t.Fatalf("mutate claimed history: %v", err)
			}
			assertSessionCompactionEpoch(t, ctx, tx, 1)

			lineage, err := dbsqlc.New(tx).ListCompactionArtifactLineageBySession(ctx, mustTestUUID(t, postgresMessageTestSessionID))
			if err != nil {
				t.Fatalf("list active compaction lineage: %v", err)
			}
			if len(lineage) != 0 {
				t.Fatalf("stale compaction lineage remained active: %#v", lineage)
			}

			if err := dbsqlc.New(tx).DeleteMessagesByIDs(ctx, []pgtype.UUID{mustTestUUID(t, fixture.user.ID)}); err != nil {
				t.Fatalf("delete stale claimed source: %v", err)
			}
			assertSessionCompactionEpoch(t, ctx, tx, 1)
		})
	}
}

func TestPostgresStaleCompactionAttemptCannotCompleteOK(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	user, assistant, turn := persistPostgresTurn(t, ctx, svc)
	queries := dbsqlc.New(tx)
	log, err := queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{
		BotID:     mustTestUUID(t, postgresMessageTestBotID),
		SessionID: mustTestUUID(t, postgresMessageTestSessionID),
	})
	if err != nil {
		t.Fatalf("create compaction log: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bot_history_messages
		SET compact_id = $1
		WHERE id = ANY($2::uuid[])
	`, log.ID, []string{user.ID, assistant.ID}); err != nil {
		t.Fatalf("claim compaction sources: %v", err)
	}
	if err := queries.HideMessagesByHistoryTurn(ctx, mustTestUUID(t, turn.ID)); err != nil {
		t.Fatalf("hide claimed turn: %v", err)
	}
	assertSessionCompactionEpoch(t, ctx, tx, 1)
	if _, err := queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{
		BotID:         mustTestUUID(t, postgresMessageTestBotID),
		SessionID:     mustTestUUID(t, postgresMessageTestSessionID),
		ExpectedEpoch: 0,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("create compaction log with stale epoch = %v, want pgx.ErrNoRows", err)
	}
	if _, err := queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{
		BotID:         mustTestUUID(t, postgresMessageTestBotID),
		SessionID:     mustTestUUID(t, postgresMessageTestSessionID),
		ExpectedEpoch: 1,
	}); err != nil {
		t.Fatalf("create compaction log with current epoch: %v", err)
	}

	complete := dbsqlc.CompleteCompactionLogParams{
		ID:           log.ID,
		Status:       "ok",
		Summary:      "stale summary",
		MessageCount: 2,
		Usage:        []byte(`{}`),
		Coverage:     []byte(`[]`),
	}
	if _, err := queries.CompleteCompactionLog(ctx, complete); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("complete stale compaction = %v, want pgx.ErrNoRows", err)
	}

	complete.Status = "error"
	complete.Summary = ""
	complete.ErrorMessage = "history source changed"
	row, err := queries.CompleteCompactionLog(ctx, complete)
	if err != nil {
		t.Fatalf("record stale compaction error: %v", err)
	}
	if row.Status != "error" {
		t.Fatalf("stale compaction status = %q, want error", row.Status)
	}
}

type claimedCompactionFixture struct {
	user      Message
	assistant Message
	turn      HistoryTurn
}

func setupClaimedCompactionFixture(t *testing.T, ctx context.Context, tx pgx.Tx) claimedCompactionFixture {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	user, assistant, turn := persistPostgresTurn(t, ctx, svc)
	artifactID := mustTestUUID(t, uuid.NewString())
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (
			id, bot_id, session_id, status, summary, message_count, completed_at
		)
		VALUES ($1, $2, $3, 'ok', 'claimed summary', 2, now())
	`, artifactID, postgresMessageTestBotID, postgresMessageTestSessionID); err != nil {
		t.Fatalf("insert compaction artifact: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bot_history_messages
		SET compact_id = $1
		WHERE id = ANY($2::uuid[])
	`, artifactID, []string{user.ID, assistant.ID}); err != nil {
		t.Fatalf("link compaction sources: %v", err)
	}
	return claimedCompactionFixture{user: user, assistant: assistant, turn: turn}
}

func persistPostgresTurn(t *testing.T, ctx context.Context, svc *DBService) (Message, Message, HistoryTurn) {
	t.Helper()
	user, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"hello"}`),
	})
	if err != nil {
		t.Fatalf("persist user: %v", err)
	}
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "assistant",
		Content:   []byte(`{"role":"assistant","content":"answer"}`),
	})
	if err != nil {
		t.Fatalf("persist assistant: %v", err)
	}
	turn, err := svc.GetVisibleTurnByMessage(ctx, postgresMessageTestSessionID, assistant.ID)
	if err != nil {
		t.Fatalf("get history turn: %v", err)
	}
	return user, assistant, turn
}

func assertSessionCompactionEpoch(t *testing.T, ctx context.Context, tx pgx.Tx, want int64) {
	t.Helper()
	var got int64
	if err := tx.QueryRow(ctx, `
		SELECT compaction_epoch
		FROM bot_sessions
		WHERE id = $1
	`, postgresMessageTestSessionID).Scan(&got); err != nil {
		t.Fatalf("read session compaction epoch: %v", err)
	}
	if got != want {
		t.Fatalf("session compaction epoch = %d, want %d", got, want)
	}
}

func mustTestUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return parsed
}

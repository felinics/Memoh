package message

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// Replacing or hiding a turn rewinds the session's compaction to before the
// turn instead of discarding it: the summaries that end before the turn move
// to the new epoch, the one a retired summary superseded is active again,
// and its rows stay summarized. Only the summaries that reach the turn, and
// unfinished attempts, are retired.
func TestPostgresHistoryMutationsRewindCompaction(t *testing.T) {
	for _, tc := range []struct {
		name string
		// mutate changes the earlier turn when early, else the latest one.
		early  bool
		mutate func(context.Context, pgx.Tx, rewindFixture, HistoryTurn) error
		// keepsRequest reports whether a request of the latest turn stays
		// visible: the retried input, kept or copied.
		keepsRequest bool
	}{
		{name: "supersede turn", mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			_, err := dbsqlc.New(tx).SupersedeHistoryTurn(ctx, dbsqlc.SupersedeHistoryTurnParams{
				SupersededByTurnID: mustTestUUID(t, uuid.NewString()),
				SupersededAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				SupersededReason:   pgtype.Text{String: "test", Valid: true},
				OldTurnID:          mustTestUUID(t, turn.ID),
				SessionID:          mustTestUUID(t, postgresMessageTestSessionID),
			})
			return err
		}},
		{name: "hide turn", mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, turn.ID))
		}},
		{name: "retry turn", keepsRequest: true, mutate: func(ctx context.Context, tx pgx.Tx, f rewindFixture, turn HistoryTurn) error {
			replacement, err := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx))).Persist(ctx, PersistInput{
				BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "assistant",
				Content: []byte(`{"role":"assistant","content":"retried answer"}`), SkipHistoryTurn: true,
			})
			if err != nil {
				return err
			}
			_, err = dbsqlc.New(tx).ReplaceHistoryTurn(ctx, dbsqlc.ReplaceHistoryTurnParams{
				OldTurnID:           mustTestUUID(t, turn.ID),
				SessionID:           mustTestUUID(t, postgresMessageTestSessionID),
				ReplacementTurnID:   mustTestUUID(t, uuid.NewString()),
				ReplacementPosition: turn.Position + 1,
				RequestMessageID:    mustTestUUID(t, f.latestRequest),
				AssistantMessageID:  mustTestUUID(t, replacement.ID),
				SupersededAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				SupersededReason:    pgtype.Text{String: "retry", Valid: true},
			})
			return err
		}},
		{name: "bound retry", mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			// A retried run writes its own copy of the input as the new
			// turn's request before it selects the replacement.
			position := turn.Position + 1
			replacementTurnID := uuid.NewString()
			if _, err := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx))).Persist(ctx, PersistInput{
				BotID: postgresMessageTestBotID, SessionID: postgresMessageTestSessionID, Role: "user",
				Content: []byte(`{"role":"user","content":"hello"}`), TurnID: replacementTurnID, TurnPosition: &position,
			}); err != nil {
				return err
			}
			_, err := dbsqlc.New(tx).SelectBoundHistoryTurnReplacement(ctx, dbsqlc.SelectBoundHistoryTurnReplacementParams{
				BotID: mustTestUUID(t, postgresMessageTestBotID), SessionID: mustTestUUID(t, postgresMessageTestSessionID),
				OldTurnID: mustTestUUID(t, turn.ID), ReplacementTurnID: mustTestUUID(t, replacementTurnID),
				ReplacementPosition: position, Reason: pgtype.Text{String: "retry", Valid: true},
			})
			return err
		}, keepsRequest: true},
		{name: "hide an earlier turn", early: true, mutate: func(ctx context.Context, tx pgx.Tx, _ rewindFixture, turn HistoryTurn) error {
			return dbsqlc.New(tx).HideMessagesByHistoryTurn(ctx, mustTestUUID(t, turn.ID))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := beginPostgresMessageTestTx(t, ctx)
			setupPostgresMessageTestFixtures(t, ctx, tx)
			f := setupRewindFixture(t, ctx, tx)

			turn := f.latest
			if tc.early {
				turn = f.earlier
			}
			if err := tc.mutate(ctx, tx, f, turn); err != nil {
				t.Fatalf("mutate history: %v", err)
			}
			assertSessionCompactionEpoch(t, ctx, tx, 1)
			epochs := map[string]int64{}
			rows, err := tx.Query(ctx, `SELECT id::text, compaction_epoch FROM bot_history_message_compacts WHERE session_id = $1`, postgresMessageTestSessionID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id string
				var epoch int64
				if err := rows.Scan(&id, &epoch); err != nil {
					t.Fatal(err)
				}
				epochs[id] = epoch
			}
			rows.Close()
			lineage, err := dbsqlc.New(tx).ListCompactionArtifactLineageBySession(ctx, mustTestUUID(t, postgresMessageTestSessionID))
			if err != nil {
				t.Fatal(err)
			}
			anchor, err := dbsqlc.New(tx).GetSessionHistoryAnchor(ctx, dbsqlc.GetSessionHistoryAnchorParams{SessionID: mustTestUUID(t, postgresMessageTestSessionID)})
			if err != nil {
				t.Fatal(err)
			}

			if epochs[f.pending] != 0 || epochs[f.failed] != 0 || epochs[f.rolling] != 0 {
				t.Fatalf("epochs = %v, want the rolling summary and unfinished attempts left behind", epochs)
			}
			if tc.early {
				if len(lineage) != 0 || epochs[f.earlierSummary] != 0 {
					t.Fatalf("lineage = %d summaries, epochs = %v, want none kept for a change before every summary", len(lineage), epochs)
				}
				return
			}
			if len(lineage) != 1 || lineage[0].ID.String() != f.earlierSummary || lineage[0].SupersededBy.Valid || lineage[0].SupersededAt.Valid {
				t.Fatalf("lineage = %+v, want the earlier summary alone and active again", lineage)
			}
			want := f.earlierEnd
			if tc.keepsRequest {
				// The retried request is the oldest row no summary replaces.
				if err := tx.QueryRow(ctx, `SELECT MIN(created_at) FROM bot_history_messages
					WHERE session_id = $1 AND turn_visible AND role = 'user' AND created_at > $2`, postgresMessageTestSessionID, f.earlierEnd).Scan(&want); err != nil {
					t.Fatal(err)
				}
			}
			if !anchor.Valid || !anchor.Time.Equal(want) {
				t.Fatalf("anchor = %v, want %v: the earlier turn stays summarized", anchor, want)
			}
		})
	}
}

type rewindFixture struct {
	earlier, latest                          HistoryTurn
	latestRequest                            string
	earlierEnd                               time.Time
	earlierSummary, rolling, pending, failed string
}

// setupRewindFixture persists two turns. The earlier summary covers the
// earlier turn; the rolling summary superseded it and also covers the latest
// turn, as a run that compacted its own turn leaves them.
func setupRewindFixture(t *testing.T, ctx context.Context, tx pgx.Tx) rewindFixture {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	earlierUser, earlierAssistant, earlier := persistPostgresTurn(t, ctx, svc)
	latestUser, latestAssistant, latest := persistPostgresTurn(t, ctx, svc)
	for i, id := range []string{earlierUser.ID, earlierAssistant.ID, latestUser.ID, latestAssistant.ID} {
		if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET created_at = $2 WHERE id = $1`, id, at(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	compact := func(status, summary string, start, end time.Time) string {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, anchor_start_ms, anchor_end_ms)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
			postgresMessageTestBotID, postgresMessageTestSessionID, status, summary, start.UnixMilli(), end.UnixMilli()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f := rewindFixture{
		earlier: earlier, latest: latest, latestRequest: latestUser.ID,
		earlierEnd:     at(2),
		earlierSummary: compact("ok", "earlier summary", at(1), at(2)),
		rolling:        compact("ok", "rolling summary", at(1), at(4)),
		pending:        compact("pending", "", at(1), at(1)),
		failed:         compact("error", "", at(1), at(1)),
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_message_compacts SET superseded_by = $2, superseded_at = now() WHERE id = $1`, f.earlierSummary, f.rolling); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_message_compacts SET parent_ids = ARRAY[$2::uuid] WHERE id = $1`, f.rolling, f.earlierSummary); err != nil {
		t.Fatal(err)
	}
	for compactID, ids := range map[string][]string{f.earlierSummary: {earlierUser.ID, earlierAssistant.ID}, f.rolling: {latestUser.ID, latestAssistant.ID}} {
		if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET compact_id = $1 WHERE id = ANY($2::uuid[])`, compactID, ids); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

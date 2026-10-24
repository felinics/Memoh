package message

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// The compaction anchor is the oldest visible row no usable summary of the
// session's current epoch replaces; every older row is summarized. As of a
// cutoff, only rows before it count, and only summaries that end before it.
func TestPostgresSessionHistoryAnchor(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	queries := dbsqlc.New(tx)
	sessionID, err := dbpkg.ParseUUID(postgresMessageTestSessionID)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(seconds int) time.Time { return base.Add(time.Duration(seconds) * time.Second) }
	anchorAsOf := func(before time.Time) pgtype.Timestamptz {
		t.Helper()
		got, err := queries.GetSessionHistoryAnchor(ctx, dbsqlc.GetSessionHistoryAnchorParams{
			SessionID: sessionID, Before: pgtype.Timestamptz{Time: before, Valid: !before.IsZero()},
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	expect := func(step string, before time.Time, want time.Time) {
		t.Helper()
		if got := anchorAsOf(before); !got.Valid || !got.Time.Equal(want) {
			t.Fatalf("%s: anchor %v, want %v", step, got, want)
		}
	}
	live := time.Time{}

	if got := anchorAsOf(live); got.Valid {
		t.Fatalf("a session without rows anchors at %v, want none", got.Time)
	}

	rows := make([]string, 6)
	for i := range rows {
		rows[i] = insertUnsummarizedTestRow(t, ctx, tx, at(i+1), `{}`, true)
	}
	expect("nothing compacted", live, at(1))

	summary := insertUnsummarizedTestCompact(t, ctx, tx, "ok", "rolling summary")
	setUnsummarizedTestCompact(t, ctx, tx, summary, rows[0], rows[1], rows[2])
	setUnsummarizedTestAnchors(t, ctx, tx, summary, at(1), at(3))
	expect("the oldest rows summarized", live, at(4))

	// Rows a load never reads do not hold the anchor back.
	insertUnsummarizedTestRow(t, ctx, tx, at(0), `{}`, false)
	insertUnsummarizedTestRow(t, ctx, tx, at(0), `{"trigger_mode":"passive_sync"}`, true)
	expect("hidden and passive rows", live, at(4))

	// A summary that is not usable does not replace its rows.
	for _, unusable := range []struct{ status, summary string }{
		{"error", "rolling summary"}, {"pending", ""}, {"ok", "  \n"},
	} {
		compact := insertUnsummarizedTestCompact(t, ctx, tx, unusable.status, unusable.summary)
		setUnsummarizedTestCompact(t, ctx, tx, compact, rows[1])
		expect(unusable.status+" compact", live, at(2))
		setUnsummarizedTestCompact(t, ctx, tx, summary, rows[1])
	}

	// As of a cutoff the summary replaces its rows only if it ends before it.
	expect("a cutoff after the summary", at(5), at(4))
	expect("a cutoff inside the summary", at(3), at(1))

	// Every row summarized: the newest one places the summary.
	setUnsummarizedTestCompact(t, ctx, tx, summary, rows[3], rows[4], rows[5])
	setUnsummarizedTestAnchors(t, ctx, tx, summary, at(1), at(6))
	expect("every row summarized", live, at(6))
	expect("a cutoff before the newest row", at(6), at(1))

	// Rewriting history moves the session past every summary it had.
	if _, err := tx.Exec(ctx, `UPDATE bot_sessions SET compaction_epoch = compaction_epoch + 1 WHERE id = $1`, postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	expect("an earlier epoch's summary", live, at(1))

	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if got := anchorAsOf(live); got.Valid {
		t.Fatalf("another team's session anchors at %v, want none", got.Time)
	}
}

func insertUnsummarizedTestRow(t *testing.T, ctx context.Context, tx pgx.Tx, created time.Time, metadata string, visible bool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
		(id, bot_id, session_id, role, content, metadata, created_at, turn_id, turn_position, turn_message_seq, turn_visible)
		VALUES ($1, $2, $3, 'user', '{"role":"user","content":"row"}'::jsonb, $4::jsonb, $5, gen_random_uuid(), 1, 1, $6)`,
		id, postgresMessageTestBotID, postgresMessageTestSessionID, metadata, created, visible); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertUnsummarizedTestCompact(t *testing.T, ctx context.Context, tx pgx.Tx, status, summary string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary)
		VALUES ($1, $2, $3, $4, $5)`, id, postgresMessageTestBotID, postgresMessageTestSessionID, status, summary); err != nil {
		t.Fatal(err)
	}
	return id
}

func setUnsummarizedTestCompact(t *testing.T, ctx context.Context, tx pgx.Tx, compactID string, rowIDs ...string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET compact_id = $1 WHERE id = ANY($2::uuid[])`, compactID, rowIDs); err != nil {
		t.Fatal(err)
	}
}

func setUnsummarizedTestAnchors(t *testing.T, ctx context.Context, tx pgx.Tx, compactID string, start, end time.Time) {
	t.Helper()
	if _, err := tx.Exec(ctx, `UPDATE bot_history_message_compacts SET anchor_start_ms = $2, anchor_end_ms = $3 WHERE id = $1`,
		compactID, start.UnixMilli(), end.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

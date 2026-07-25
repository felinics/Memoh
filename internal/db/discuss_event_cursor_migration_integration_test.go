//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDiscussEventCursorMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	steps := countMigrationsFrom(t, "0120_discuss_event_cursor.up.sql")

	assertDiscussEventCursorSchema(t, ctx, pool, true, true)
	stepDown(t, dsn, steps)
	assertDiscussEventCursorSchema(t, ctx, pool, false, false)
	stepUp(t, dsn, steps)
	assertDiscussEventCursorSchema(t, ctx, pool, true, true)
}

// TestDiscussEventCursorSequenceSeededAboveClock pins the invariant the cursor
// domain rests on: a freshly migrated deployment must never hand out cursors
// that collide with the source-time watermarks of pre-migration history.
func TestDiscussEventCursorSequenceSeededAboveClock(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)

	var seeded, clockMs int64
	if err := pool.QueryRow(ctx, `
		SELECT last_value, FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint
		FROM bot_session_event_cursor_seq
	`).Scan(&seeded, &clockMs); err != nil {
		t.Fatalf("inspect session event cursor sequence: %v", err)
	}
	if seeded < clockMs-60_000 {
		t.Fatalf("sequence seeded at %d, want at least the migration-time clock %d", seeded, clockMs)
	}
	if seeded >= 9007199254740991 {
		t.Fatalf("sequence seeded at %d, want below the JSON-safe ceiling", seeded)
	}
}

func assertDiscussEventCursorSchema(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	wantConsumedEventCursor bool,
	wantSequence bool,
) {
	t.Helper()
	var hasConsumedEventCursor, hasSequence bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS (
		    SELECT 1
		    FROM information_schema.columns
		    WHERE table_schema = 'public'
		      AND table_name = 'bot_session_discuss_cursors'
		      AND column_name = 'consumed_event_cursor'
		  ),
		  to_regclass('public.bot_session_event_cursor_seq') IS NOT NULL
	`).Scan(&hasConsumedEventCursor, &hasSequence); err != nil {
		t.Fatalf("inspect discuss event cursor schema: %v", err)
	}
	if hasConsumedEventCursor != wantConsumedEventCursor || hasSequence != wantSequence {
		t.Fatalf(
			"discuss event cursor schema = column:%t sequence:%t, want column:%t sequence:%t",
			hasConsumedEventCursor,
			hasSequence,
			wantConsumedEventCursor,
			wantSequence,
		)
	}
}

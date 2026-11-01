//go:build integration

package db_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// historyCheckpointTables are the tables 0170 converts sessions in and the
// one it creates, each under FORCE row level security once it ran.
var historyCheckpointTables = []string{
	"bot_sessions", "bot_history_messages", "bot_history_message_compacts", "bot_history_checkpoints",
}

// 0170 turns what each session renders before its context into a
// checkpoint: its active summaries, with the rows in their span they left
// raw. It does so as an owner that FORCE row level security binds, and puts
// that back. Its down restores the overlay with every summary retired. Both
// run again without effect, and the canonical init defines the same schema
// as the chain.
func TestHistoryCheckpointMigration(t *testing.T) {
	ctx := context.Background()
	dsn := limitedOwnerDSN(t)
	// Only a schema rolled back from the head is one that 0170 converts.
	migrateUpAll(t, dsn)
	migrateTo(t, dsn, 169)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the limited owner: %v", err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("set team context: %v", err)
	}
	exec := func(label, query string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}

	userID := uuid.NewString()
	exec("create user", `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, "history-checkpoint-"+userID)
	botID := uuid.NewString()
	exec("create bot", `INSERT INTO bots (id, team_id, owner_user_id, name) VALUES ($1, $2, $3, $4)`,
		botID, team.DefaultTeamID, userID, "history-checkpoint-"+botID)
	insertSession := func(epoch int64) string {
		t.Helper()
		sessionID := uuid.NewString()
		exec("create session", `INSERT INTO bot_sessions (id, bot_id, compaction_epoch) VALUES ($1, $2, $3)`, sessionID, botID, epoch)
		return sessionID
	}
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	type seeded struct {
		ids     []string
		created []time.Time
	}
	// seedTurns gives a session visible turns of a user row and an answer, a
	// second apart, oldest first.
	seedTurns := func(sessionID string, turns int) seeded {
		t.Helper()
		var rows seeded
		for position := 1; position <= turns; position++ {
			turnID := uuid.NewString()
			for seq, role := range []string{"user", "assistant"} {
				id := uuid.NewString()
				at := start.Add(time.Duration(len(rows.ids)) * time.Second)
				exec("create history row", `
					INSERT INTO bot_history_messages (id, bot_id, session_id, role, content, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
					VALUES ($1, $2, $3, $4::text, jsonb_build_object('role', $4::text, 'content', 'x'), $5, $6, $7, true, $8)
				`, id, botID, sessionID, role, turnID, position, seq+1, at)
				rows.ids = append(rows.ids, id)
				rows.created = append(rows.created, at)
			}
		}
		return rows
	}
	insertSummary := func(sessionID, summary string, epoch int64) string {
		t.Helper()
		id := uuid.NewString()
		exec("create summary", `
			INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, compaction_epoch, completed_at)
			VALUES ($1, $2, $3, 'ok', $4, $5, now())
		`, id, botID, sessionID, summary, epoch)
		return id
	}
	claim := func(summaryID string, rows ...string) {
		t.Helper()
		exec("claim rows", `UPDATE bot_history_messages SET compact_id = $1 WHERE id = ANY($2::uuid[])`, summaryID, rows)
	}

	// Summaries over raw turns: a superseded summary's rows belong to the
	// summary that superseded it, and the rows no summary replaced, before
	// the summary and the user row of turn 4, stay raw where they are.
	layered := insertSession(0)
	layeredRows := seedTurns(layered, 5)
	active := insertSummary(layered, "turns three and four", 0)
	superseded := insertSummary(layered, "turn three", 0)
	exec("supersede", `UPDATE bot_history_message_compacts SET superseded_by = $1, superseded_at = now() WHERE id = $2`, active, superseded)
	claim(superseded, layeredRows.ids[4], layeredRows.ids[5])
	claim(active, layeredRows.ids[7])

	// A summary retired by an epoch and a blank one render nothing, and a
	// legacy passive_sync row is not context.
	retiring := insertSession(1)
	retiringRows := seedTurns(retiring, 3)
	exec("passive row", `
		INSERT INTO bot_history_messages (bot_id, session_id, role, content, metadata, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
		SELECT bot_id, session_id, 'user', content, '{"trigger_mode":"passive_sync"}'::jsonb, turn_id, 1, 3, true, created_at
		FROM bot_history_messages WHERE id = $1
	`, retiringRows.ids[0])
	claim(insertSummary(retiring, "retired", 0), retiringRows.ids[0], retiringRows.ids[1])
	usable := insertSummary(retiring, "turn two", 1)
	claim(usable, retiringRows.ids[2], retiringRows.ids[3])
	insertSummary(retiring, " \n\t", 1)

	// A summary none of whose rows is still visible renders first, before the
	// whole history.
	hidden := insertSession(0)
	hiddenRows := seedTurns(hidden, 2)
	exec("hide turn", `UPDATE bot_history_messages SET turn_visible = false WHERE id = ANY($1::uuid[])`, hiddenRows.ids[:2])
	orphan := insertSummary(hidden, "hidden turn", 0)
	claim(orphan, hiddenRows.ids[0], hiddenRows.ids[1])

	plain := insertSession(0)
	seedTurns(plain, 2)

	type checkpoint struct {
		parent    pgtype.UUID
		turn, seq int64
		items     []map[string]string
	}
	readChain := func(sessionID string) []checkpoint {
		t.Helper()
		var chain []checkpoint
		var next pgtype.UUID
		if err := conn.QueryRow(ctx, `SELECT history_checkpoint_id FROM bot_sessions WHERE id = $1`, sessionID).Scan(&next); err != nil {
			t.Fatalf("read active checkpoint: %v", err)
		}
		for next.Valid {
			var got checkpoint
			var items []byte
			if err := conn.QueryRow(ctx, `
				SELECT parent_id, turn_position, turn_message_seq, items
				FROM bot_history_checkpoints WHERE id = $1 AND session_id = $2
			`, next, sessionID).Scan(&got.parent, &got.turn, &got.seq, &items); err != nil {
				t.Fatalf("read checkpoint: %v", err)
			}
			if err := json.Unmarshal(items, &got.items); err != nil {
				t.Fatalf("decode items: %v", err)
			}
			chain = append(chain, got)
			next = got.parent
		}
		return chain
	}
	sameCheckpoint := func(got, want checkpoint) bool {
		return got.turn == want.turn && got.seq == want.seq &&
			slices.EqualFunc(got.items, want.items, func(a, b map[string]string) bool { return a["row"] == b["row"] && a["summary"] == b["summary"] })
	}
	wantChain := func(label, sessionID string, want ...checkpoint) {
		t.Helper()
		got := readChain(sessionID)
		if !slices.EqualFunc(got, want, sameCheckpoint) {
			t.Errorf("%s: chain %+v, want %+v", label, got, want)
		}
	}
	row := func(id string) map[string]string { return map[string]string{"row": id} }
	summary := func(id string) map[string]string { return map[string]string{"summary": id} }
	assertCheckpoints := func() {
		t.Helper()
		wantChain("summaries over raw turns", layered,
			checkpoint{turn: 4, seq: 2, items: []map[string]string{
				row(layeredRows.ids[0]), row(layeredRows.ids[1]), row(layeredRows.ids[2]), row(layeredRows.ids[3]),
				row(layeredRows.ids[6]), summary(active),
			}})
		wantChain("a retired and a usable summary", retiring,
			checkpoint{turn: 2, seq: 2, items: []map[string]string{row(retiringRows.ids[0]), row(retiringRows.ids[1]), summary(usable)}})
		wantChain("summary of hidden rows", hidden, checkpoint{items: []map[string]string{summary(orphan)}})
		wantChain("nothing before its context", plain)
	}

	migrateUpAll(t, dsn)
	assertCheckpoints()
	assertForcedRLS(t, ctx, pool, historyCheckpointTables...)
	var leftovers int
	if err := conn.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM information_schema.columns
		   WHERE table_schema = 'public'
		     AND (table_name, column_name) IN (
		       ('bot_history_messages', 'compact_id'),
		       ('bot_sessions', 'compaction_epoch'),
		       ('bot_history_message_compacts', 'coverage'), ('bot_history_message_compacts', 'superseded_by'),
		       ('bot_history_message_compacts', 'compaction_epoch')))
	`).Scan(&leftovers); err != nil {
		t.Fatalf("inspect the overlay after up: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("after up %d pieces of the overlay are left", leftovers)
	}

	migrations := postgresMigrationsFS(t)
	up, err := fs.ReadFile(migrations, "0170_history_checkpoints.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec("re-apply 0170 up", string(up))
	assertCheckpoints()
	assertForcedRLS(t, ctx, pool, historyCheckpointTables...)

	assertOverlay := func() {
		t.Helper()
		var checkpoints bool
		var claims, epochs int
		if err := conn.QueryRow(ctx, `
			SELECT
			  to_regclass('public.bot_history_checkpoints') IS NOT NULL,
			  (SELECT count(*) FROM bot_history_messages WHERE bot_id = $1 AND compact_id IS NOT NULL),
			  (SELECT count(*) FROM bot_sessions WHERE bot_id = $1 AND compaction_epoch = 1)
		`, botID).Scan(&checkpoints, &claims, &epochs); err != nil {
			t.Fatalf("inspect the overlay after down: %v", err)
		}
		// Every session with a summary moved past its epoch.
		if checkpoints || claims != 0 || epochs != 3 {
			t.Errorf("after down: checkpoints table %v, %d claimed rows, %d sessions past their summaries; want none, 0, 3",
				checkpoints, claims, epochs)
		}
		assertForcedRLS(t, ctx, pool, "bot_sessions", "bot_history_messages", "bot_history_message_compacts")
	}
	migrateTo(t, dsn, 169)
	assertOverlay()
	down, err := fs.ReadFile(migrations, "0170_history_checkpoints.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec("re-apply 0170 down", string(down))
	assertOverlay()

	// Up again, the retired summaries render nothing, so nothing stands
	// before any session's context.
	stepUp(t, dsn, 1)
	for _, sessionID := range []string{layered, retiring, hidden, plain} {
		wantChain("after down and up", sessionID)
	}
	assertForcedRLS(t, ctx, pool, historyCheckpointTables...)
	migrateUpAll(t, dsn)

	chain := historyCheckpointSchema(t, ctx, pool)
	t.Run("canonical init defines the same history checkpoints", func(t *testing.T) {
		dsn := teamMigrationDSN(t)
		canonical := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		if got := historyCheckpointSchema(t, ctx, canonical); !slices.Equal(got, chain) {
			t.Fatalf("canonical init defines\n%q\nthe chain defines\n%q", got, chain)
		}
	})
}

// historyCheckpointSchema is how a database defines history checkpoints and
// what they replaced: the checkpoint tables and the tables the overlay lived
// in, with their columns, constraints and indexes, the checkpoint policies,
// the functions that move a session's checkpoint, the triggers on history
// rows, and the visible-history view the rest reads.
func historyCheckpointSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	var definitions []string
	if err := pool.QueryRow(ctx, `
		WITH tables AS (
		  SELECT name, ('public.' || name)::regclass AS oid
		  FROM unnest(ARRAY[
		    'bot_history_checkpoints', 'bot_sessions', 'bot_history_messages', 'bot_history_message_compacts'
		  ]) AS name
		)
		SELECT ARRAY(
		  SELECT a.attrelid::regclass::text || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
		    || CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
		    || COALESCE(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), '')
		  FROM pg_attribute a
		  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE a.attrelid IN (SELECT oid FROM tables)
		    AND a.attnum > 0
		    AND NOT a.attisdropped
		  ORDER BY 1
		) || ARRAY(
		  -- Older migrations add references to tables under FORCE row level
		  -- security NOT VALID where the canonical init declares them plainly;
		  -- whether a reference was validated is not what is compared here.
		  SELECT c.conrelid::regclass::text || '.' || c.conname || ' '
		    || regexp_replace(pg_get_constraintdef(c.oid), ' NOT VALID$', '')
		  FROM pg_constraint c
		  WHERE c.conrelid IN (SELECT oid FROM tables)
		  ORDER BY 1
		) || ARRAY(
		  SELECT indexdef
		  FROM pg_indexes
		  WHERE schemaname = 'public'
		    AND tablename IN (SELECT name FROM tables)
		  ORDER BY indexname
		) || ARRAY(
		  SELECT policyname || ' ' || cmd || ' ' || COALESCE(qual, '') || ' ' || COALESCE(with_check, '')
		  FROM pg_policies
		  WHERE schemaname = 'public' AND tablename = 'bot_history_checkpoints'
		  ORDER BY policyname
		) || ARRAY(
		  SELECT pg_get_functiondef(p.oid)
		  FROM pg_proc p
		  WHERE p.pronamespace = 'public'::regnamespace
		    AND p.proname IN ('memoh_history_checkpoint_kept', 'memoh_rewind_history_checkpoint')
		  ORDER BY p.proname
		) || ARRAY(
		  SELECT pg_get_triggerdef(tg.oid)
		  FROM pg_trigger tg
		  WHERE tg.tgrelid = 'public.bot_history_messages'::regclass
		    AND NOT tg.tgisinternal
		  ORDER BY tg.tgname
		) || ARRAY[pg_get_viewdef('public.bot_visible_history_messages'::regclass)]
	`).Scan(&definitions); err != nil {
		t.Fatalf("read the history checkpoint schema: %v", err)
	}
	return definitions
}

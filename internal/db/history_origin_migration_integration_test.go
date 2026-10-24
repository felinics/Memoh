//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// historyOriginTables are the tables 0172 reads or writes while it places
// origins, each under FORCE row level security before and after.
var historyOriginTables = []string{
	"bots", "models", "bot_sessions", "bot_session_events",
	"bot_history_messages", "bot_history_message_compacts", "context_lifecycles",
}

// 0172 places a history origin for every live session that exists when it
// runs and for no session created after: for the budget the session's runs
// planned, else half its model's context window up to the default cap, else
// half the cap. It does so as an owner that FORCE row level security binds,
// and puts that back. Its down removes the origin and restores 0171's
// compaction rewind. Both run again without effect, and the canonical init
// defines the same schema as the chain.
func TestHistoryOriginMigration(t *testing.T) {
	ctx := context.Background()
	dsn := limitedOwnerDSN(t)
	// The canonical init already defines the origin, so only a schema rolled
	// back from the head is one that 0172 upgrades.
	migrateUpAll(t, dsn)
	migrateTo(t, dsn, 171)

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

	var rewind171 string
	if err := conn.QueryRow(ctx, `SELECT pg_get_functiondef('public.memoh_rewind_compaction(uuid, timestamptz)'::regprocedure)`).Scan(&rewind171); err != nil {
		t.Fatalf("read the 0171 compaction rewind: %v", err)
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
	`, userID, "history-origin-"+userID)
	providerID := uuid.NewString()
	exec("create provider", `INSERT INTO providers (id, name) VALUES ($1, $2)`, providerID, "history-origin-"+providerID)
	insertModel := func(contextWindow int) string {
		t.Helper()
		modelID := uuid.NewString()
		exec("create model", `
			INSERT INTO models (id, provider_id, model_id, name, type, config, team_id)
			VALUES ($1, $2, $3, $3, 'chat', jsonb_build_object('context_window', $4::int), $5)
		`, modelID, providerID, "history-origin-"+modelID, contextWindow, team.DefaultTeamID)
		return modelID
	}
	insertBot := func(chatModelID string) string {
		t.Helper()
		botID := uuid.NewString()
		var model any
		if chatModelID != "" {
			model = chatModelID
		}
		exec("create bot", `
			INSERT INTO bots (id, team_id, owner_user_id, name, chat_model_id)
			VALUES ($1, $2, $3, $4, $5)
		`, botID, team.DefaultTeamID, userID, "history-origin-"+botID, model)
		return botID
	}
	insertSession := func(botID, preferredModelID string) string {
		t.Helper()
		sessionID := uuid.NewString()
		var model any
		if preferredModelID != "" {
			model = preferredModelID
		}
		exec("create session", `INSERT INTO bot_sessions (id, bot_id, preferred_chat_model_id) VALUES ($1, $2, $3)`, sessionID, botID, model)
		return sessionID
	}
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	// seedTurns gives a session visible turns of a user row and an answer,
	// each of rowBytes stored content bytes and a second after the one
	// before it, and returns their creation times, oldest first.
	seedTurns := func(sessionID, botID string, turns, rowBytes int) []time.Time {
		t.Helper()
		var created []time.Time
		for position := 1; position <= turns; position++ {
			turnID := uuid.NewString()
			for seq, role := range []string{"user", "assistant"} {
				at := start.Add(time.Duration(len(created)) * time.Second)
				exec("create history row", `
					INSERT INTO bot_history_messages (bot_id, session_id, role, content, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
					VALUES ($1, $2, $3::text,
					  jsonb_build_object('role', $3::text, 'content',
					    repeat('x', $4::int - octet_length(jsonb_build_object('role', $3::text, 'content', '')::text))),
					  $5, $6, $7, true, $8)
				`, botID, sessionID, role, rowBytes, turnID, position, seq+1, at)
				created = append(created, at)
			}
		}
		return created
	}

	// A 2000-token window gives a 1000-token budget: 2880 bytes of room hold
	// two of six 1000-byte turns. A 4000-token one holds five.
	smallWindow := insertModel(2000)
	largeWindow := insertModel(4000)
	// A window above the 200000-token cap counts as the cap, as no window
	// does: 288000 bytes of room hold four of six 60000-byte turns.
	hugeWindow := insertModel(1000000)
	smallBot := insertBot(smallWindow)
	hugeBot := insertBot(hugeWindow)
	modelessBot := insertBot("")

	windowed := insertSession(smallBot, "")
	windowedRows := seedTurns(windowed, smallBot, 6, 500)
	preferred := insertSession(smallBot, largeWindow)
	preferredRows := seedTurns(preferred, smallBot, 6, 500)
	// The newest run that planned a budget sets it, here 500 tokens and 1440
	// bytes of room for one turn, over an older plan and a newer run that
	// planned none.
	planned := insertSession(smallBot, "")
	plannedRows := seedTurns(planned, smallBot, 6, 500)
	for age, snapshot := range []string{`{"version": 1}`, `{"budget_plan": {"history_budget": 500}}`, `{"budget_plan": {"history_budget": 100000}}`} {
		exec("record a run", `
			INSERT INTO context_lifecycles (run_id, bot_id, session_id, status, snapshot, created_at)
			VALUES ($1, $2, $3, 'completed', $4::jsonb, $5)
		`, uuid.NewString(), smallBot, planned, snapshot, start.Add(-time.Duration(age+1)*time.Hour))
	}
	capped := insertSession(hugeBot, "")
	cappedRows := seedTurns(capped, hugeBot, 6, 30000)
	modeless := insertSession(modelessBot, "")
	modelessRows := seedTurns(modeless, modelessBot, 6, 30000)
	fitting := insertSession(smallBot, "")
	seedTurns(fitting, smallBot, 1, 500)
	empty := insertSession(smallBot, "")
	deleted := insertSession(smallBot, "")
	seedTurns(deleted, smallBot, 6, 500)
	exec("delete session", `UPDATE bot_sessions SET deleted_at = now() WHERE id = $1`, deleted)

	type origin struct {
		at, first, last pgtype.Timestamptz
		count           int64
	}
	readOrigin := func(sessionID string) origin {
		t.Helper()
		var got origin
		if err := conn.QueryRow(ctx, `
			SELECT history_origin_at, history_omitted_count, history_omitted_first_at, history_omitted_last_at
			FROM bot_sessions WHERE id = $1
		`, sessionID).Scan(&got.at, &got.count, &got.first, &got.last); err != nil {
			t.Fatalf("read history origin: %v", err)
		}
		return got
	}
	// wantOrigin checks that the session's context starts at its row kept,
	// leaving out every row before it.
	wantOrigin := func(label, sessionID string, rows []time.Time, kept int) {
		t.Helper()
		got := readOrigin(sessionID)
		if !got.at.Valid || !got.at.Time.Equal(rows[kept]) ||
			got.count != int64(kept) ||
			!got.first.Valid || !got.first.Time.Equal(rows[0]) ||
			!got.last.Valid || !got.last.Time.Equal(rows[kept-1]) {
			t.Errorf("%s: origin %+v, want %v leaving out %d rows from %v to %v", label, got, rows[kept], kept, rows[0], rows[kept-1])
		}
	}
	wantNoOrigin := func(label, sessionID string) {
		t.Helper()
		if got := readOrigin(sessionID); got != (origin{}) {
			t.Errorf("%s: origin %+v, want none", label, got)
		}
	}
	assertOrigins := func() {
		t.Helper()
		wantOrigin("bot model window", windowed, windowedRows, 8)
		wantOrigin("session model window", preferred, preferredRows, 2)
		wantOrigin("planned budget", planned, plannedRows, 10)
		wantOrigin("capped model window", capped, cappedRows, 4)
		wantOrigin("no model", modeless, modelessRows, 4)
		wantNoOrigin("history that fits", fitting)
		wantNoOrigin("no history", empty)
		wantNoOrigin("deleted session", deleted)
	}

	migrateUpAll(t, dsn)
	assertOrigins()
	assertForcedRLS(t, ctx, pool, historyOriginTables...)

	// A session created after the migration has no origin, and running the
	// migration again places none and moves none.
	later := insertSession(smallBot, "")
	laterRows := seedTurns(later, smallBot, 6, 500)
	migrations := postgresMigrationsFS(t)
	up, err := fs.ReadFile(migrations, "0172_history_origin.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec("re-apply 0172 up", string(up))
	assertOrigins()
	wantNoOrigin("session created after the migration", later)
	assertForcedRLS(t, ctx, pool, historyOriginTables...)

	assertRemoved := func() {
		t.Helper()
		var columns, constraints int
		var placement, budget bool
		var rewind string
		if err := conn.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM information_schema.columns
			   WHERE table_schema = 'public' AND table_name = 'bot_sessions'
			     AND column_name IN ('history_origin_at', 'history_omitted_count', 'history_omitted_first_at', 'history_omitted_last_at')),
			  (SELECT count(*) FROM pg_constraint WHERE conname = 'bot_sessions_history_origin_check'),
			  to_regprocedure('public.memoh_place_history_origin(uuid, bigint)') IS NOT NULL,
			  to_regprocedure('public.memoh_session_history_budget(uuid)') IS NOT NULL,
			  pg_get_functiondef('public.memoh_rewind_compaction(uuid, timestamptz)'::regprocedure)
		`).Scan(&columns, &constraints, &placement, &budget, &rewind); err != nil {
			t.Fatalf("inspect the schema after down: %v", err)
		}
		if columns != 0 || constraints != 0 || placement || budget {
			t.Errorf("after down: %d origin columns, %d checks, placement=%v budget=%v; want none", columns, constraints, placement, budget)
		}
		if rewind != rewind171 {
			t.Errorf("after down the compaction rewind is\n%s\nwant the 0171 one\n%s", rewind, rewind171)
		}
	}
	stepDown(t, dsn, 1)
	assertRemoved()
	down, err := fs.ReadFile(migrations, "0172_history_origin.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec("re-apply 0172 down", string(down))
	assertRemoved()

	// Up again, the session created after the first run is one that exists.
	stepUp(t, dsn, 1)
	assertOrigins()
	wantOrigin("session that exists at the second run", later, laterRows, 8)
	assertForcedRLS(t, ctx, pool, historyOriginTables...)

	chain := historyOriginSchema(t, ctx, pool)
	t.Run("canonical init defines the same history origin", func(t *testing.T) {
		dsn := teamMigrationDSN(t)
		canonical := resetToEmpty(t)
		applyCanonicalInitOnly(t, dsn)
		if got := historyOriginSchema(t, ctx, canonical); !slices.Equal(got, chain) {
			t.Fatalf("canonical init defines\n%q\nthe chain defines\n%q", got, chain)
		}
	})
}

// historyOriginSchema is how a database defines the history origin: the
// bot_sessions columns and their check, and the functions that budget, place
// and honor it.
func historyOriginSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	var definitions []string
	if err := pool.QueryRow(ctx, `
		SELECT ARRAY(
		  SELECT a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
		    || CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
		    || COALESCE(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), '')
		  FROM pg_attribute a
		  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE a.attrelid = 'public.bot_sessions'::regclass
		    AND a.attname IN ('history_origin_at', 'history_omitted_count', 'history_omitted_first_at', 'history_omitted_last_at')
		    AND NOT a.attisdropped
		  ORDER BY a.attname
		) || ARRAY(
		  SELECT c.conname || ' ' || pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  WHERE c.conrelid = 'public.bot_sessions'::regclass
		    AND c.conname = 'bot_sessions_history_origin_check'
		) || ARRAY(
		  SELECT pg_get_functiondef(p.oid)
		  FROM pg_proc p
		  WHERE p.pronamespace = 'public'::regnamespace
		    AND p.proname IN ('memoh_session_history_budget', 'memoh_place_history_origin', 'memoh_rewind_compaction')
		  ORDER BY p.proname
		)
	`).Scan(&definitions); err != nil {
		t.Fatalf("read the history origin schema: %v", err)
	}
	if len(definitions) != 8 {
		t.Fatalf("history origin schema has %d definitions, want 4 columns, a check and 3 functions: %q", len(definitions), definitions)
	}
	return definitions
}

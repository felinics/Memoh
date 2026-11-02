package db

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// TestHistoryCheckpointCandidateQueries exercises the real SQL that decides
// what a compaction pass may fold onto a checkpoint: the rows the checkpoint
// keeps by reference and the context rows after its position, before a
// cutoff when one is set.
// Hidden rows and legacy passive_sync rows are never candidates, and the
// bounded read admits rows oldest first within its byte budget.
func TestHistoryCheckpointCandidateQueries(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	userID, botID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, userID)
	exec(`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, botID, userID)
	exec(`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, sessionID, botID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id = $1`, botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	turns := make(map[int64]string)
	row := func(turn, seq int64, external, metadata string, visible bool) pgtype.UUID {
		t.Helper()
		turnID, ok := turns[turn]
		if !ok {
			turnID = uuid.NewString()
			turns[turn] = turnID
		}
		id := uuid.NewString()
		var source any
		if external != "" {
			source = external
		}
		exec(`INSERT INTO bot_history_messages
			(id, bot_id, session_id, role, content, metadata, source_message_id, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
			VALUES ($1, $2, $3, 'user', '{"role":"user","content":"m"}'::jsonb, $4::jsonb, $5, $6, $7, $8, $9, clock_timestamp())`,
			id, botID, sessionID, metadata, source, turnID, turn, seq, visible)
		return ParseUUIDOrEmpty(id)
	}
	kept := row(1, 1, "", "{}", true)
	row(1, 2, "", "{}", true)
	second, third := row(2, 1, "", "{}", true), row(2, 2, "", "{}", true)
	row(3, 1, "", `{"trigger_mode":"passive_sync"}`, true)
	row(3, 2, "", "{}", false)
	external := row(4, 1, "ext-4", "{}", true)
	row(4, 2, "", "{}", true)

	queries := sqlc.New(pool)
	session := ParseUUIDOrEmpty(sessionID)
	cutoff := func(turn, seq int64) (pgtype.Int8, pgtype.Int8) {
		return pgtype.Int8{Int64: turn, Valid: true}, pgtype.Int8{Int64: seq, Valid: true}
	}
	beforeTurn, beforeSeq := cutoff(4, 2)
	read := func(params sqlc.ListHistoryCheckpointCandidatesWithinBytesParams) []pgtype.UUID {
		t.Helper()
		rows, err := queries.ListHistoryCheckpointCandidatesWithinBytes(ctx, params)
		if err != nil {
			t.Fatalf("read candidates: %v", err)
		}
		ids := make([]pgtype.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids
	}
	all := sqlc.ListHistoryCheckpointCandidatesWithinBytesParams{
		SessionID: session, AfterTurnPosition: 1, AfterTurnMessageSeq: 2, KeptIds: []pgtype.UUID{kept},
		BeforeTurnPosition: beforeTurn, BeforeTurnMessageSeq: beforeSeq, MaxBytes: 1 << 20,
	}
	if got, want := read(all), []pgtype.UUID{kept, second, third, external}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}

	left, err := queries.HasHistoryCheckpointCandidates(ctx, sqlc.HasHistoryCheckpointCandidatesParams{
		SessionID: session, AfterTurnPosition: 1, AfterTurnMessageSeq: 2, KeptIds: []pgtype.UUID{kept},
		BeforeTurnPosition: beforeTurn, BeforeTurnMessageSeq: beforeSeq,
	})
	if err != nil {
		t.Fatalf("look for candidates: %v", err)
	}
	if !left {
		t.Fatal("no candidates reported, want the four")
	}
	if left, err = queries.HasHistoryCheckpointCandidates(ctx, sqlc.HasHistoryCheckpointCandidatesParams{
		SessionID: session, AfterTurnPosition: 4, AfterTurnMessageSeq: 2,
	}); err != nil || left {
		t.Fatalf("candidates after the last row = %v (%v), want none", left, err)
	}
	var largest int64
	if err := pool.QueryRow(ctx, `SELECT max(octet_length(content::text)) FROM bot_history_messages WHERE id = ANY($1)`, []pgtype.UUID{kept, second, third, external}).Scan(&largest); err != nil {
		t.Fatalf("measure the largest candidate: %v", err)
	}

	bounded := all
	bounded.MaxBytes = largest
	rows, err := queries.ListHistoryCheckpointCandidatesWithinBytes(ctx, bounded)
	if err != nil {
		t.Fatalf("bounded read: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != kept || !rows[0].Truncated {
		t.Fatalf("bounded read = %+v, want the oldest candidate, truncated", rows)
	}
	if rows, err := queries.ListHistoryCheckpointCandidatesWithinBytes(ctx, all); err != nil || len(rows) == 0 || rows[0].Truncated {
		t.Fatalf("a read that takes every candidate = %+v (%v), want it untruncated", rows, err)
	}
	bounded.MaxBytes = 1
	if got := read(bounded); len(got) != 0 {
		t.Fatalf("a budget below the oldest candidate admitted %v", got)
	}
	bounded.MaxRowBytes = largest
	if got := read(bounded); !slices.Equal(got, []pgtype.UUID{kept}) {
		t.Fatalf("a row bound that holds the oldest candidate admitted %v, want it alone", got)
	}
	if err := pool.QueryRow(ctx, `SELECT octet_length(content::text) FROM bot_history_messages WHERE id = $1`, kept).Scan(&bounded.MaxBytes); err != nil {
		t.Fatalf("measure the oldest candidate: %v", err)
	}
	if got := read(bounded); !slices.Equal(got, []pgtype.UUID{kept, second}) {
		t.Fatalf("a budget that holds the oldest candidate admitted %v, want it and the one that crosses the budget", got)
	}
}

// TestHistoryCheckpointCandidatesEndAtACallGroup exercises the real SQL that
// reads whole call groups: the group that crosses the budget comes whole when
// what it reads past the budget fits max_row_bytes, and is left to a later
// read whole when it does not, however small each of its rows is.
func TestHistoryCheckpointCandidatesEndAtACallGroup(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	userID, botID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, userID)
	exec(`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, botID, userID)
	exec(`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, sessionID, botID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id = $1`, botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	turnID := uuid.NewString()
	var seq int64
	row := func(role string) (pgtype.UUID, int64) {
		t.Helper()
		seq++
		id := uuid.NewString()
		exec(`INSERT INTO bot_history_messages
			(id, bot_id, session_id, role, content, metadata, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
			VALUES ($1, $2, $3, $4, jsonb_build_object('role', $4::text, 'content', 'm'), '{}'::jsonb, $5, 1, $6, true, clock_timestamp())`,
			id, botID, sessionID, role, turnID, seq)
		var bytes int64
		if err := pool.QueryRow(ctx, `SELECT octet_length(content::text) FROM bot_history_messages WHERE id = $1`, id).Scan(&bytes); err != nil {
			t.Fatalf("measure row: %v", err)
		}
		return ParseUUIDOrEmpty(id), bytes
	}
	request, requestBytes := row("user")
	call, callBytes := row("assistant")
	first, firstBytes := row("tool")
	second, _ := row("tool")
	next, _ := row("user")

	queries := sqlc.New(pool)
	read := func(maxBytes, maxRowBytes int64) []pgtype.UUID {
		t.Helper()
		rows, err := queries.ListHistoryCheckpointCandidatesWithinBytes(ctx, sqlc.ListHistoryCheckpointCandidatesWithinBytesParams{
			SessionID: ParseUUIDOrEmpty(sessionID), MaxBytes: maxBytes, MaxRowBytes: maxRowBytes,
		})
		if err != nil {
			t.Fatalf("read candidates: %v", err)
		}
		ids := make([]pgtype.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids
	}
	group := []pgtype.UUID{request, call, first, second}
	for _, tc := range []struct {
		name             string
		maxBytes, maxRow int64
		want             []pgtype.UUID
	}{
		{name: "the call crosses the budget", maxBytes: requestBytes, maxRow: 1 << 20, want: group},
		{name: "the first result crosses the budget", maxBytes: requestBytes + callBytes, maxRow: 1 << 20, want: group},
		{name: "the budget ends at the first result", maxBytes: requestBytes + callBytes + firstBytes, maxRow: 1 << 20, want: group},
		{name: "a result is larger than a row may be", maxBytes: requestBytes + callBytes, maxRow: firstBytes - 1, want: []pgtype.UUID{request}},
		{name: "the results together are larger than a row may be", maxBytes: requestBytes + callBytes, maxRow: firstBytes, want: []pgtype.UUID{request}},
		{name: "everything fits", maxBytes: 1 << 20, maxRow: 1 << 20, want: append(slices.Clone(group), next)},
	} {
		if got := read(tc.maxBytes, tc.maxRow); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: read %v, want %v", tc.name, got, tc.want)
		}
	}
}

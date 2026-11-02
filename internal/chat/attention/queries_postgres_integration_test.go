//go:build integration

package attention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type attentionFixture struct {
	tx     pgx.Tx
	q      *dbsqlc.Queries
	userID uuid.UUID
	other  uuid.UUID
	botID  uuid.UUID
}

func beginAttentionFixture(t *testing.T) attentionFixture {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip postgres integration test: TEST_POSTGRES_DSN is not set")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	f := attentionFixture{tx: tx, q: dbsqlc.New(tx), userID: uuid.New(), other: uuid.New(), botID: uuid.New()}
	for _, id := range []uuid.UUID{f.userID, f.other} {
		if _, err := tx.Exec(ctx, `
			WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1, $2, true) RETURNING id)
			INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u
		`, id, "attention-"+id.String()); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)`,
		f.botID, f.userID, "attention-"+f.botID.String()); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	return f
}

type sessionSpec struct {
	sessionType string
	visibility  string
	channelType *string
	creator     *uuid.UUID
	deleted     bool
}

func (f attentionFixture) session(t *testing.T, spec sessionSpec) uuid.UUID {
	t.Helper()
	if spec.sessionType == "" {
		spec.sessionType = "chat"
	}
	if spec.visibility == "" {
		spec.visibility = "user"
	}
	mode := spec.sessionType
	if mode == "acp_agent" {
		mode = "chat"
	}
	id := uuid.New()
	var deletedAt any
	if spec.deleted {
		deletedAt = time.Now()
	}
	var creator any
	if spec.creator != nil {
		creator = *spec.creator
	}
	if _, err := f.tx.Exec(context.Background(), `
		INSERT INTO bot_sessions (id, bot_id, type, session_mode, visibility, channel_type, created_by_user_id, title, deleted_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '2026-01-01T00:00:00Z')
	`, id, f.botID, spec.sessionType, mode, spec.visibility, spec.channelType, creator, "s-"+id.String()[:8], deletedAt); err != nil {
		t.Fatalf("insert session %+v: %v", spec, err)
	}
	return id
}

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func pgTime(at time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: at, Valid: true} }

func (f attentionFixture) mark(id uuid.UUID, kind string, at time.Time) error {
	_, err := f.q.MarkSessionAttention(context.Background(), dbsqlc.MarkSessionAttentionParams{ID: pgUUID(id), Kind: kind, At: pgTime(at)})
	return err
}

func strp(s string) *string { return &s }

func TestPostgresMarkSessionAttentionEligibility(t *testing.T) {
	f := beginAttentionFixture(t)
	now := time.Now()
	creator := &f.userID
	cases := []struct {
		name     string
		spec     sessionSpec
		eligible bool
	}{
		{"web chat", sessionSpec{creator: creator}, true},
		{"local channel", sessionSpec{creator: creator, channelType: strp("local")}, true},
		{"external agent", sessionSpec{sessionType: "acp_agent", creator: creator}, true},
		{"schedule", sessionSpec{sessionType: "schedule", creator: creator}, false},
		{"discuss", sessionSpec{sessionType: "discuss", creator: creator}, false},
		{"subagent", sessionSpec{sessionType: "subagent", creator: creator}, false},
		{"internal", sessionSpec{visibility: "internal", creator: creator}, false},
		{"telegram", sessionSpec{channelType: strp("telegram"), creator: creator}, false},
		{"no creator", sessionSpec{}, false},
		{"deleted", sessionSpec{creator: creator, deleted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := f.mark(f.session(t, tc.spec), "reply", now)
			if tc.eligible && err != nil {
				t.Fatalf("mark eligible session: %v", err)
			}
			if !tc.eligible && !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("mark ineligible session err = %v, want ErrNoRows", err)
			}
		})
	}
}

func TestPostgresMarkSessionAttentionKeepsUpdatedAt(t *testing.T) {
	f := beginAttentionFixture(t)
	id := f.session(t, sessionSpec{creator: &f.userID})
	if err := f.mark(id, "reply", time.Now()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	var updated time.Time
	if err := f.tx.QueryRow(context.Background(), `SELECT updated_at FROM bot_sessions WHERE id = $1`, id).Scan(&updated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !updated.Equal(want) {
		t.Fatalf("updated_at = %v, want %v", updated, want)
	}
}

func TestPostgresMarkSessionAttentionOnlyMovesForward(t *testing.T) {
	f := beginAttentionFixture(t)
	id := f.session(t, sessionSpec{creator: &f.userID})
	at := time.Now()
	if err := f.mark(id, "reply", at); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	for _, replay := range []time.Time{at, at.Add(-time.Minute)} {
		if err := f.mark(id, "failed", replay); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("replay at %v err = %v, want ErrNoRows", replay, err)
		}
	}
	if err := f.mark(id, "needs_input", at.Add(time.Second)); err != nil {
		t.Fatalf("later mark: %v", err)
	}
}

func (f attentionFixture) unread(t *testing.T, user uuid.UUID) []string {
	t.Helper()
	rows, err := f.q.ListUnreadSessionsForUser(context.Background(), dbsqlc.ListUnreadSessionsForUserParams{UserID: pgUUID(user), LimitCount: 200})
	if err != nil {
		t.Fatalf("list unread: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s:%s", uuid.UUID(r.SessionID.Bytes).String(), r.AttentionKind))
	}
	return out
}

func (f attentionFixture) read(t *testing.T, user, session uuid.UUID, through time.Time) time.Time {
	t.Helper()
	got, err := f.q.MarkSessionRead(context.Background(), dbsqlc.MarkSessionReadParams{UserID: pgUUID(user), SessionID: pgUUID(session), Through: pgTime(through)})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	return got.Time
}

func TestPostgresUnreadRules(t *testing.T) {
	f := beginAttentionFixture(t)
	t1 := time.Now().Truncate(time.Microsecond)
	id := f.session(t, sessionSpec{creator: &f.userID})
	otherUsers := f.session(t, sessionSpec{creator: &f.other})
	deleted := f.session(t, sessionSpec{creator: &f.userID})

	for _, s := range []uuid.UUID{id, otherUsers, deleted} {
		if err := f.mark(s, "reply", t1); err != nil {
			t.Fatalf("mark: %v", err)
		}
	}
	if _, err := f.tx.Exec(context.Background(), `UPDATE bot_sessions SET deleted_at = now() WHERE id = $1`, deleted); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if got := f.unread(t, f.userID); len(got) != 1 || got[0] != id.String()+":reply" {
		t.Fatalf("unread before read = %v, want only %s", got, id)
	}

	f.read(t, f.userID, id, t1)
	if got := f.unread(t, f.userID); len(got) != 0 {
		t.Fatalf("unread after read = %v, want none", got)
	}

	t2 := t1.Add(time.Second)
	if err := f.mark(id, "failed", t2); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	if got := f.unread(t, f.userID); len(got) != 1 || got[0] != id.String()+":failed" {
		t.Fatalf("unread after new attention = %v, want %s:failed", got, id)
	}

	if got := f.read(t, f.userID, id, t1.Add(-time.Hour)); !got.Equal(t1) {
		t.Fatalf("stale through rewound read_at to %v, want %v", got, t1)
	}
	if got := f.unread(t, f.userID); len(got) != 1 {
		t.Fatalf("stale through cleared unread: %v", got)
	}

	attention, err := f.q.ListSessionAttentionForUser(context.Background(), dbsqlc.ListSessionAttentionForUserParams{
		UserID:     pgUUID(f.userID),
		SessionIds: []pgtype.UUID{pgUUID(id), pgUUID(otherUsers)},
	})
	if err != nil {
		t.Fatalf("list attention: %v", err)
	}
	if len(attention) != 1 || uuid.UUID(attention[0].SessionID.Bytes) != id || !attention[0].Unread || attention[0].AttentionKind != "failed" {
		t.Fatalf("attention for user = %+v, want only %s unread failed", attention, id)
	}
}

func (f attentionFixture) markUnread(t *testing.T, user uuid.UUID, at time.Time, ids ...uuid.UUID) []dbsqlc.MarkSessionsUnreadRow {
	t.Helper()
	pgIDs := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		pgIDs = append(pgIDs, pgUUID(id))
	}
	rows, err := f.q.MarkSessionsUnread(context.Background(), dbsqlc.MarkSessionsUnreadParams{UserID: pgUUID(user), SessionIds: pgIDs, At: pgTime(at)})
	if err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	return rows
}

func TestPostgresMarkedUnreadIsTheOtherSideOfRead(t *testing.T) {
	f := beginAttentionFixture(t)
	t1 := time.Now().Truncate(time.Microsecond)
	t2 := t1.Add(time.Second)
	replied := f.session(t, sessionSpec{creator: &f.userID})
	quiet := f.session(t, sessionSpec{creator: &f.userID})
	othersSession := f.session(t, sessionSpec{creator: &f.other})
	telegram := f.session(t, sessionSpec{creator: &f.userID, channelType: strp("telegram")})
	if err := f.mark(replied, "failed", t1); err != nil {
		t.Fatalf("mark: %v", err)
	}
	f.read(t, f.userID, replied, t1)

	rows := f.markUnread(t, f.userID, t2, replied, quiet, othersSession, telegram)
	got := map[uuid.UUID]dbsqlc.MarkSessionsUnreadRow{}
	for _, r := range rows {
		got[uuid.UUID(r.SessionID.Bytes)] = r
	}
	if len(got) != 2 {
		t.Fatalf("marked %d sessions, want only the caller's eligible two: %+v", len(got), rows)
	}
	if r := got[replied]; r.AttentionKind != "failed" || !r.AttentionAt.Time.Equal(t1) || !r.ReadThrough.Time.Equal(t2) || !r.Marked {
		t.Fatalf("replied session = %+v, want its own kind and time, read through the mark", r)
	}
	if r := got[quiet]; r.AttentionKind != "marked" || !r.AttentionAt.Time.Equal(t2) || !r.Marked {
		t.Fatalf("never-attended session = %+v, want kind marked at the mark time", r)
	}
	if list := f.unread(t, f.userID); len(list) != 2 {
		t.Fatalf("unread after marking = %v, want both marked sessions", list)
	}

	f.read(t, f.userID, quiet, t1)
	if list := f.unread(t, f.userID); len(list) != 2 {
		t.Fatalf("a read through older than the mark cleared it: %v", list)
	}
	f.read(t, f.userID, replied, t2)
	f.read(t, f.userID, quiet, t2)
	if list := f.unread(t, f.userID); len(list) != 0 {
		t.Fatalf("unread after reading through the marks = %v, want none", list)
	}

	attention, err := f.q.ListSessionAttentionForUser(context.Background(), dbsqlc.ListSessionAttentionForUserParams{
		UserID:     pgUUID(f.userID),
		SessionIds: []pgtype.UUID{pgUUID(quiet)},
	})
	if err != nil {
		t.Fatalf("list attention: %v", err)
	}
	if len(attention) != 1 || attention[0].Unread {
		t.Fatalf("annotation for a read marked session = %+v, want one read row", attention)
	}
}

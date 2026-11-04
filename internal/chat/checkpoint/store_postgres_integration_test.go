package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// historyFixture is a bot whose sessions hold turn-bound history rows
// written directly, so a test places each row exactly where it needs it.
type historyFixture struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	queries *postgresstore.Queries
	store   *Store
	botID   string
	session string
	turns   map[string]string
	rows    map[string]messagepkg.HistoryPosition
}

func newHistoryFixture(t *testing.T) *historyFixture {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the history checkpoint PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	f := &historyFixture{
		t:       t,
		ctx:     ctx,
		pool:    pool,
		queries: queries,
		store:   NewStore(nil, queries),
		botID:   uuid.NewString(),
		turns:   make(map[string]string),
		rows:    make(map[string]messagepkg.HistoryPosition),
	}
	userID := uuid.NewString()
	f.exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, userID)
	f.exec(`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, f.botID, userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id = $1`, f.botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	f.session = f.newSession()
	return f
}

func (f *historyFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f *historyFixture) newSession() string {
	f.t.Helper()
	id := uuid.NewString()
	f.exec(`INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')`, id, f.botID)
	return id
}

// row writes a visible context row of the session at a turn position and a
// sequence in it.
func (f *historyFixture) row(session string, turn, seq int64) string {
	f.t.Helper()
	key := fmt.Sprintf("%s/%d", session, turn)
	turnID, ok := f.turns[key]
	if !ok {
		turnID = uuid.NewString()
		f.turns[key] = turnID
	}
	role := "user"
	if seq > 1 {
		role = "assistant"
	}
	id := uuid.NewString()
	f.exec(`INSERT INTO bot_history_messages
		(id, bot_id, session_id, role, content, turn_id, turn_position, turn_message_seq, turn_visible, created_at)
		VALUES ($1, $2, $3, $4, jsonb_build_object('role', $4::text, 'content', $5::text), $6, $7, $8, true, clock_timestamp())`,
		id, f.botID, session, role, fmt.Sprintf("turn %d message %d", turn, seq), turnID, turn, seq)
	f.rows[id] = messagepkg.HistoryPosition{Turn: turn, Seq: seq}
	return id
}

// summary writes a usable compaction log of the session.
func (f *historyFixture) summary(session, text string) string {
	f.t.Helper()
	id := uuid.NewString()
	f.exec(`INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, completed_at)
		VALUES ($1, $2, $3, 'ok', $4, now())`, id, f.botID, session, text)
	return id
}

// fold commits a summary of the folded rows onto parent for the fixture's
// session, as a compaction pass that read every row of it does.
func (f *historyFixture) fold(parent Checkpoint, text string, folded ...string) (Checkpoint, error) {
	f.t.Helper()
	return f.foldRead(parent, text, f.sessionRows(), folded...)
}

func (f *historyFixture) foldRead(parent Checkpoint, text string, read []string, folded ...string) (Checkpoint, error) {
	f.t.Helper()
	rows := make([]Row, 0, len(folded))
	for _, id := range folded {
		rows = append(rows, Row{ID: id, Position: f.rows[id]})
	}
	fold := Fold{SessionID: f.session, Parent: parent, SummaryID: f.summary(f.session, text), Summary: text, Folded: rows, Read: read}
	var created Checkpoint
	err := f.queries.InTx(f.ctx, func(q dbstore.Queries) error {
		var err error
		created, err = f.store.Commit(f.ctx, q, fold)
		return err
	})
	return created, err
}

func (f *historyFixture) mustFold(parent Checkpoint, text string, folded ...string) Checkpoint {
	f.t.Helper()
	created, err := f.fold(parent, text, folded...)
	if err != nil {
		f.t.Fatalf("fold %q: %v", text, err)
	}
	return created
}

func (f *historyFixture) sessionRows() []string {
	ids := make([]string, 0, len(f.rows))
	for id := range f.rows {
		ids = append(ids, id)
	}
	return ids
}

// pointer is the session's active checkpoint as stored: "" when it has none.
func (f *historyFixture) pointer(session string) string {
	f.t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT history_checkpoint_id FROM bot_sessions WHERE id = $1`, session).Scan(&id); err != nil {
		f.t.Fatalf("read active checkpoint: %v", err)
	}
	return id.String()
}

func (f *historyFixture) active() Checkpoint {
	f.t.Helper()
	active, err := f.store.Active(f.ctx, f.session)
	if err != nil {
		f.t.Fatalf("active checkpoint: %v", err)
	}
	return active
}

func items(entries ...Item) []Item { return entries }

func sameItems(got, want []Item) bool {
	return slices.EqualFunc(got, want, func(a, b Item) bool {
		return a.RowID == b.RowID && a.SummaryID == b.SummaryID && (b.Summary == "" || a.Summary == b.Summary)
	})
}

// A fold accounts for the history through the last row it folds: the rows
// there it leaves raw stay in place by reference, its summary follows them,
// and the next fold absorbs both into its own summary.
func TestPostgresCommitFoldsOntoTheActiveCheckpoint(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	u2, a2 := f.row(f.session, 2, 1), f.row(f.session, 2, 2)
	u3, _ := f.row(f.session, 3, 1), f.row(f.session, 3, 2)

	first := f.mustFold(Checkpoint{}, "first", u1, a1, a2)
	if first.ParentID != "" || first.Position != (messagepkg.HistoryPosition{Turn: 2, Seq: 2}) {
		t.Fatalf("first = parent %q at %+v, want no parent at 2.2", first.ParentID, first.Position)
	}
	if want := items(Item{RowID: u2}, Item{SummaryID: first.SummaryIDs()[0], Summary: "first"}); !sameItems(first.Items, want) {
		t.Fatalf("first items = %+v, want %+v", first.Items, want)
	}
	if got := f.pointer(f.session); got != first.ID {
		t.Fatalf("active = %q, want %q", got, first.ID)
	}
	read := f.active()
	if read.ID != first.ID || !sameItems(read.Items, first.Items) || read.Items[1].Summary != "first" {
		t.Fatalf("Active = %+v, want %+v with the summary text", read, first)
	}

	second := f.mustFold(read, "second", u2, u3)
	if second.ParentID != first.ID || second.Position != (messagepkg.HistoryPosition{Turn: 3, Seq: 1}) {
		t.Fatalf("second = parent %q at %+v, want %q at 3.1", second.ParentID, second.Position, first.ID)
	}
	if len(second.Items) != 1 || second.Items[0].SummaryID == "" {
		t.Fatalf("second items = %+v, want only its summary", second.Items)
	}
	if got := f.active(); got.ID != second.ID || got.Items[0].Summary != "second" {
		t.Fatalf("Active = %+v, want %q with its text", got, second.ID)
	}
}

// A fold keeps the rows of its span it does not fold: one its pass read, an
// anchor, before its summary, where the request that read it places the
// summary, and one its pass never read after it, since the summary was
// written without it.
func TestPostgresCommitKeepsTheRowsItDoesNotFoldAroundItsSummary(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	read := f.sessionRows()
	u2, a2 := f.row(f.session, 2, 1), f.row(f.session, 2, 2)

	created, err := f.foldRead(Checkpoint{}, "summary", append(read, a2), a1, a2)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if want := items(Item{RowID: u1}, Item{SummaryID: created.SummaryIDs()[0]}, Item{RowID: u2}); !sameItems(created.Items, want) {
		t.Fatalf("items = %+v, want the anchor before the summary and the unread row after it", created.Items)
	}
}

// A fold commits only onto the checkpoint it read: when another fold moved
// the session on, it is refused and the session stays where it was.
func TestPostgresCommitRefusesHistoryThatMoved(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	first := f.mustFold(Checkpoint{}, "first", a1)

	if _, err := f.fold(Checkpoint{}, "stale", u1); !errors.Is(err, ErrMoved) {
		t.Fatalf("fold onto a replaced parent = %v, want ErrMoved", err)
	}
	if got := f.pointer(f.session); got != first.ID {
		t.Fatalf("active = %q, want %q", got, first.ID)
	}
}

// Every checkpoint holds a summary: what moves a session back relies on it,
// so the database refuses one without.
func TestPostgresACheckpointAlwaysHoldsASummary(t *testing.T) {
	f := newHistoryFixture(t)
	row := f.row(f.session, 1, 1)
	_, err := create(f.ctx, f.queries, dbpkg.ParseUUIDOrEmpty(f.session), Checkpoint{}, messagepkg.HistoryPosition{Turn: 1, Seq: 1}, items(Item{RowID: row}))
	if err == nil || !strings.Contains(err.Error(), "bot_history_checkpoints_summary_check") {
		t.Fatalf("create without a summary = %v, want the summary check to refuse it", err)
	}
	if got := f.pointer(f.session); got != "" {
		t.Fatalf("active after the refused checkpoint = %q, want none", got)
	}
}

// What an edit or retry renders before its replacement commits is the
// checkpoint the session keeps once the turn goes: the newest one before
// the turn; a turn without context rows has none to name. Reading it leaves
// the session as it is; rewinding before the turn makes it active.
func TestPostgresKeptBeforeTurnIsWhatAReplacementKeeps(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	u2, a2 := f.row(f.session, 2, 1), f.row(f.session, 2, 2)
	f.row(f.session, 3, 1)
	first := f.mustFold(Checkpoint{}, "first", u1, a1)
	second := f.mustFold(first, "second", u2, a2)
	turn := func(position int64) string { return f.turns[fmt.Sprintf("%s/%d", f.session, position)] }

	for _, tc := range []struct {
		turn int64
		want string
	}{
		{turn: 3, want: second.ID},
		{turn: 2, want: first.ID},
		{turn: 1, want: ""},
	} {
		kept, err := f.store.KeptBeforeTurn(f.ctx, f.session, turn(tc.turn))
		if err != nil {
			t.Fatalf("KeptBeforeTurn(%d): %v", tc.turn, err)
		}
		if kept.ID != tc.want {
			t.Fatalf("KeptBeforeTurn(%d) = %q, want %q", tc.turn, kept.ID, tc.want)
		}
	}
	if _, err := f.store.KeptBeforeTurn(f.ctx, f.session, uuid.NewString()); err == nil {
		t.Fatal("KeptBeforeTurn of a turn without context rows reported a checkpoint")
	}
	if got := f.pointer(f.session); got != second.ID {
		t.Fatalf("active after reads = %q, want %q", got, second.ID)
	}

	rewind := func() bool {
		t.Helper()
		var rewound bool
		if err := f.queries.InTx(f.ctx, func(q dbstore.Queries) error {
			var err error
			rewound, err = q.RewindHistoryCheckpointBeforeTurn(f.ctx, dbsqlc.RewindHistoryCheckpointBeforeTurnParams{
				SessionID: dbpkg.ParseUUIDOrEmpty(f.session),
				BotID:     dbpkg.ParseUUIDOrEmpty(f.botID),
				TurnID:    dbpkg.ParseUUIDOrEmpty(turn(2)),
			})
			return err
		}); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		return rewound
	}
	if !rewind() || f.pointer(f.session) != first.ID {
		t.Fatalf("rewind before turn 2 left %q active, want %q", f.pointer(f.session), first.ID)
	}
	if rewind() {
		t.Fatal("a second rewind before turn 2 reported a move")
	}
}

// A rewind deletes the checkpoints it moves back past: each accounts for a
// removed row, so none holds again. A fold that read one of them as the
// active checkpoint finds the history moved, and another session's chain is
// left alone.
func TestPostgresRewindDeletesTheCheckpointsItMovesBackPast(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	u2, a2 := f.row(f.session, 2, 1), f.row(f.session, 2, 2)
	u3, a3 := f.row(f.session, 3, 1), f.row(f.session, 3, 2)
	first := f.mustFold(Checkpoint{}, "first", u1, a1)
	second := f.mustFold(first, "second", u2, a2)
	third := f.mustFold(second, "third", u3, a3)

	main := f.session
	f.session = f.newSession()
	other := f.mustFold(Checkpoint{}, "other", f.row(f.session, 1, 1))
	f.session = main

	checkpoints := func(session string) []string {
		t.Helper()
		rows, err := f.pool.Query(f.ctx, `SELECT id::text FROM bot_history_checkpoints WHERE session_id = $1 ORDER BY created_at`, session)
		if err != nil {
			t.Fatalf("list checkpoints: %v", err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan checkpoint: %v", err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	rewindBefore := func(position int64) {
		t.Helper()
		if err := f.queries.InTx(f.ctx, func(q dbstore.Queries) error {
			_, err := q.RewindHistoryCheckpointBeforeTurn(f.ctx, dbsqlc.RewindHistoryCheckpointBeforeTurnParams{
				SessionID: dbpkg.ParseUUIDOrEmpty(f.session),
				BotID:     dbpkg.ParseUUIDOrEmpty(f.botID),
				TurnID:    dbpkg.ParseUUIDOrEmpty(f.turns[fmt.Sprintf("%s/%d", f.session, position)]),
			})
			return err
		}); err != nil {
			t.Fatalf("rewind before turn %d: %v", position, err)
		}
	}

	rewindBefore(2)
	if got := checkpoints(f.session); f.pointer(f.session) != first.ID || !slices.Equal(got, []string{first.ID}) {
		t.Fatalf("after rewinding past %s and %s: active=%q checkpoints=%v, want only %q", second.ID, third.ID, f.pointer(f.session), got, first.ID)
	}
	if _, err := f.fold(third, "onto a deleted parent", u2); !errors.Is(err, ErrMoved) {
		t.Fatalf("fold onto a checkpoint the rewind deleted = %v, want ErrMoved", err)
	}
	if got := f.pointer(f.session); got != first.ID {
		t.Fatalf("active after the refused fold = %q, want %q", got, first.ID)
	}

	rewindBefore(1)
	if got := checkpoints(f.session); f.pointer(f.session) != "" || len(got) != 0 {
		t.Fatalf("after rewinding past every checkpoint: active=%q checkpoints=%v, want none", f.pointer(f.session), got)
	}
	var others int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_checkpoints WHERE id = $1`, other.ID).Scan(&others); err != nil || others != 1 {
		t.Fatalf("another session's checkpoint: count=%d err=%v, want it kept", others, err)
	}
}

// A fork carries the newest checkpoint of its source's chain that accounts
// for no history past the copy: its summaries become the fork's own, its rows
// are named by their copies, and it sits at the copy's place for its
// position, with no parent. A copy that misses a row the checkpoint keeps is
// refused.
func TestPostgresCopyToForkCarriesTheNewestCheckpointTheCopyHolds(t *testing.T) {
	f := newHistoryFixture(t)
	turn := func(position int64) [2]string {
		return [2]string{f.row(f.session, position, 1), f.row(f.session, position, 2)}
	}
	source := [4][2]string{turn(1), turn(2), turn(3), turn(4)}
	first := f.mustFold(Checkpoint{}, "first", source[0][1])
	second := f.mustFold(first, "second", source[0][0], source[1][0], source[1][1], source[2][0])
	if !slices.Contains(first.RowIDs(), source[0][0]) || len(second.Items) != 1 {
		t.Fatalf("chain = %+v / %+v, want the first keeping its user row and the second only a summary", first.Items, second.Items)
	}

	// fork copies the source's rows of the given turns into a new session,
	// ten turns later, as a thread fork binds its copies.
	fork := func(throughTurn int, skip ...string) (string, Copy, map[string]string) {
		session := f.newSession()
		copies := make(map[string]string)
		c := Copy{SourceSessionID: f.session, ForkSessionID: session, ThroughTurnPosition: int64(throughTurn)}
		for turn := 0; turn < throughTurn; turn++ {
			for seq, id := range source[turn] {
				if slices.Contains(skip, id) {
					continue
				}
				copied := f.row(session, int64(turn+11), int64(seq+1))
				copies[id] = copied
				c.SourceMessageIDs = append(c.SourceMessageIDs, dbpkg.ParseUUIDOrEmpty(id))
				c.ForkMessageIDs = append(c.ForkMessageIDs, dbpkg.ParseUUIDOrEmpty(copied))
			}
		}
		return session, c, copies
	}
	copyToFork := func(c Copy) error {
		t.Helper()
		return f.queries.InTx(f.ctx, func(q dbstore.Queries) error {
			return CopyToFork(f.ctx, q, c)
		})
	}
	forkActive := func(session string) Checkpoint {
		t.Helper()
		copied, err := f.store.Active(f.ctx, session)
		if err != nil {
			t.Fatalf("fork Active: %v", err)
		}
		return copied
	}

	session, c, copies := fork(2)
	if err := copyToFork(c); err != nil {
		t.Fatalf("copy to fork: %v", err)
	}
	copied := forkActive(session)
	if copied.ID == "" || copied.ParentID != "" || copied.Position != (messagepkg.HistoryPosition{Turn: 11, Seq: 2}) {
		t.Fatalf("copy = %q parent %q at %+v, want a checkpoint without parent at 11.2", copied.ID, copied.ParentID, copied.Position)
	}
	if len(copied.Items) != 2 || copied.Items[0].RowID != copies[source[0][0]] || copied.Items[1].SummaryID == first.SummaryIDs()[0] || copied.Items[1].Summary != "first" {
		t.Fatalf("copy items = %+v, want the copied row then a summary of the fork's own holding the source's text", copied.Items)
	}
	if got := f.pointer(f.session); got != second.ID {
		t.Fatalf("source active = %q, want %q", got, second.ID)
	}

	session, c, _ = fork(3)
	if err := copyToFork(c); err != nil || forkActive(session).Position != (messagepkg.HistoryPosition{Turn: 13, Seq: 1}) {
		t.Fatalf("a fork through turn 3 did not carry the checkpoint through 3.1: %v", err)
	}
	if session, c, _ = fork(2, source[0][0]); copyToFork(c) == nil || f.pointer(session) != "" {
		t.Fatal("a fork that did not copy a kept row was given a checkpoint")
	}
}

// Checkpoints go with their session; one removed under the session leaves it
// with the history it rendered raw, and its descendants go with it.
func TestPostgresCheckpointsGoWithTheirSession(t *testing.T) {
	f := newHistoryFixture(t)
	u1, a1 := f.row(f.session, 1, 1), f.row(f.session, 1, 2)
	u2 := f.row(f.session, 2, 1)
	first := f.mustFold(Checkpoint{}, "first", u1, a1)
	second := f.mustFold(first, "second", u2)

	f.exec(`DELETE FROM bot_history_checkpoints WHERE id = $1`, first.ID)
	var left int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_checkpoints WHERE id = ANY($1::uuid[])`, []string{first.ID, second.ID}).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 || f.pointer(f.session) != "" {
		t.Fatalf("after removing the root: %d checkpoints left, active %q; want none", left, f.pointer(f.session))
	}

	third := f.mustFold(Checkpoint{}, "third", u1)
	f.exec(`DELETE FROM bot_sessions WHERE id = $1`, f.session)
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_checkpoints WHERE id = $1`, third.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("a removed session left its checkpoint")
	}
}

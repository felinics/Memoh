package message

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// originFixture is six turns of two rows each, every row 500 bytes of
// content and created a second after the one before it.
type originFixture struct {
	turns []HistoryTurn
	// users and answers name each turn's rows, oldest turn first.
	users, answers []Message
	created        map[string]time.Time
}

func (f originFixture) at(row Message) time.Time { return f.created[row.ID] }

func setupOriginFixture(t *testing.T, ctx context.Context, tx pgx.Tx) originFixture {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	f := originFixture{created: map[string]time.Time{}}
	for i := 0; i < 6; i++ {
		user, answer, turn := persistPostgresTurn(t, ctx, svc)
		f.users, f.answers, f.turns = append(f.users, user), append(f.answers, answer), append(f.turns, turn)
		for j, id := range []string{user.ID, answer.ID} {
			at := base.Add(time.Duration(2*i+j) * time.Second)
			f.created[id] = at
			if _, err := tx.Exec(ctx, `UPDATE bot_history_messages
				SET created_at = $2,
				    content = jsonb_build_object('role', role, 'content',
				      repeat('x', 500 - octet_length(jsonb_build_object('role', role, 'content', '')::text)))
				WHERE id = $1`, id, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

// placeOrigin places the fixture session's history origin for a budget in
// tokens: 9/10 of it, in bytes at 4 per token less the estimator's 25
// percent margin, is the room for the rows from the origin on.
func placeOrigin(t *testing.T, ctx context.Context, tx pgx.Tx, budgetTokens int64) pgtype.Timestamptz {
	t.Helper()
	var origin pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `SELECT public.memoh_place_history_origin($1, $2)`, postgresMessageTestSessionID, budgetTokens).Scan(&origin); err != nil {
		t.Fatalf("place history origin: %v", err)
	}
	return origin
}

func loadOrigin(t *testing.T, ctx context.Context, tx pgx.Tx) HistoryOrigin {
	t.Helper()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	origin, err := svc.HistoryOriginBySession(ctx, postgresMessageTestSessionID)
	if err != nil {
		t.Fatalf("load history origin: %v", err)
	}
	return origin
}

// insertOriginTestSummary commits an ok summary of the given epoch that
// claims rows; an anchored one spans their creation times.
func insertOriginTestSummary(t *testing.T, ctx context.Context, tx pgx.Tx, f originFixture, epoch int64, summary string, anchored bool, rows ...Message) string {
	t.Helper()
	var startMs, endMs int64
	if anchored {
		startMs, endMs = f.at(rows[0]).UnixMilli(), f.at(rows[len(rows)-1]).UnixMilli()
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, compaction_epoch, anchor_start_ms, anchor_end_ms)
		VALUES ($1, $2, 'ok', $3, $4, $5, $6)
		RETURNING id::text`, postgresMessageTestBotID, postgresMessageTestSessionID, summary, epoch, startMs, endMs).Scan(&id); err != nil {
		t.Fatalf("insert summary: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET compact_id = $1 WHERE id = ANY($2::uuid[])`, id, ids); err != nil {
		t.Fatalf("claim summary rows: %v", err)
	}
	return id
}

func rowsOfTurns(f originFixture, turns ...int) []Message {
	var rows []Message
	for _, i := range turns {
		rows = append(rows, f.users[i], f.answers[i])
	}
	return rows
}

// The origin keeps the newest whole turns whose rows, with every active
// summary beside them, fit nine tenths of the budget, and records what it
// leaves out. It always keeps the newest turn, places nothing when all of it
// fits, starts at the earliest of the kept rows and the events they record,
// and moves to a clean turn boundary.
func TestPostgresHistoryOriginKeepsTheNewestHistoryThatFits(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	f := setupOriginFixture(t, ctx, tx)

	// 2304 bytes of room hold the 1000-byte turns 6 and 5, not turn 4.
	if got := placeOrigin(t, ctx, tx, 800); !got.Valid || !got.Time.Equal(f.at(f.users[4])) {
		t.Fatalf("origin = %v, want the start of turn 5 at %v", got, f.at(f.users[4]))
	}
	want := HistoryOrigin{At: f.at(f.users[4]), OmittedCount: 8, OmittedFirst: f.at(f.users[0]), OmittedLast: f.at(f.answers[3])}
	if got := loadOrigin(t, ctx, tx); !sameOrigin(got, want) {
		t.Fatalf("origin = %+v, want %+v", got, want)
	}

	// A budget that holds everything places no origin, and clears one.
	if got := placeOrigin(t, ctx, tx, 3000); got.Valid {
		t.Fatalf("origin = %v for a history that fits", got)
	}
	if got := loadOrigin(t, ctx, tx); got.Omits() || !got.At.IsZero() {
		t.Fatalf("origin = %+v after a placement that fits", got)
	}

	// An active summary takes its room: beside 421 bytes of summary only
	// turn 6 fits. The rows it replaced cost nothing and are left out.
	summaryID := insertOriginTestSummary(t, ctx, tx, f, 0, strings.Repeat("s", 400), true, rowsOfTurns(f, 0, 1)...)
	if got := placeOrigin(t, ctx, tx, 800); !got.Valid || !got.Time.Equal(f.at(f.users[5])) {
		t.Fatalf("origin beside a summary = %v, want the start of turn 6", got)
	}
	if got := loadOrigin(t, ctx, tx); got.OmittedCount != 10 {
		t.Fatalf("omitted = %d, want the 10 rows of turns 1-5", got.OmittedCount)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_message_compacts SET status = 'error' WHERE id = $1`, summaryID); err != nil {
		t.Fatal(err)
	}

	// The newest turn is kept however large it is.
	if got := placeOrigin(t, ctx, tx, 100); !got.Valid || !got.Time.Equal(f.at(f.users[5])) {
		t.Fatalf("origin under a budget the newest turn exceeds = %v, want its start", got)
	}

	// Rows the history load does not read cost nothing: with the inputs of
	// turns 5 and 6 passive, turns 4 to 6 fit.
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET metadata = '{"trigger_mode":"passive_sync"}' WHERE id = ANY($1::uuid[])`,
		[]string{f.users[4].ID, f.users[5].ID}); err != nil {
		t.Fatal(err)
	}
	if got := placeOrigin(t, ctx, tx, 800); !got.Valid || !got.Time.Equal(f.at(f.users[3])) {
		t.Fatalf("origin beside passive rows = %v, want the start of turn 4", got)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET metadata = '{}' WHERE id = ANY($1::uuid[])`,
		[]string{f.users[4].ID, f.users[5].ID}); err != nil {
		t.Fatal(err)
	}

	// The event a kept row records starts the origin when it came first.
	received := f.at(f.users[4]).Add(-300 * time.Millisecond)
	eventID, err := dbsqlc.New(tx).CreateSessionEvent(ctx, dbsqlc.CreateSessionEventParams{
		BotID: mustTestUUID(t, postgresMessageTestBotID), SessionID: mustTestUUID(t, postgresMessageTestSessionID),
		EventKind: "message", EventData: []byte(`{}`), ExternalMessageID: pgtype.Text{String: "inbound-5", Valid: true},
		ReceivedAtMs: received.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET event_id = $2 WHERE id = $1`, f.users[4].ID, eventID); err != nil {
		t.Fatal(err)
	}
	if got := placeOrigin(t, ctx, tx, 800); !got.Valid || !got.Time.Equal(received) {
		t.Fatalf("origin = %v, want the event of turn 5 received at %v", got, received)
	}

	// A row of turn 4 created after turn 5 began leaves turn 5 without a
	// clean start: the origin moves to turn 6.
	if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET created_at = $2 WHERE id = $1`, f.answers[3].ID, f.at(f.users[4]).Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := placeOrigin(t, ctx, tx, 800); !got.Valid || !got.Time.Equal(f.at(f.users[5])) {
		t.Fatalf("origin past a late row = %v, want the start of turn 6", got)
	}
}

// What the origin leaves out reaches no read path: not the history load's
// anchor, not the compaction candidates, not the Discuss event replay. A
// cleared history has no origin.
func TestPostgresHistoryOriginBoundsEveryReadPath(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	f := setupOriginFixture(t, ctx, tx)
	q := dbsqlc.New(tx)
	svc := NewService(nil, postgresstore.NewQueries(q))
	sessionID := mustTestUUID(t, postgresMessageTestSessionID)

	for _, row := range []struct {
		id string
		at time.Time
	}{{"before-origin", f.at(f.users[1])}, {"after-origin", f.at(f.users[5])}} {
		if _, err := q.CreateSessionEvent(ctx, dbsqlc.CreateSessionEventParams{
			BotID: mustTestUUID(t, postgresMessageTestBotID), SessionID: sessionID,
			EventKind: "message", EventData: []byte(`{}`), ExternalMessageID: pgtype.Text{String: row.id, Valid: true},
			ReceivedAtMs: row.at.UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	replay := func() []string {
		t.Helper()
		events, err := q.ListSessionEventsBySessionPageBeforeWithinBytes(ctx, dbsqlc.ListSessionEventsBySessionPageBeforeWithinBytesParams{
			SessionID: sessionID, CoveredExternalMessages: []byte(`{}`), CoveredEventCursors: []byte(`{}`), MaxBytes: 1 << 20, PageSize: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(events))
		for _, event := range events {
			ids = append(ids, event.ExternalMessageID.String)
		}
		return ids
	}
	candidates := func() int64 {
		t.Helper()
		measure, err := q.MeasureUncompactedMessagesBySession(ctx, dbsqlc.MeasureUncompactedMessagesBySessionParams{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := q.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{SessionID: sessionID, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(rows)) != measure.CandidateCount {
			t.Fatalf("listed %d candidates, measured %d", len(rows), measure.CandidateCount)
		}
		return measure.CandidateCount
	}
	anchor := func() time.Time {
		t.Helper()
		at, err := svc.HistoryAnchorBySession(ctx, postgresMessageTestSessionID, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		return at
	}

	if got := anchor(); !got.Equal(f.at(f.users[0])) {
		t.Fatalf("anchor without an origin = %v, want the oldest row", got)
	}
	if got := candidates(); got != 12 {
		t.Fatalf("candidates without an origin = %d, want 12", got)
	}
	if got := replay(); len(got) != 2 {
		t.Fatalf("replay without an origin = %v, want both events", got)
	}

	placeOrigin(t, ctx, tx, 800)
	if got := anchor(); !got.Equal(f.at(f.users[4])) {
		t.Fatalf("anchor = %v, want the origin at %v", got, f.at(f.users[4]))
	}
	if got := candidates(); got != 4 {
		t.Fatalf("candidates = %d, want the 4 rows of turns 5 and 6", got)
	}
	if got := replay(); len(got) != 1 || got[0] != "after-origin" {
		t.Fatalf("replay = %v, want only the event after the origin", got)
	}
	// Once turn 5 is summarized the anchor moves past the origin as before.
	insertOriginTestSummary(t, ctx, tx, f, 0, "turn five", true, rowsOfTurns(f, 4)...)
	if got := anchor(); !got.Equal(f.at(f.users[5])) {
		t.Fatalf("anchor after a summary = %v, want turn 6", got)
	}

	if err := q.ClearHistoryBySession(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	if got := loadOrigin(t, ctx, tx); !got.At.IsZero() || got.OmittedCount != 0 {
		t.Fatalf("origin after clearing the history = %+v", got)
	}
}

// A change before the origin rewinds compaction only when a usable summary
// reaches it, by its anchors (which outlive a deleted row) or, for a summary
// from before anchors, by the rows that carry it. A change from the origin on
// rewinds as it always did.
func TestPostgresRewindBeforeTheHistoryOriginNeedsASummaryThatReachesIt(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	f := setupOriginFixture(t, ctx, tx)
	placeOrigin(t, ctx, tx, 800)
	rewind := func(from time.Time) bool {
		t.Helper()
		var done bool
		if err := tx.QueryRow(ctx, `SELECT public.memoh_rewind_compaction($1, $2)`, postgresMessageTestSessionID, from).Scan(&done); err != nil {
			t.Fatalf("rewind: %v", err)
		}
		return done
	}

	if rewind(f.at(f.users[1])) {
		t.Fatal("a change no summary reaches rewound")
	}
	assertSessionCompactionEpoch(t, ctx, tx, 0)

	insertOriginTestSummary(t, ctx, tx, f, 0, "turns one and two", true, rowsOfTurns(f, 0, 1)...)
	if rewind(f.at(f.users[2])) {
		t.Fatal("a change after the only summary before the origin rewound")
	}
	assertSessionCompactionEpoch(t, ctx, tx, 0)
	if !rewind(f.at(f.users[1])) {
		t.Fatal("a change the summary covers did not rewind")
	}
	assertSessionCompactionEpoch(t, ctx, tx, 1)

	// Deleting the last row a summary covers leaves no row that carries it;
	// its anchors still reach the change.
	insertOriginTestSummary(t, ctx, tx, f, 1, "turns one and two", true, rowsOfTurns(f, 0, 1)...)
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	if err := svc.DeleteByIDs(ctx, []string{f.answers[1].ID}); err != nil {
		t.Fatal(err)
	}
	assertSessionCompactionEpoch(t, ctx, tx, 2)

	// A summary from before anchors reaches the rows that carry it.
	insertOriginTestSummary(t, ctx, tx, f, 2, "turn three", false, rowsOfTurns(f, 2)...)
	if rewind(f.at(f.users[3])) {
		t.Fatal("a change after the rows of an anchorless summary rewound")
	}
	if !rewind(f.at(f.answers[2])) {
		t.Fatal("a change of a row an anchorless summary carries did not rewind")
	}
	assertSessionCompactionEpoch(t, ctx, tx, 3)

	if !rewind(f.at(f.users[5])) {
		t.Fatal("a change after the origin did not rewind")
	}
	assertSessionCompactionEpoch(t, ctx, tx, 4)
}

func sameOrigin(a, b HistoryOrigin) bool {
	return a.At.Equal(b.At) && a.OmittedCount == b.OmittedCount && a.OmittedFirst.Equal(b.OmittedFirst) && a.OmittedLast.Equal(b.OmittedLast)
}

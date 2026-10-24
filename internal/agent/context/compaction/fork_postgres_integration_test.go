package compaction

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// forkTestBudget is the history budget, in tokens, the fork tests' sources
// record: 6720 bytes of room, less 221 for each 200-byte summary.
const forkTestBudget = 2334

// forkFixture forks sessions of one bot through the thread service with the
// summary carrier, against PostgreSQL. Every row it writes is 1000 bytes of
// stored content, created a second after the one before it.
type forkFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	queries  *postgresstore.Queries
	messages messagepkg.Service
	threads  *thread.Service
	userID   string
	botID    string
	next     time.Time
}

// forkTurn is an answered turn: its id and its two rows.
type forkTurn struct {
	id, user, answer string
	userAt           time.Time
	answerAt         time.Time
}

func newForkFixture(t *testing.T) *forkFixture {
	t.Helper()
	ctx := context.Background()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run the fork PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &forkFixture{t: t, ctx: ctx, pool: pool, userID: uuid.NewString(), botID: uuid.NewString(), next: time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)}
	f.exec(`WITH u AS (INSERT INTO users (id, username, is_active) VALUES ($1::uuid, $1::text, true) RETURNING id)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM u`, f.userID)
	f.exec(`INSERT INTO bots (id, owner_user_id, name) VALUES ($1::uuid, $2, $1::text)`, f.botID, f.userID)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM bots WHERE id=$1`, f.botID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, f.userID)
	})
	f.queries = postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	f.messages = messagepkg.NewService(slog.Default(), f.queries)
	f.threads = thread.NewService(slog.Default(), f.queries, nil)
	f.threads.SetForkHistoryCarrier(ForkHistoryCarrier{})
	return f
}

func (f *forkFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, query, args...); err != nil {
		f.t.Fatalf("exec: %v", err)
	}
}

// session creates a chat session whose runs planned forkTestBudget.
func (f *forkFixture) session() string {
	f.t.Helper()
	sessionID := uuid.NewString()
	f.exec(`INSERT INTO bot_sessions (id, bot_id, channel_type, title) VALUES ($1, $2, 'local', 'Source')`, sessionID, f.botID)
	f.exec(`INSERT INTO context_lifecycles (run_id, bot_id, session_id, status, snapshot)
		VALUES ($1, $2, $3, 'completed', jsonb_build_object('budget_plan', jsonb_build_object('history_budget', $4::int)))`,
		uuid.NewString(), f.botID, sessionID, forkTestBudget)
	return sessionID
}

// turns appends n answered turns to the session.
func (f *forkFixture) turns(sessionID string, n int) []forkTurn {
	f.t.Helper()
	turns := make([]forkTurn, 0, n)
	for range n {
		var rows [2]messagepkg.Message
		var created [2]time.Time
		for i, role := range []string{"user", "assistant"} {
			row, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: sessionID, Role: role, Content: []byte(`{"role":"` + role + `","content":""}`)})
			if err != nil {
				f.t.Fatalf("persist %s: %v", role, err)
			}
			created[i] = f.next
			f.next = f.next.Add(time.Second)
			f.exec(`UPDATE bot_history_messages
				SET created_at = $2,
				    content = jsonb_build_object('role', role, 'content',
				      repeat('x', 1000 - octet_length(jsonb_build_object('role', role, 'content', '')::text)))
				WHERE id = $1`, row.ID, created[i])
			rows[i] = row
		}
		historyTurn, err := f.messages.GetVisibleTurnByMessage(f.ctx, sessionID, rows[1].ID)
		if err != nil {
			f.t.Fatalf("read turn: %v", err)
		}
		turns = append(turns, forkTurn{id: historyTurn.ID, user: rows[0].ID, answer: rows[1].ID, userAt: created[0], answerAt: created[1]})
	}
	return turns
}

// summarize commits a 200-byte summary of the turns' rows as a pass does:
// coverage of their records, anchors at the first and the last, the rows
// claimed.
func (f *forkFixture) summarize(sessionID, label string, turns ...forkTurn) string {
	f.t.Helper()
	session := mustForkUUID(f.t, sessionID)
	var ids []pgtype.UUID
	for _, turn := range turns {
		ids = append(ids, mustForkUUID(f.t, turn.user), mustForkUUID(f.t, turn.answer))
	}
	records, err := coverageRecords(f.ctx, f.queries, session, ids)
	if err != nil {
		f.t.Fatal(err)
	}
	items := make([]CompactionCandidate, 0, len(ids))
	for _, id := range ids {
		items = append(items, CompactionCandidate{ID: id, Record: records[id]})
	}
	metadata, err := artifactMetadataFor(items, ids)
	if err != nil {
		f.t.Fatal(err)
	}
	var compactID string
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, message_count, coverage, anchor_start_ms, anchor_end_ms, artifact_version, started_at, completed_at)
		VALUES ($1, $2, 'ok', $3, $4, $5, $6, $7, 1, now(), now())
		RETURNING id::text`,
		f.botID, sessionID, label+strings.Repeat(".", 200-len(label)), len(ids), metadata.Coverage, metadata.AnchorStartMs, metadata.AnchorEndMs).Scan(&compactID); err != nil {
		f.t.Fatalf("insert summary: %v", err)
	}
	f.exec(`UPDATE bot_history_messages SET compact_id = $1 WHERE id = ANY($2::uuid[])`, compactID, ids)
	return compactID
}

// placeOrigin places the session's history origin as the upgrade does.
func (f *forkFixture) placeOrigin(sessionID string) {
	f.t.Helper()
	f.exec(`SELECT public.memoh_place_history_origin($1, public.memoh_session_history_budget($1))`, sessionID)
}

func (f *forkFixture) fork(sessionID string, at forkTurn) string {
	f.t.Helper()
	fork, err := f.threads.ForkFromAssistantTurn(f.ctx, thread.ForkFromAssistantInput{BotID: f.botID, ThreadID: sessionID, TurnID: at.id, Title: "Fork", CreatedByUserID: f.userID})
	if err != nil {
		f.t.Fatalf("fork: %v", err)
	}
	return fork.ID
}

func (f *forkFixture) origin(sessionID string) messagepkg.HistoryOrigin {
	f.t.Helper()
	origin, err := f.messages.HistoryOriginBySession(f.ctx, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return origin
}

// summaries is the session's active summaries.
func (f *forkFixture) summaries(sessionID string) []Artifact {
	f.t.Helper()
	frontier, err := NewArtifactProjection(f.queries).LoadActiveSession(f.ctx, ArtifactOwner{BotID: f.botID, SessionID: sessionID, SessionIDKnown: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return frontier.Artifacts
}

// copies maps each source row to the fork's copy of it.
func (f *forkFixture) copies(sourceID, forkID string) map[string]string {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `
		SELECT source.id::text, copy.id::text
		FROM bot_history_messages source
		JOIN bot_history_messages copy ON copy.session_id = $2 AND copy.created_at = source.created_at
		WHERE source.session_id = $1`, sourceID, forkID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	copies := map[string]string{}
	for rows.Next() {
		var source, copied string
		if err := rows.Scan(&source, &copied); err != nil {
			f.t.Fatal(err)
		}
		copies[source] = copied
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return copies
}

// claimed is the session's rows a summary claims, by summary.
func (f *forkFixture) claimed(sessionID string) map[string][]string {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT compact_id::text, id::text FROM bot_history_messages WHERE session_id = $1 AND compact_id IS NOT NULL ORDER BY created_at`, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	claimed := map[string][]string{}
	for rows.Next() {
		var compactID, rowID string
		if err := rows.Scan(&compactID, &rowID); err != nil {
			f.t.Fatal(err)
		}
		claimed[compactID] = append(claimed[compactID], rowID)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return claimed
}

// candidates counts the rows a compaction pass of the session would read.
func (f *forkFixture) candidates(sessionID string) int64 {
	f.t.Helper()
	measure, err := f.queries.MeasureUncompactedMessagesBySession(f.ctx, dbsqlc.MeasureUncompactedMessagesBySessionParams{SessionID: mustForkUUID(f.t, sessionID)})
	if err != nil {
		f.t.Fatal(err)
	}
	return measure.CandidateCount
}

// assertCarried checks that the fork holds each source summary, in order, as
// an active root of its own over the copies of exactly the rows it covers,
// and that every copy it names counts as covered.
func (f *forkFixture) assertCarried(sourceID, forkID string, want ...Artifact) {
	f.t.Helper()
	got := f.summaries(forkID)
	if len(got) != len(want) {
		f.t.Fatalf("fork has %d active summaries, want %d", len(got), len(want))
	}
	copies := f.copies(sourceID, forkID)
	claimed := f.claimed(forkID)
	for i, source := range want {
		carried := got[i]
		if carried.ID == source.ID || carried.Summary != source.Summary || carried.AnchorStartMs != source.AnchorStartMs || carried.AnchorEndMs != source.AnchorEndMs || len(carried.Coverage) != len(source.Coverage) {
			f.t.Fatalf("carried summary %d = %+v, want a copy of %+v", i, carried, source)
		}
		var copyIDs []pgtype.UUID
		for j, covered := range source.Coverage {
			copyID := copies[covered.Ref.ID]
			if copyID == "" || carried.Coverage[j].Ref.ID != copyID {
				f.t.Fatalf("carried summary %d names %s for source row %s, want its copy %s", i, carried.Coverage[j].Ref.ID, covered.Ref.ID, copyID)
			}
			copyIDs = append(copyIDs, mustForkUUID(f.t, copyID))
		}
		if strings.Join(claimed[carried.ID], ",") != strings.Join(coverageIDs(carried), ",") {
			f.t.Fatalf("carried summary %d claims %v, want the copies it covers %v", i, claimed[carried.ID], coverageIDs(carried))
		}
		records, err := coverageRecords(f.ctx, f.queries, mustForkUUID(f.t, forkID), copyIDs)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, id := range copyIDs {
			if !carried.CoversRecord(records[id]) {
				f.t.Fatalf("carried summary %d does not cover the copy %s it names", i, id)
			}
		}
	}
	if len(claimed) != len(want) {
		f.t.Fatalf("fork rows are claimed by %d summaries, want %d", len(claimed), len(want))
	}
}

func coverageIDs(artifact Artifact) []string {
	ids := make([]string, 0, len(artifact.Coverage))
	for _, covered := range artifact.Coverage {
		ids = append(ids, covered.Ref.ID)
	}
	return ids
}

func wantForkOrigin(t *testing.T, got messagepkg.HistoryOrigin, at time.Time, omitted int64, first, last time.Time) {
	t.Helper()
	want := messagepkg.HistoryOrigin{At: at, OmittedCount: omitted, OmittedFirst: first, OmittedLast: last}
	if !got.At.Equal(want.At) || got.OmittedCount != want.OmittedCount || !got.OmittedFirst.Equal(want.OmittedFirst) || !got.OmittedLast.Equal(want.OmittedLast) {
		t.Fatalf("fork origin = %+v, want %+v", got, want)
	}
}

func mustForkUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// A fork of a session from before the upgrade keeps what its requests read.
// One that copies history past the source's origin takes the origin and the
// summaries that hold for the copy; one cut before the origin gets an origin
// placed for the copy with the source's budget. Either way its first request
// fits as the source's does, without compacting.
func TestPostgresForkOfAnUpgradedSessionKeepsItsContext(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	turns := f.turns(source, 12)
	f.summarize(source, "turns 1-2", turns[0], turns[1])
	// Beside the summary, 6499 bytes of room hold turns 10-12.
	f.placeOrigin(source)
	wantForkOrigin(t, f.origin(source), turns[9].userAt, 18, turns[0].userAt, turns[8].answerAt)
	legacy := f.summaries(source)

	t.Run("fork of the newest turn", func(t *testing.T) {
		fork := f.fork(source, turns[11])
		wantForkOrigin(t, f.origin(fork), turns[9].userAt, 18, turns[0].userAt, turns[8].answerAt)
		f.assertCarried(source, fork, legacy...)
		if got := f.candidates(fork); got != 6 {
			t.Fatalf("fork reads %d raw rows, want the 6 of turns 10-12", got)
		}
	})
	t.Run("fork after the origin", func(t *testing.T) {
		fork := f.fork(source, turns[10])
		wantForkOrigin(t, f.origin(fork), turns[9].userAt, 18, turns[0].userAt, turns[8].answerAt)
		f.assertCarried(source, fork, legacy...)
		if got := f.candidates(fork); got != 4 {
			t.Fatalf("fork reads %d raw rows, want the 4 of turns 10-11", got)
		}
	})
	t.Run("fork before the origin", func(t *testing.T) {
		// The copy reads 12000 raw bytes from turn 3 on; turns 6-8 fit.
		fork := f.fork(source, turns[7])
		wantForkOrigin(t, f.origin(fork), turns[5].userAt, 10, turns[0].userAt, turns[4].answerAt)
		f.assertCarried(source, fork, legacy...)
		if got := f.candidates(fork); got != 6 {
			t.Fatalf("fork reads %d raw rows, want the 6 of turns 6-8", got)
		}
	})
}

// A fork of a long session created after the upgrade carries the summaries
// that end before the fork point, which a replaced turn there would keep.
// The rows of a summary that reaches past it read raw in the fork; when they
// do not fit, an origin placed for the copy keeps the fork's first request
// within the source's budget.
func TestPostgresForkOfALongNewSessionCarriesItsSummaries(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	turns := f.turns(source, 14)
	f.summarize(source, "turns 1-5", turns[:5]...)
	f.summarize(source, "turns 6-12", turns[5:12]...)
	summaries := f.summaries(source)
	if len(summaries) != 2 || f.origin(source).Omits() {
		t.Fatalf("source has %d active summaries and origin %+v, want two and none", len(summaries), f.origin(source))
	}

	t.Run("fork of the newest turn", func(t *testing.T) {
		fork := f.fork(source, turns[13])
		if origin := f.origin(fork); !origin.At.IsZero() {
			t.Fatalf("fork origin = %+v, want none", origin)
		}
		f.assertCarried(source, fork, summaries...)
		if got := f.candidates(fork); got != 4 {
			t.Fatalf("fork reads %d raw rows, want the 4 of turns 13-14", got)
		}
	})
	t.Run("fork inside a summary", func(t *testing.T) {
		// The second summary reaches turn 12, so turns 6-11 read raw:
		// 12000 bytes, of which turns 9-11 fit beside the first summary.
		fork := f.fork(source, turns[10])
		wantForkOrigin(t, f.origin(fork), turns[8].userAt, 16, turns[0].userAt, turns[7].answerAt)
		f.assertCarried(source, fork, summaries[0])
		if got := f.candidates(fork); got != 6 {
			t.Fatalf("fork reads %d raw rows, want the 6 of turns 9-11", got)
		}
	})
}

// A source row that changed after a summary covered it is not covered by
// that summary, and neither is its copy in a fork: the carried coverage keeps
// the hash the summary saw.
func TestPostgresForkKeepsAStaleCoverageStale(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	turns := f.turns(source, 3)
	f.summarize(source, "turns 1-2", turns[0], turns[1])
	f.exec(`UPDATE bot_history_messages SET content = jsonb_set(content, '{content}', to_jsonb(repeat('y', 980))) WHERE id = $1`, turns[0].answer)
	summary := f.summaries(source)[0]

	fork := f.fork(source, turns[2])
	carried := f.summaries(fork)
	if len(carried) != 1 {
		t.Fatalf("fork has %d active summaries, want one", len(carried))
	}
	copies := f.copies(source, fork)
	sourceIDs := []pgtype.UUID{mustForkUUID(t, turns[0].answer), mustForkUUID(t, turns[1].answer)}
	copyIDs := []pgtype.UUID{mustForkUUID(t, copies[turns[0].answer]), mustForkUUID(t, copies[turns[1].answer])}
	sourceRecords, err := coverageRecords(f.ctx, f.queries, mustForkUUID(t, source), sourceIDs)
	if err != nil {
		t.Fatal(err)
	}
	copyRecords, err := coverageRecords(f.ctx, f.queries, mustForkUUID(t, fork), copyIDs)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CoversRecord(sourceRecords[sourceIDs[0]]) || carried[0].CoversRecord(copyRecords[copyIDs[0]]) {
		t.Fatal("a summary covers a row that changed after it was written")
	}
	if !summary.CoversRecord(sourceRecords[sourceIDs[1]]) || !carried[0].CoversRecord(copyRecords[copyIDs[1]]) {
		t.Fatal("a summary does not cover a row that did not change")
	}
}

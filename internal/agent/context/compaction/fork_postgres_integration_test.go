package compaction

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// forkFixture forks sessions of one bot through the thread service with the
// history carrier, against PostgreSQL. Every row it writes is 1000 bytes of
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
	return f
}

func (f *forkFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, query, args...); err != nil {
		f.t.Fatalf("exec: %v", err)
	}
}

// session creates a chat session.
func (f *forkFixture) session() string {
	f.t.Helper()
	sessionID := uuid.NewString()
	f.exec(`INSERT INTO bot_sessions (id, bot_id, channel_type, title) VALUES ($1, $2, 'local', 'Source')`, sessionID, f.botID)
	return sessionID
}

// turns appends n answered turns to the session.
func (f *forkFixture) turns(sessionID string, n int) []forkTurn {
	f.t.Helper()
	turns := make([]forkTurn, 0, n)
	for range n {
		var rows [2]messagepkg.Message
		for i, role := range []string{"user", "assistant"} {
			row, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: sessionID, Role: role, Content: []byte(`{"role":"` + role + `","content":""}`)})
			if err != nil {
				f.t.Fatalf("persist %s: %v", role, err)
			}
			f.exec(`UPDATE bot_history_messages
				SET created_at = $2,
				    content = jsonb_build_object('role', role, 'content',
				      repeat('x', 1000 - octet_length(jsonb_build_object('role', role, 'content', '')::text)))
				WHERE id = $1`, row.ID, f.next)
			f.next = f.next.Add(time.Second)
			rows[i] = row
		}
		historyTurn, err := f.messages.GetVisibleTurnByMessage(f.ctx, sessionID, rows[1].ID)
		if err != nil {
			f.t.Fatalf("read turn: %v", err)
		}
		turns = append(turns, forkTurn{id: historyTurn.ID, user: rows[0].ID, answer: rows[1].ID})
	}
	return turns
}

// summarize commits a 200-byte summary of the turns' rows onto the session's
// active checkpoint, as a pass that folds them does.
func (f *forkFixture) summarize(sessionID, label string, turns ...forkTurn) checkpoint.Checkpoint {
	f.t.Helper()
	rows := make([]string, 0, 2*len(turns))
	for _, turn := range turns {
		rows = append(rows, turn.user, turn.answer)
	}
	return f.fold(sessionID, label, rows...)
}

// fold commits a 200-byte summary of the rows onto the session's active
// checkpoint, as a pass that read every row of the session and folded these
// does: the rows between them that it does not fold stay in place.
func (f *forkFixture) fold(sessionID, label string, rows ...string) checkpoint.Checkpoint {
	f.t.Helper()
	text := label + strings.Repeat(".", 200-len(label))
	var summaryID string
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, message_count, started_at, completed_at)
		VALUES ($1, $2, 'ok', $3, $4, now(), now())
		RETURNING id::text`,
		f.botID, sessionID, text, len(rows)).Scan(&summaryID); err != nil {
		f.t.Fatalf("insert summary: %v", err)
	}
	folded := make([]checkpoint.Row, 0, len(rows))
	for _, id := range rows {
		folded = append(folded, checkpoint.Row{ID: id, Position: f.position(id)})
	}
	read := f.rowIDs(sessionID)
	var committed checkpoint.Checkpoint
	if err := f.queries.InTx(f.ctx, func(q dbstore.Queries) error {
		store := checkpoint.NewStore(slog.Default(), q)
		parent, err := store.Active(f.ctx, sessionID)
		if err != nil {
			return err
		}
		committed, err = store.Commit(f.ctx, q, checkpoint.Fold{SessionID: sessionID, Parent: parent, SummaryID: summaryID, Summary: text, Folded: folded, Read: read})
		return err
	}); err != nil {
		f.t.Fatalf("commit summary: %v", err)
	}
	return committed
}

func (f *forkFixture) fork(sessionID string, at forkTurn) string {
	f.t.Helper()
	fork, err := f.threads.ForkFromAssistantTurn(f.ctx, thread.ForkFromAssistantInput{BotID: f.botID, ThreadID: sessionID, TurnID: at.id, Title: "Fork", CreatedByUserID: f.userID})
	if err != nil {
		f.t.Fatalf("fork: %v", err)
	}
	return fork.ID
}

// active is the session's active checkpoint as a request renders it.
func (f *forkFixture) active(sessionID string) checkpoint.Checkpoint {
	f.t.Helper()
	active, err := checkpoint.NewStore(slog.Default(), f.queries).Active(f.ctx, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return active
}

// position is the row's place in its session's history.
func (f *forkFixture) position(rowID string) messagepkg.HistoryPosition {
	f.t.Helper()
	var position messagepkg.HistoryPosition
	if err := f.pool.QueryRow(f.ctx, `SELECT turn_position, turn_message_seq FROM bot_history_messages WHERE id = $1`, rowID).Scan(&position.Turn, &position.Seq); err != nil {
		f.t.Fatalf("read position: %v", err)
	}
	return position
}

// rowAt is the session's row at a place in its history.
func (f *forkFixture) rowAt(sessionID string, position messagepkg.HistoryPosition) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM bot_history_messages WHERE session_id = $1 AND turn_position = $2 AND turn_message_seq = $3`,
		sessionID, position.Turn, position.Seq).Scan(&id); err != nil {
		f.t.Fatalf("read row at %+v: %v", position, err)
	}
	return id
}

// rowIDs names every row of the session.
func (f *forkFixture) rowIDs(sessionID string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT id::text FROM bot_history_messages WHERE session_id = $1`, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return ids
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

// raw counts the rows a request of the session reads raw beside its active
// checkpoint: the rows after its position and the rows it keeps.
func (f *forkFixture) raw(sessionID string) int64 {
	f.t.Helper()
	active := f.active(sessionID)
	kept := []pgtype.UUID{}
	for _, id := range active.RowIDs() {
		kept = append(kept, mustForkUUID(f.t, id))
	}
	rows, err := f.queries.ListHistoryCheckpointCandidatesWithinBytes(f.ctx, dbsqlc.ListHistoryCheckpointCandidatesWithinBytesParams{
		SessionID:           mustForkUUID(f.t, sessionID),
		AfterTurnPosition:   active.Position.Turn,
		AfterTurnMessageSeq: active.Position.Seq,
		KeptIds:             kept,
		MaxBytes:            1 << 40,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return int64(len(rows))
}

// assertCopied checks that a fork's checkpoint is its own copy of a source
// checkpoint: a root at the copy's place for the source's position that
// holds the source's summaries as logs of the fork and keeps the copies of
// the rows the source keeps.
func (f *forkFixture) assertCopied(got, source checkpoint.Checkpoint, sourceSession string, copies map[string]string) {
	f.t.Helper()
	if got.ID == "" || got.ID == source.ID || got.ParentID != "" {
		f.t.Fatalf("fork checkpoint = %+v, want a root of its own", got)
	}
	if want := f.position(copies[f.rowAt(sourceSession, source.Position)]); got.Position != want {
		f.t.Fatalf("fork checkpoint at %+v, want the copy's place %+v of the source's %+v", got.Position, want, source.Position)
	}
	if len(got.Items) != len(source.Items) {
		f.t.Fatalf("fork checkpoint holds %+v, want a copy of %+v", got.Items, source.Items)
	}
	for i, item := range source.Items {
		carried := got.Items[i]
		if item.RowID != "" && carried.RowID != copies[item.RowID] {
			f.t.Fatalf("fork checkpoint item %d keeps %q, want the copy %q of %s", i, carried.RowID, copies[item.RowID], item.RowID)
		}
		if item.SummaryID != "" && (carried.SummaryID == "" || carried.SummaryID == item.SummaryID || carried.Summary != item.Summary) {
			f.t.Fatalf("fork checkpoint item %d = %+v, want the fork's own copy of the summary %+v", i, carried, item)
		}
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

// A fork of a long session carries the newest checkpoint that accounts for
// no history past the fork point, which a replaced turn there would keep.
// The rows a later checkpoint folds past it read raw in the fork.
func TestPostgresForkOfALongSessionCarriesItsCheckpoint(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	turns := f.turns(source, 14)
	first := f.summarize(source, "turns 1-5", turns[:5]...)
	second := f.summarize(source, "turns 6-12", turns[5:12]...)
	if second.ParentID != first.ID || len(second.Items) != 1 || second.Items[0].SummaryID == "" {
		t.Fatalf("source checkpoint = %+v, want one summary that absorbs the one of %s", second, first.ID)
	}

	t.Run("fork of the newest turn", func(t *testing.T) {
		fork := f.fork(source, turns[13])
		f.assertCopied(f.active(fork), second, source, f.copies(source, fork))
		if got := f.raw(fork); got != 4 {
			t.Fatalf("fork reads %d raw rows, want the 4 of turns 13-14", got)
		}
	})
	t.Run("fork inside a checkpoint", func(t *testing.T) {
		// The second checkpoint reaches turn 12, so the fork carries the
		// first, and turns 6-11 read raw.
		fork := f.fork(source, turns[10])
		f.assertCopied(f.active(fork), first, source, f.copies(source, fork))
		if got := f.raw(fork); got != 12 {
			t.Fatalf("fork reads %d raw rows, want the 12 of turns 6-11", got)
		}
	})
}

// A checkpoint that keeps a row by reference is carried with the fork's copy
// of the row in its place.
func TestPostgresForkKeepsTheCopiesOfTheRowsACheckpointKeeps(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	turns := f.turns(source, 4)
	// A pass folded turns 1-3 around turn 2's request, which it kept.
	kept := f.fold(source, "turns 1-3", turns[0].user, turns[0].answer, turns[1].answer, turns[2].user, turns[2].answer)
	if !slices.Equal(kept.RowIDs(), []string{turns[1].user}) {
		t.Fatalf("source checkpoint keeps %v, want turn 2's request", kept.RowIDs())
	}

	fork := f.fork(source, turns[3])
	f.assertCopied(f.active(fork), kept, source, f.copies(source, fork))
	if got := f.raw(fork); got != 3 {
		t.Fatalf("fork reads %d raw rows, want the kept request and the 2 of turn 4", got)
	}
}

// A fork carries its source's summaries naming the fork's copies of the rows
// they cite: a tool result a summary cites by ref reads back in the fork,
// where the source's row is not the fork's to read.
func TestPostgresForkSummariesCiteTheForksRows(t *testing.T) {
	f := newForkFixture(t)
	source := f.session()
	persist := func(message sdk.Message) string {
		t.Helper()
		content, err := historyfrag.MarshalStoredSDKMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		row, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: source, Role: string(message.Role), Content: content})
		if err != nil {
			t.Fatalf("persist %s: %v", message.Role, err)
		}
		f.exec(`UPDATE bot_history_messages SET created_at = $2 WHERE id = $1`, row.ID, f.next)
		f.next = f.next.Add(time.Second)
		return row.ID
	}
	log := "BUILD_LOG: compiled 42 packages"
	rows := []string{
		persist(sdk.UserMessage("build it")),
		persist(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "exec", Input: sdk.ParseToolArguments(`{"cmd":"make"}`)}}}),
		persist(sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: sdk.TextOutput(log)})),
		persist(sdk.AssistantMessage("built")),
	}
	later := f.turns(source, 1)
	f.fold(source, "built; ref="+rows[2]+":0", rows...)

	fork := f.fork(source, later[0])
	items := f.active(fork).Items
	at := slices.IndexFunc(items, func(item checkpoint.Item) bool { return item.SummaryID != "" })
	if at < 0 {
		t.Fatalf("fork checkpoint holds %+v, want the source's summary", items)
	}
	summary := items[at].Summary
	cited := regexp.MustCompile(`ref=([0-9a-f-]{36}):0`).FindStringSubmatch(summary)
	if cited == nil || cited[1] == rows[2] {
		t.Fatalf("the fork's summary %q cites %v, want the fork's copy of %s", summary, cited, rows[2])
	}
	limit := tools.ToolOutputLimit{MaxBytes: 4096, MaxLines: 64}
	provided, err := tools.NewHistoryProvider(nil, nil, f.messages, nil, limit).Tools(f.ctx, tools.SessionContext{BotID: f.botID, SessionID: fork})
	if err != nil {
		t.Fatal(err)
	}
	read := func(ref string) (string, error) {
		for _, tool := range provided {
			if tool.Name == tools.ToolGetMessages().String() {
				out, err := tool.Execute(&toolexec.ToolExecContext{Context: f.ctx, ToolName: tool.Name}, toolexec.ArgumentsFromValue(map[string]any{"ref": ref}))
				return out.String(), err
			}
		}
		t.Fatal("no get_messages tool")
		return "", nil
	}
	if out, err := read(cited[1] + ":0"); err != nil || !strings.Contains(out, log) {
		t.Fatalf("get_messages in the fork = %s, %v; want the cited result", out, err)
	}
	if _, err := read(rows[2] + ":0"); err == nil || !strings.Contains(err.Error(), "not in the current session") {
		t.Fatalf("the source's ref read in the fork: %v", err)
	}
}

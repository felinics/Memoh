package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// fold runs one compaction event over the fixture's session, by default for
// a request with a 300-token history budget, whose verbatim budget holds none
// of the fixture's rows: an event folds everything it does not keep as an
// anchor.
func (f *backlogFixture) fold(ctx context.Context, cfg compaction.TriggerConfig) ([]compaction.Pass, error) {
	cfg.BotID, cfg.SessionID = f.botID, f.sessionID
	cfg.ModelID, cfg.ClientType, cfg.APIKey, cfg.BaseURL = "compact-model", "openai-completions", "test-key", f.providerURL
	cfg.SummaryWindowTokens = cmp.Or(cfg.SummaryWindowTokens, 128000)
	cfg.HistoryBudgetTokens = cmp.Or(cfg.HistoryBudgetTokens, 300)
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	return compaction.NewService(slog.Default(), queries).Compact(ctx, cfg)
}

// foldedRows lists the history rows the session's active checkpoint folds,
// in history order.
func (f *backlogFixture) foldedRows() []string {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT h.id::text FROM (`+historyRowsSQL+`) h WHERE h.folded ORDER BY h.turn_position, h.turn_message_seq`, f.sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return ids
}

// activeSummaryIDs names the summaries the session's active checkpoint holds.
func (f *backlogFixture) activeSummaryIDs() []string {
	f.t.Helper()
	active, err := checkpoint.NewStore(slog.Default(), postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))).Active(f.ctx, f.sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return active.SummaryIDs()
}

// A pass folds onto the session's active checkpoint: the summary it holds
// and the row it keeps raw go, with the rows after it, into the one summary
// the pass writes. The summarizer reads both, and the session is left with
// one checkpoint that holds one summary and keeps nothing raw.
func TestPostgresCompactionFoldsTheActiveCheckpointIntoOneSummary(t *testing.T) {
	f := newBacklogFixture(t)
	for i := range 3 {
		f.appendMessage(fmt.Sprintf("earlier-%d", i), fmt.Sprintf("earlier-%d ", i)+strings.Repeat("y", 400))
	}
	kept := turn.ContextMessageSource{Kind: "history", ID: f.rowID("earlier-1")}
	// The budget holds the anchor, not the newest row beside it.
	first, err := f.fold(f.ctx, compaction.TriggerConfig{Anchors: []turn.ContextMessageSource{kept}, HistoryBudgetTokens: 600})
	if err != nil || len(first) != 1 {
		t.Fatalf("fold the earlier rows around the one kept raw: passes=%+v err=%v", first, err)
	}
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	checkpoints := checkpoint.NewStore(slog.Default(), queries)
	held, err := checkpoints.Active(f.ctx, f.sessionID)
	if err != nil || !slices.Equal(held.RowIDs(), []string{kept.ID}) || !slices.Equal(held.SummaryIDs(), []string{first[0].SummaryID}) {
		t.Fatalf("active checkpoint rows=%v summaries=%v err=%v, want the kept row and the first summary", held.RowIDs(), held.SummaryIDs(), err)
	}
	f.appendBacklog(4, 400)
	before := f.foldedRows()

	var read string
	f.onSummary = func(body []byte) { read = string(body) }
	passes, err := f.fold(f.ctx, compaction.TriggerConfig{})
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	if !strings.Contains(read, "Earlier backlog summarized.") || !strings.Contains(read, "earlier-1 ") || !strings.Contains(read, "backlog-3 ") {
		t.Fatal("the summarizer did not read the held summary, the kept row and the backlog")
	}
	if held := f.activeSummaryIDs(); !slices.Equal(held, []string{passes[0].SummaryID}) {
		t.Fatalf("active summaries=%v, want the pass's own in place of the one the checkpoint held (%s)", held, first[0].SummaryID)
	}
	want := []string{kept.ID, f.rowID("backlog-0"), f.rowID("backlog-1"), f.rowID("backlog-2"), f.rowID("backlog-3")}
	if folded := slices.DeleteFunc(f.foldedRows(), func(id string) bool { return slices.Contains(before, id) }); !slices.Equal(folded, want) {
		t.Fatalf("folded %v, want the kept row and the backlog %v", folded, want)
	}
	active, err := checkpoints.Active(f.ctx, f.sessionID)
	if err != nil || active.ParentID != held.ID || len(active.RowIDs()) != 0 || !slices.Equal(active.SummaryIDs(), []string{passes[0].SummaryID}) {
		t.Fatalf("active checkpoint parent=%s rows=%v summaries=%v err=%v, want one summary over %s and nothing kept", active.ParentID, active.RowIDs(), active.SummaryIDs(), err, held.ID)
	}
	if raw, compacted := f.backlogRowsPrefixed("earlier-"); raw != 0 || compacted != 3 {
		t.Fatalf("earlier rows raw=%d compacted=%d, want all of them folded", raw, compacted)
	}
}

// Compaction summarizes the thread's history however old it is, but an edit
// or retry compacts only the history before its cutoff: rows after it stay
// raw instead of being folded into a summary the request then carries.
func TestPostgresCompactionStopsAtTheCutoff(t *testing.T) {
	f := newBacklogFixture(t)
	f.appendBacklog(3, 400)
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_messages SET created_at=created_at - interval '2 days' WHERE session_id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		f.appendMessage(fmt.Sprintf("view-%d", i), fmt.Sprintf("view-%d ", i)+strings.Repeat("y", 400))
	}
	for i := range 2 {
		f.appendMessage(fmt.Sprintf("after-%d", i), fmt.Sprintf("after-%d ", i)+strings.Repeat("z", 400))
	}
	cutoff := f.position("after-0")

	if passes, err := f.fold(f.ctx, compaction.TriggerConfig{CandidatesBefore: &cutoff}); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	if raw, compacted := f.backlogRows(); raw != 0 || compacted != 3 {
		t.Fatalf("backlog days old: raw=%d compacted=%d, want all 3 summarized", raw, compacted)
	}
	if raw, compacted := f.backlogRowsPrefixed("after-"); raw != 2 || compacted != 0 {
		t.Fatalf("rows after the cutoff: raw=%d compacted=%d, want both left raw", raw, compacted)
	}
	if _, compacted := f.backlogRowsPrefixed("view-"); compacted == 0 {
		t.Fatal("rows inside the view were not compacted")
	}
}

// An edit or retry that compacts before its first step commits folds onto the
// summaries its replacement keeps. The replaced turn's run wrote a summary
// that reaches past the cutoff; the event first rewinds the session to before
// the turn, as the replacement will, so the summary that ends before it is
// active again and the turn's rows are left raw for the attempt to replace.
func TestPostgresCompactionRewindsTheReplacedTurnBeforeFolding(t *testing.T) {
	f := newBacklogFixture(t)
	base := time.Now().Add(-time.Hour)
	f.persistHistory("early-", 4, 400, base)
	early, err := f.fold(f.ctx, compaction.TriggerConfig{})
	if err != nil || len(early) != 1 {
		t.Fatalf("fold the early history: passes=%+v err=%v", early, err)
	}
	f.persistHistory("later-", 4, 400, base.Add(10*time.Second))
	f.persistHistory("replaced-", 2, 400, base.Add(20*time.Second))
	var turnID string
	if err := f.pool.QueryRow(f.ctx, `SELECT turn_id::text FROM bot_history_messages WHERE session_id=$1 AND source_message_id='replaced-0'`, f.sessionID).Scan(&turnID); err != nil {
		t.Fatal(err)
	}
	turnStart := f.position("replaced-0")
	// The replaced turn's run folded its own rows with everything before them.
	if spanning, err := f.fold(f.ctx, compaction.TriggerConfig{}); err != nil || len(spanning) != 1 {
		t.Fatalf("fold the replaced turn: passes=%+v err=%v", spanning, err)
	}

	passes, err := f.fold(f.ctx, compaction.TriggerConfig{CandidatesBefore: &turnStart, ReplacedTurnID: turnID})
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v, want the edit's event to fold once the replaced turn is rewound", passes, err)
	}
	var parent string
	if err := f.pool.QueryRow(f.ctx, `SELECT p.items::text FROM bot_sessions s
		JOIN bot_history_checkpoints c ON c.id = s.history_checkpoint_id
		JOIN bot_history_checkpoints p ON p.id = c.parent_id
		WHERE s.id = $1`, f.sessionID).Scan(&parent); err != nil || !strings.Contains(parent, early[0].SummaryID) {
		t.Fatalf("the event's checkpoint builds on %s (%v), want the one holding only the summary that ends before the turn (%s)", parent, err, early[0].SummaryID)
	}
	if raw, compacted := f.backlogRowsPrefixed("later-"); raw != 0 || compacted != 4 {
		t.Fatalf("rows between the earlier summary and the turn raw=%d compacted=%d, want the 4 folded", raw, compacted)
	}
	if raw, compacted := f.backlogRowsPrefixed("replaced-"); raw != 2 || compacted != 0 {
		t.Fatalf("replaced turn rows raw=%d compacted=%d, want both left raw", raw, compacted)
	}
}

// An event folds the whole backlog. A pass folds what one summarizer call can
// read, and the event passes again only for what that call could not: each
// further pass folds the summary before it with the next rows, so the event
// leaves one active summary and nothing raw.
func TestPostgresCompactionEventFoldsTheWholeBacklog(t *testing.T) {
	f := newBacklogFixture(t)
	f.appendBacklog(6, 1600)
	passes, err := f.fold(f.ctx, compaction.TriggerConfig{SummaryWindowTokens: 2400})
	if err != nil || len(passes) < 2 || int(f.summaries.Load()) != len(passes) {
		t.Fatalf("passes=%d summarizer calls=%d err=%v, want one call per pass and a backlog one call cannot read", len(passes), f.summaries.Load(), err)
	}
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	active, err := checkpoint.NewStore(slog.Default(), queries).Active(f.ctx, f.sessionID)
	if err != nil || len(active.RowIDs()) != 0 || !slices.Equal(active.SummaryIDs(), []string{passes[len(passes)-1].SummaryID}) {
		t.Fatalf("active checkpoint rows=%v summaries=%v err=%v, want the last pass as its one summary", active.RowIDs(), active.SummaryIDs(), err)
	}
	if raw, compacted := f.backlogRows(); raw != 0 || compacted != 6 {
		t.Fatalf("backlog raw=%d compacted=%d after %d passes, want all of it folded", raw, compacted, len(passes))
	}
}

// A compaction pass commits as the session's runtime owner, onto the
// history it read. An owner that lost the session cannot commit; a pass its
// crashed owner left pending is settled when the next owner activates. Each
// time what is left of the backlog stays raw and the next event folds it.
func TestPostgresCompactionCommitsOnlyOntoTheHistoryItRead(t *testing.T) {
	for _, interruption := range []string{"owner replaced", "owner crashed mid-pass"} {
		t.Run(interruption, func(t *testing.T) {
			f := newBacklogFixture(t)
			f.appendBacklog(4, 400)
			const backlog = 4
			queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
			f.activate(t, queries, 1)
			switch interruption {
			case "owner replaced":
				f.onSummary = func([]byte) { f.activate(t, queries, 2) }
			case "owner crashed mid-pass":
				openPass(t, f, queries)
			}
			if interruption != "owner crashed mid-pass" {
				passes, err := f.fold(f.owner(1), compaction.TriggerConfig{})
				if len(passes) != 0 || compaction.ReasonOf(err) != compaction.ReasonStaleSource {
					t.Fatalf("interrupted event passes=%d err=%v, want %s", len(passes), err, compaction.ReasonStaleSource)
				}
			}
			if raw, compacted := f.backlogRows(); raw != backlog || compacted != 0 {
				t.Fatalf("after the interruption raw=%d compacted=%d, want the backlog of %d left raw", raw, compacted, backlog)
			}
			f.onSummary = nil
			if interruption != "owner replaced" {
				f.activate(t, queries, 2)
			}
			if passes, err := f.fold(f.owner(2), compaction.TriggerConfig{}); err != nil || len(passes) != 1 {
				t.Fatalf("next event passes=%+v err=%v", passes, err)
			}
			if raw, compacted := f.backlogRows(); raw != 0 || compacted != backlog {
				t.Fatalf("after the next event raw=%d compacted=%d, want the backlog of %d folded", raw, compacted, backlog)
			}
			var pending int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE session_id=$1 AND status='pending'`, f.sessionID).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("pending passes=%d err=%v", pending, err)
			}
		})
	}
}

// rowID is the history row of the inbound message id.
func (f *backlogFixture) rowID(id string) string {
	f.t.Helper()
	var row string
	if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM bot_history_messages WHERE session_id=$1 AND source_message_id=$2`, f.sessionID, id).Scan(&row); err != nil {
		f.t.Fatal(err)
	}
	return row
}

// position is the place in the history of the inbound message id's row.
func (f *backlogFixture) position(id string) messagepkg.HistoryPosition {
	f.t.Helper()
	position, err := f.messages.HistoryPositionByID(f.ctx, f.sessionID, f.rowID(id))
	if err != nil {
		f.t.Fatal(err)
	}
	return position
}

// owner is the context a run that holds token writes in.
func (f *backlogFixture) owner(token int64) context.Context {
	return runtimefence.WithContext(f.ctx, runtimefence.Fence{BotID: f.botID, SessionID: f.sessionID, Token: token})
}

// activate makes token the session's runtime owner.
func (f *backlogFixture) activate(t *testing.T, queries dbstore.Queries, token int64) {
	t.Helper()
	if err := runtimefence.Activate(f.ctx, queries, runtimefence.Fence{BotID: f.botID, SessionID: f.sessionID, Token: token}); err != nil {
		t.Error(err)
	}
}

// openPass records a pass the way a compaction does before its summarizer
// call, and stops there.
func openPass(t *testing.T, f *backlogFixture, queries dbstore.Queries) {
	t.Helper()
	if _, err := queries.CreateCompactionLog(f.ctx, sqlc.CreateCompactionLogParams{BotID: db.ParseUUIDOrEmpty(f.botID), SessionID: db.ParseUUIDOrEmpty(f.sessionID)}); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// fold runs one compaction event over the fixture's session, by default for
// a request with a 100000-token history budget.
func (f *backlogFixture) fold(ctx context.Context, cfg compaction.TriggerConfig) ([]compaction.Pass, error) {
	cfg.BotID, cfg.SessionID = f.botID, f.sessionID
	cfg.ModelID, cfg.ClientType, cfg.APIKey, cfg.BaseURL = "compact-model", "openai-completions", "test-key", f.providerURL
	cfg.SummaryWindowTokens = cmp.Or(cfg.SummaryWindowTokens, 128000)
	cfg.HistoryBudgetTokens = cmp.Or(cfg.HistoryBudgetTokens, 100000)
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	return compaction.NewService(slog.Default(), queries).Compact(ctx, cfg)
}

// foldedRows lists the history rows passes folded.
func foldedRows(passes ...compaction.Pass) []string {
	var ids []string
	for _, pass := range passes {
		for _, source := range pass.Covered {
			if source.Kind == "history" {
				ids = append(ids, source.ID)
			}
		}
	}
	return ids
}

// A summary written before artifacts carried coverage is folded into the next
// summary, and the read path accepts the lineage: one active summary covering
// both the legacy rows and the newly compacted ones.
func TestPostgresCompactionFoldsALegacySummaryIntoOne(t *testing.T) {
	f := newBacklogFixture(t)
	var legacyID string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO bot_history_message_compacts(bot_id,session_id,status,summary,completed_at) VALUES($1,$2,'ok','Legacy summary.',now()) RETURNING id`, f.botID, f.sessionID).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	legacyRows := map[string]bool{}
	for _, text := range []string{"legacy question", "legacy answer"} {
		content, _ := json.Marshal(text)
		msg, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: f.sessionID, Role: "user", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_messages SET compact_id=$2 WHERE id=$1`, msg.ID, legacyID); err != nil {
			t.Fatal(err)
		}
		legacyRows[msg.ID] = true
	}
	f.appendBacklog(4, 400)

	passes, err := f.fold(f.ctx, compaction.TriggerConfig{})
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	frontier, err := compaction.NewArtifactProjection(queries).LoadActiveSession(f.ctx, compaction.ArtifactOwner{BotID: f.botID, SessionID: f.sessionID, SessionIDKnown: true})
	if err != nil || len(frontier.Issues) != 0 || len(frontier.Artifacts) != 1 {
		t.Fatalf("frontier=%+v err=%v, want one active summary and no lineage issue", frontier, err)
	}
	active := frontier.Artifacts[0]
	if len(active.ParentIDs) != 1 || active.ParentIDs[0] != legacyID {
		t.Fatalf("active summary parents=%v, want the legacy summary %s", active.ParentIDs, legacyID)
	}
	covered := 0
	for _, source := range active.Coverage {
		if legacyRows[source.Ref.ID] {
			covered++
		}
	}
	if folded := len(foldedRows(passes...)); covered != len(legacyRows) || len(active.Coverage) != len(legacyRows)+folded {
		t.Fatalf("coverage=%d legacy=%d/%d new=%d, want legacy and new rows covered once", len(active.Coverage), covered, len(legacyRows), folded)
	}
}

// Compaction only summarizes rows inside the caller's history range: an older
// backlog, and rows after an edit or retry cutoff, stay raw instead of being
// folded into a summary the request then carries.
func TestPostgresCompactionLeavesRowsOutsideTheViewRaw(t *testing.T) {
	f := newBacklogFixture(t)
	f.appendBacklog(3, 400)
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_messages SET created_at=created_at - interval '2 days' WHERE session_id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		f.appendMessage(fmt.Sprintf("view-%d", i), fmt.Sprintf("view-%d ", i)+strings.Repeat("y", 400))
	}
	cutoff := f.base.Add(time.Duration(len(f.rc)+1) * time.Second)
	for i := range 2 {
		f.appendMessage(fmt.Sprintf("after-%d", i), fmt.Sprintf("after-%d ", i)+strings.Repeat("z", 400))
	}

	if passes, err := f.fold(f.ctx, compaction.TriggerConfig{CandidatesFrom: f.base, CandidatesBefore: cutoff}); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	if raw, compacted := f.backlogRows(); raw != 3 || compacted != 0 {
		t.Fatalf("backlog outside the view: raw=%d compacted=%d, want all 3 left raw", raw, compacted)
	}
	if raw, compacted := f.backlogRowsPrefixed("after-"); raw != 2 || compacted != 0 {
		t.Fatalf("rows after the cutoff: raw=%d compacted=%d, want both left raw", raw, compacted)
	}
	if _, compacted := f.backlogRowsPrefixed("view-"); compacted == 0 {
		t.Fatal("rows inside the view were not compacted")
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
	for i, pass := range passes {
		if pass.More != (i < len(passes)-1) {
			t.Fatalf("pass %d More=%t, want only the last pass to leave nothing to read", i, pass.More)
		}
		if i > 0 && !slices.Equal(pass.Absorbed, []string{passes[i-1].ArtifactID}) {
			t.Fatalf("pass %d absorbed %v, want the summary of pass %d", i, pass.Absorbed, i-1)
		}
	}
	queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	frontier, err := compaction.NewArtifactProjection(queries).LoadActiveSession(f.ctx, compaction.ArtifactOwner{BotID: f.botID, SessionID: f.sessionID, SessionIDKnown: true})
	if err != nil || len(frontier.Artifacts) != 1 || frontier.Artifacts[0].ID != passes[len(passes)-1].ArtifactID {
		t.Fatalf("frontier=%+v err=%v, want the last pass as the one active summary", frontier.Artifacts, err)
	}
	if raw, compacted := f.backlogRows(); raw != 0 || compacted != len(foldedRows(passes...)) {
		t.Fatalf("backlog raw=%d compacted=%d after %d passes, want all of it folded", raw, compacted, len(passes))
	}
}

// A pass folds only what the caller's request renders. Rows the request does
// not show stay raw. Anchors stay raw in place, and the summarizer reads them
// there, marked as kept. The tail the request keeps ends what is folded.
func TestPostgresCompactionFoldsTheViewAroundItsAnchors(t *testing.T) {
	f := newBacklogFixture(t)
	for _, id := range []string{"old-0", "old-1", "input", "step-0", "outside", "step-1", "tail"} {
		f.appendMessage(id, id+" "+strings.Repeat("x", 200))
	}
	external := make(map[string]turn.ContextMessageSource)
	for _, segment := range f.rc {
		external[segment.MessageID] = turn.ContextMessageSource{Kind: "external", ID: segment.MessageID, EventCursor: segment.LastEventCursor}
	}
	var read strings.Builder
	f.onSummary = func(body []byte) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
		}
		for _, message := range request.Messages {
			read.WriteString(message.Content)
		}
	}
	history := func(id string) turn.ContextMessageSource {
		return turn.ContextMessageSource{Kind: "history", ID: f.rowID(id)}
	}
	cfg := compaction.TriggerConfig{
		View: []turn.ContextMessageSource{
			history("old-0"), external["old-1"], history("step-0"), external["step-1"], history("tail"),
		},
		Anchors:          []turn.ContextMessageSource{history("input")},
		ProtectedSources: []turn.ContextMessageSource{history("tail")},
	}
	passes, err := f.fold(f.ctx, cfg)
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	want := []string{f.rowID("old-0"), f.rowID("old-1"), f.rowID("step-0"), f.rowID("step-1")}
	if got := foldedRows(passes...); !slices.Equal(got, want) {
		t.Fatalf("folded %v, want %v", got, want)
	}
	for _, id := range []string{"input", "outside", "tail"} {
		if raw, _ := f.backlogRowsPrefixed(id); raw != 1 {
			t.Fatalf("%s was folded", id)
		}
	}
	prompt := read.String()
	order := []string{"old-1 ", "<kept>\nuser: [message_id: input]\n", "\ninput ", "</kept>", "step-0 "}
	at := 0
	for _, text := range order {
		next := strings.Index(prompt[at:], text)
		if next < 0 {
			t.Fatalf("summarizer prompt lacks %q after offset %d:\n%s", text, at, prompt)
		}
		at += next
	}
	if strings.Contains(prompt, "outside ") {
		t.Fatal("the summarizer read a row the request does not render")
	}
}

// A compaction claims and commits as the session's runtime owner. An owner
// that lost the session, or rows that changed after their claim, cannot
// commit; a claim its crashed owner left pending is released when the next
// owner activates. Each time the rows stay raw and the next event folds them.
func TestPostgresCompactionCommitsOnlyWhatItsOwnerClaimed(t *testing.T) {
	for _, interruption := range []string{"owner replaced", "row changed", "owner crashed after claim"} {
		t.Run(interruption, func(t *testing.T) {
			f := newBacklogFixture(t)
			f.appendBacklog(4, 400)
			queries := postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
			f.activate(t, queries, 1)
			switch interruption {
			case "owner replaced":
				f.onSummary = func([]byte) { f.activate(t, queries, 2) }
			case "row changed":
				f.onSummary = func([]byte) {
					if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_messages SET content='"backlog-1 edited"'::jsonb WHERE session_id=$1 AND source_message_id='backlog-1'`, f.sessionID); err != nil {
						t.Error(err)
					}
				}
			case "owner crashed after claim":
				claimBacklog(t, f, queries)
			}
			if interruption != "owner crashed after claim" {
				passes, err := f.fold(f.owner(1), compaction.TriggerConfig{})
				if len(passes) != 0 || compaction.ReasonOf(err) != compaction.ReasonStaleSource {
					t.Fatalf("interrupted event passes=%d err=%v, want %s", len(passes), err, compaction.ReasonStaleSource)
				}
			}
			if raw, compacted := f.backlogRows(); raw != 4 || compacted != 0 {
				t.Fatalf("after the interruption raw=%d compacted=%d, want the backlog left raw", raw, compacted)
			}
			f.onSummary = nil
			if interruption != "owner replaced" {
				f.activate(t, queries, 2)
			}
			if passes, err := f.fold(f.owner(2), compaction.TriggerConfig{}); err != nil || len(passes) != 1 {
				t.Fatalf("next event passes=%+v err=%v", passes, err)
			}
			if raw, compacted := f.backlogRows(); raw != 0 || compacted != 4 {
				t.Fatalf("after the next event raw=%d compacted=%d, want the backlog folded", raw, compacted)
			}
			var pending int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE session_id=$1 AND status='pending'`, f.sessionID).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("pending claims=%d err=%v", pending, err)
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

// claimBacklog claims the backlog the way a compaction does, and stops there.
func claimBacklog(t *testing.T, f *backlogFixture, queries dbstore.Queries) {
	t.Helper()
	botID, sessionID := db.ParseUUIDOrEmpty(f.botID), db.ParseUUIDOrEmpty(f.sessionID)
	var epoch int64
	if err := f.pool.QueryRow(f.ctx, `SELECT compaction_epoch FROM bot_sessions WHERE id=$1`, f.sessionID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	log, err := queries.CreateCompactionLog(f.ctx, sqlc.CreateCompactionLogParams{BotID: botID, SessionID: sessionID, ExpectedEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Query(f.ctx, `SELECT id FROM bot_history_messages WHERE session_id=$1 AND source_message_id LIKE 'backlog-%' ORDER BY created_at`, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	marked, err := queries.MarkMessagesCompacted(f.ctx, sqlc.MarkMessagesCompactedParams{CompactID: log.ID, MessageIds: ids, ExpectedCompactIds: make([]pgtype.UUID, len(ids))})
	if err != nil || marked != int64(len(ids)) {
		t.Fatalf("claimed %d of %d rows: %v", marked, len(ids), err)
	}
}

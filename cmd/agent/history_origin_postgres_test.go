package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

// originFixture drives chat sessions of the backlog fixture's bot, and forks
// them through the thread service the server wires.
type originFixture struct {
	*backlogFixture
	queries *postgresstore.Queries
	threads *thread.Service
}

func newOriginFixture(t *testing.T) *originFixture {
	t.Helper()
	f := &originFixture{backlogFixture: newBacklogFixture(t)}
	f.queries = postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))
	f.threads = thread.NewService(slog.Default(), f.queries, nil)
	f.threads.SetForkHistoryCarrier(compaction.ForkHistoryCarrier{})
	f.exec(`UPDATE bot_sessions SET type='chat', channel_type='local' WHERE id=$1`, f.sessionID)
	return f
}

func (f *originFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *originFixture) newChatSession() string {
	f.t.Helper()
	sessionID := uuid.NewString()
	f.exec(`INSERT INTO bot_sessions (id, bot_id, channel_type, type) VALUES ($1, $2, 'local', 'chat')`, sessionID, f.botID)
	return sessionID
}

// chat runs a chat turn on the session and reports its failure code, "" when
// it was answered.
func (f *originFixture) chat(sessionID, text string) string {
	f.t.Helper()
	handle, err := f.service.StartTurn(f.ctx, turn.StartTurnCommand{TeamID: f.cfg.TeamID, Mode: turn.ModeChat, BotID: f.botID, ChatID: f.botID, ThreadID: sessionID, Query: text, CurrentChannel: "local"})
	if err != nil {
		f.t.Fatal(err)
	}
	for range handle.Events() {
	}
	if err := <-handle.Errs(); err != nil {
		return string(apperror.CodeOf(err))
	}
	return ""
}

// answered runs a chat turn that must be answered by one provider call
// without a summary, and returns the provider request.
func (f *originFixture) answered(sessionID, text string) string {
	f.t.Helper()
	summaries, calls := f.summaries.Load(), f.modelCalls.Load()
	if code := f.chat(sessionID, text); code != "" || f.modelCalls.Load() != calls+1 || f.summaries.Load() != summaries {
		f.t.Fatalf("%q: code=%q provider_calls=%d summarizer_calls=%d, want one provider call and no summary", text, code, f.modelCalls.Load()-calls, f.summaries.Load()-summaries)
	}
	request, _ := f.lastRequest.Load().(string)
	if !strings.Contains(request, text) {
		f.t.Fatalf("%q: the provider request does not carry the input", text)
	}
	return request
}

// persistAt writes answered history from before the session's turns:
// user/assistant pairs a second apart from at, each message its id followed
// by size bytes.
func (f *originFixture) persistAt(sessionID, prefix string, count, size int, at time.Time) {
	f.t.Helper()
	for i := range count {
		id := f.persistRow(sessionID, prefix, i, size)
		f.exec(`UPDATE bot_history_messages SET created_at=$2 WHERE id=$1`, id, at.Add(time.Duration(i)*time.Second))
	}
}

// persist writes answered history as it arrives, each row a few
// milliseconds after the one before.
func (f *originFixture) persist(sessionID, prefix string, count, size int) {
	f.t.Helper()
	for i := range count {
		f.persistRow(sessionID, prefix, i, size)
		time.Sleep(2 * time.Millisecond)
	}
}

func (f *originFixture) persistRow(sessionID, prefix string, i, size int) string {
	f.t.Helper()
	id := fmt.Sprintf("%s%d", prefix, i)
	role := "user"
	if i%2 == 1 {
		role = "assistant"
	}
	content, _ := json.Marshal(id + " " + strings.Repeat("x", size))
	msg, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: sessionID, ExternalMessageID: id, Role: role, Content: content})
	if err != nil {
		f.t.Fatal(err)
	}
	return msg.ID
}

// plannedBudget is the history budget the session's newest run planned.
func (f *originFixture) plannedBudget(sessionID string) int {
	f.t.Helper()
	var budget int
	if err := f.pool.QueryRow(f.ctx, `SELECT (snapshot->'budget_plan'->>'history_budget')::int FROM context_lifecycles WHERE session_id=$1 ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&budget); err != nil {
		f.t.Fatal(err)
	}
	return budget
}

// upgrade gives the session a run that planned budget, as the session's runs
// before the upgrade did, and places its origin as the upgrade does.
func (f *originFixture) upgrade(sessionID string, budget int) messagepkg.HistoryOrigin {
	f.t.Helper()
	f.exec(`INSERT INTO context_lifecycles (run_id, bot_id, session_id, status, snapshot, created_at)
		VALUES ($1, $2, $3, 'completed', jsonb_build_object('budget_plan', jsonb_build_object('history_budget', $4::int)), now() - interval '1 day')`,
		uuid.NewString(), f.botID, sessionID, budget)
	f.exec(`SELECT public.memoh_place_history_origin($1, public.memoh_session_history_budget($1))`, sessionID)
	origin := f.origin(sessionID)
	if !origin.Omits() {
		f.t.Fatalf("the upgrade placed no origin for the session: %+v", origin)
	}
	return origin
}

func (f *originFixture) origin(sessionID string) messagepkg.HistoryOrigin {
	f.t.Helper()
	origin, err := f.messages.HistoryOriginBySession(f.ctx, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return origin
}

// created is when the session's row for the inbound id was written.
func (f *originFixture) created(sessionID, id string) time.Time {
	f.t.Helper()
	var at time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT created_at FROM bot_history_messages WHERE session_id=$1 AND source_message_id=$2`, sessionID, id).Scan(&at); err != nil {
		f.t.Fatal(err)
	}
	return at
}

// fork forks the session at the turn of the newest answer whose text
// starts with prefix.
func (f *originFixture) fork(sessionID, prefix string) string {
	f.t.Helper()
	var turnID string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT turn_id::text FROM bot_history_messages
		WHERE session_id=$1 AND role='assistant' AND turn_visible AND content::text LIKE '%' || $2 || '%'
		ORDER BY created_at DESC LIMIT 1`, sessionID, prefix).Scan(&turnID); err != nil {
		f.t.Fatal(err)
	}
	fork, err := f.threads.ForkFromAssistantTurn(f.ctx, thread.ForkFromAssistantInput{BotID: f.botID, ThreadID: sessionID, TurnID: turnID, Title: "Fork", CreatedByUserID: f.ownerUserID})
	if err != nil {
		f.t.Fatal(err)
	}
	return fork.ID
}

// activeSummaryTexts lists the session's active summaries.
func (f *originFixture) activeSummaryTexts(sessionID string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT summary FROM bot_history_message_compacts WHERE session_id=$1 AND status='ok' AND superseded_by IS NULL ORDER BY anchor_start_ms`, sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			f.t.Fatal(err)
		}
		texts = append(texts, text)
	}
	return texts
}

const legacySummary = "Legacy summary of the first backlog."

// A session from before the upgrade answers its first request from its
// origin on, without compacting: the notice says what was left out, its
// summaries still speak for what they cover, and the months of history
// before the origin stay out. Forks of it, before or after the origin,
// answer the same way. The first request that overflows folds the legacy
// summary into the one summary it writes, and the notice stays.
func TestPostgresUpgradedChatSessionAnswersFromItsOrigin(t *testing.T) {
	f := newOriginFixture(t)
	// What a run of this bot's chat sessions plans for history.
	probe := f.newChatSession()
	f.answered(probe, "probe message")
	budget := f.plannedBudget(probe)

	session := f.sessionID
	f.persistAt(session, "legacy-", 8, 400, time.Now().Add(-60*24*time.Hour))
	if passes, err := f.fold(f.ctx, compaction.TriggerConfig{}); err != nil || len(passes) != 1 {
		t.Fatalf("legacy summary: passes=%d err=%v", len(passes), err)
	}
	f.exec(`UPDATE bot_history_message_compacts SET summary=$2 WHERE session_id=$1`, session, legacySummary)
	f.persistAt(session, "old-", 60, 2800, time.Now().Add(-30*24*time.Hour))
	origin := f.upgrade(session, budget)
	if !origin.At.After(f.created(session, "old-0")) || !origin.At.Before(f.created(session, "old-59")) {
		t.Fatalf("origin %v does not fall inside the backlog", origin.At)
	}
	t.Logf("history_budget=%d origin=%v omitted=%d", budget, origin.At, origin.OmittedCount)

	assertFromOrigin := func(t *testing.T, request string, origin messagepkg.HistoryOrigin, newest string) {
		t.Helper()
		if notice := timeline.HistoryNotice(origin); strings.Count(request, notice) != 1 {
			t.Fatalf("the request carries the history notice %d times, want once: %q", strings.Count(request, notice), notice)
		}
		if strings.Count(request, legacySummary) != 1 {
			t.Fatalf("the request carries the legacy summary %d times, want once", strings.Count(request, legacySummary))
		}
		if strings.Contains(request, "old-0 x") || !strings.Contains(request, newest+" x") {
			t.Fatalf("the request reads history before the origin, or not its newest history")
		}
	}

	request := f.answered(session, "upgraded message")
	assertFromOrigin(t, request, origin, "old-59")

	t.Run("fork of the newest turn", func(t *testing.T) {
		fork := f.fork(session, "BACKLOG_OK")
		if got := f.origin(fork); got != origin {
			t.Fatalf("fork origin = %+v, want the source's %+v", got, origin)
		}
		assertFromOrigin(t, f.answered(fork, "newest fork message"), origin, "old-59")
	})
	t.Run("fork before the origin", func(t *testing.T) {
		if !f.created(session, "old-29").Before(origin.At) {
			t.Fatal("the fork point is not before the origin")
		}
		fork := f.fork(session, "old-29 ")
		forkOrigin := f.origin(fork)
		if !forkOrigin.Omits() || !forkOrigin.At.Before(f.created(session, "old-29")) {
			t.Fatalf("fork origin = %+v, want one placed before the fork point", forkOrigin)
		}
		assertFromOrigin(t, f.answered(fork, "earlier fork message"), forkOrigin, "old-29")
	})
	t.Run("overflow folds the legacy summary", func(t *testing.T) {
		// Two more rows take the history just past the budget.
		f.persist(session, "new-", 2, 2800)
		var summarized []string
		f.onSummary = func(body []byte) { summarized = append(summarized, string(body)) }
		defer func() { f.onSummary = nil }()
		calls := f.modelCalls.Load()
		if code := f.chat(session, "overflow message"); code != "" || f.modelCalls.Load() != calls+1 || len(summarized) != 1 {
			t.Fatalf("code=%q provider_calls=%d summarizer_calls=%d, want one summary and one answer", code, f.modelCalls.Load()-calls, len(summarized))
		}
		if !strings.Contains(summarized[0], legacySummary) {
			t.Fatal("the summarizer did not read the legacy summary")
		}
		if active := f.activeSummaryTexts(session); len(active) != 1 || active[0] == legacySummary {
			t.Fatalf("active summaries = %q, want the one the pass wrote", active)
		}
		request, _ := f.lastRequest.Load().(string)
		if strings.Contains(request, legacySummary) || !strings.Contains(request, "Earlier backlog summarized.") || strings.Count(request, timeline.HistoryNotice(origin)) != 1 {
			t.Fatal("the answer does not read the new summary beside the history notice")
		}
		if got := f.origin(session); got != origin {
			t.Fatalf("compaction moved the origin to %+v", got)
		}
	})
}

// A session created after the upgrade has no origin, and grows and compacts
// as before.
func TestPostgresNewChatSessionCompactsWithoutAnOrigin(t *testing.T) {
	f := newOriginFixture(t)
	session := f.newChatSession()
	for round := range 3 {
		prefix := fmt.Sprintf("round%d-", round)
		f.persist(session, prefix, 12, 2800)
		summaries := f.summaries.Load()
		if code := f.chat(session, prefix+"current message"); code != "" || f.summaries.Load() == summaries {
			t.Fatalf("%s: code=%q summarizer_calls=%d, want a summary before the answer", prefix, code, f.summaries.Load()-summaries)
		}
		if request, _ := f.lastRequest.Load().(string); strings.Contains(request, "[History notice]") {
			t.Fatalf("%s: a new session's request carries a history notice", prefix)
		}
	}
	if origin := f.origin(session); !origin.At.IsZero() {
		t.Fatalf("a new session got an origin: %+v", origin)
	}
	if active := f.activeSummaryTexts(session); len(active) != 1 {
		t.Fatalf("active summaries = %q, want one", active)
	}
}

// A fork of a long session created after the upgrade carries the summaries
// that end before the fork point. The rows of a summary that reaches past it
// read raw in the fork; when they do not fit, the fork gets an origin. Either
// way its first request needs no summary.
func TestPostgresForkOfALongNewChatSessionNeedsNoSummary(t *testing.T) {
	f := newOriginFixture(t)
	session := f.sessionID
	f.persist(session, "row-", 40, 1000)
	if passes, err := f.fold(f.ctx, compaction.TriggerConfig{CandidatesBefore: f.created(session, "row-10")}); err != nil || len(passes) != 1 {
		t.Fatalf("first summary: passes=%d err=%v", len(passes), err)
	}
	f.exec(`UPDATE bot_history_message_compacts SET summary='Summary of rows 0-9.' WHERE session_id=$1`, session)
	if passes, err := f.fold(f.ctx, compaction.TriggerConfig{CandidatesBefore: f.created(session, "row-30")}); err != nil || len(passes) != 1 {
		t.Fatalf("second summary: passes=%d err=%v", len(passes), err)
	}
	f.exec(`UPDATE bot_history_message_compacts SET summary='Summary of rows 0-29.' WHERE session_id=$1 AND superseded_by IS NULL`, session)
	if active := f.activeSummaryTexts(session); len(active) != 1 || active[0] != "Summary of rows 0-29." {
		t.Fatalf("active summaries = %q, want the second, which absorbed the first", active)
	}
	f.answered(session, "source message")

	t.Run("fork of the newest turn", func(t *testing.T) {
		fork := f.fork(session, "BACKLOG_OK")
		if got := f.origin(fork); !got.At.IsZero() {
			t.Fatalf("fork origin = %+v, want none", got)
		}
		request := f.answered(fork, "newest fork message")
		if strings.Contains(request, "[History notice]") || !strings.Contains(request, "Summary of rows 0-29.") || !strings.Contains(request, "row-39 x") || strings.Contains(request, "row-29 x") {
			t.Fatal("the fork does not read like its source")
		}
	})
	t.Run("fork inside a summary", func(t *testing.T) {
		// The second summary reaches row 29, so rows 10-25 read raw in the
		// fork, more than its budget holds.
		fork := f.fork(session, "row-25 ")
		origin := f.origin(fork)
		if !origin.Omits() || !origin.At.After(f.created(session, "row-10")) || origin.At.After(f.created(session, "row-24")) {
			t.Fatalf("fork origin = %+v, want one inside rows 11-24", origin)
		}
		request := f.answered(fork, "inner fork message")
		if strings.Count(request, timeline.HistoryNotice(origin)) != 1 || strings.Count(request, "Summary of rows 0-9.") != 1 || strings.Contains(request, "Summary of rows 0-29.") {
			t.Fatal("the fork does not read the notice and the summary that ends before it")
		}
		if !strings.Contains(request, "row-25 x") || strings.Contains(request, "row-26 x") || strings.Contains(request, "row-10 x") {
			t.Fatal("the fork does not read its own history from its origin on")
		}
	})
}

// A Discuss session from before the upgrade answers its first trigger after
// it from its origin on: the inputs and replies before the origin stay out,
// and the notice says what they were.
func TestPostgresUpgradedDiscussSessionAnswersFromItsOrigin(t *testing.T) {
	f := newBacklogFixture(t)
	for i := range 10 {
		id := fmt.Sprintf("old-%d", i)
		f.appendMessageAt(id, id+" "+strings.Repeat("x", 600), time.Now())
		f.answer(id, id+" x")
		time.Sleep(2 * time.Millisecond)
	}
	// Placed as the upgrade does, for a budget that holds the newest rounds.
	if _, err := f.pool.Exec(f.ctx, `SELECT public.memoh_place_history_origin($1, 600)`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	origin, err := f.messages.HistoryOriginBySession(f.ctx, f.sessionID)
	if err != nil || !origin.Omits() {
		t.Fatalf("origin = %+v err=%v, want one inside the history", origin, err)
	}
	t.Logf("origin=%v omitted=%d", origin.At, origin.OmittedCount)

	summaries := f.summaries.Load()
	f.appendMessageAt("new-0", "new-0 message", time.Now())
	request := f.answer("after the upgrade", "new-0 message")
	if f.summaries.Load() != summaries {
		t.Fatalf("the first trigger after the upgrade needed %d summaries", f.summaries.Load()-summaries)
	}
	if notice := timeline.HistoryNotice(origin); strings.Count(request, notice) != 1 {
		t.Fatalf("the request carries the history notice %d times, want once: %q", strings.Count(request, notice), notice)
	}
	if strings.Contains(request, "old-0 x") || !strings.Contains(request, "old-9 x") {
		t.Fatal("the request reads input from before the origin, or not the newest")
	}
}

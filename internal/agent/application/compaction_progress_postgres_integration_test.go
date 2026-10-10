package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// countingSummarizer is a scripted OpenAI-compatible summarizer.
type countingSummarizer struct {
	summary    string
	refuse     string // a request containing it is refused with content_filter
	cutOffOver int    // a request longer than this many bytes is cut off at the output limit
	calls      int
}

func (s *countingSummarizer) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	finish := "stop"
	if req.Body != nil {
		prompt, _ := io.ReadAll(req.Body)
		switch {
		case s.refuse != "" && strings.Contains(string(prompt), s.refuse):
			finish = "content_filter"
		case s.cutOffOver > 0 && len(prompt) > s.cutOffOver:
			finish = "length"
		}
	}
	content, _ := json.Marshal(s.summary)
	body := `{"id":"stub","object":"chat.completion","created":0,"model":"stub",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + string(content) + `},"finish_reason":"` + finish + `"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// summaryTokens returns a summary whose replay estimate is tokens.
func summaryTokens(tokens int) string { return strings.Repeat("s", 4*tokens-21) }

type progressFixture struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	queries   *dbsqlc.Queries
	messages  *messagepkg.DBService
	botID     string
	sessionID string
}

func (f progressFixture) persist(role string, content any) messagepkg.Message {
	f.t.Helper()
	raw, err := json.Marshal(map[string]any{"role": role, "content": content})
	if err != nil {
		f.t.Fatal(err)
	}
	msg, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{BotID: f.botID, SessionID: f.sessionID, Role: role, Content: raw})
	if err != nil {
		f.t.Fatalf("persist %s: %v", role, err)
	}
	return msg
}

func (f progressFixture) text(role, text string) messagepkg.Message { return f.persist(role, text) }

func (f progressFixture) reasoning() messagepkg.Message {
	return f.persist("assistant", []map[string]any{{"type": "reasoning", "text": "REASONING_BARRIER"}})
}

func (f progressFixture) askUser(n int) []messagepkg.Message {
	id := fmt.Sprintf("ask-%d", n)
	return []messagepkg.Message{
		f.persist("assistant", []map[string]any{{"type": "tool-call", "toolCallId": id, "toolName": "ask_user", "input": map[string]any{"question": "继续吗？"}}}),
		f.persist("tool", []map[string]any{{"type": "tool-result", "toolCallId": id, "toolName": "ask_user", "output": map[string]any{"type": "text", "value": "继续"}}}),
	}
}

func (f progressFixture) exec(n int) []messagepkg.Message {
	id := fmt.Sprintf("exec-%d", n)
	return []messagepkg.Message{
		f.persist("assistant", []map[string]any{{"type": "tool-call", "toolCallId": id, "toolName": "exec", "input": map[string]any{"command": fmt.Sprintf("make step-%d", n)}}}),
		f.persist("tool", []map[string]any{{"type": "tool-result", "toolCallId": id, "toolName": "exec", "output": map[string]any{"type": "text", "value": strings.Repeat(fmt.Sprintf("LATER step %d ok; ", n), 60)}}}),
	}
}

func (f progressFixture) uuid(id string) pgtype.UUID {
	f.t.Helper()
	parsed, err := dbpkg.ParseUUID(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return parsed
}

// claims maps every visible row of the session to its compact_id, in order.
func (f progressFixture) claims() ([]string, map[string]string) {
	f.t.Helper()
	rows, err := f.pool.Query(f.ctx, `
		SELECT id::text, COALESCE(compact_id::text, '')
		FROM bot_visible_history_messages
		WHERE session_id = $1
		ORDER BY turn_position, turn_message_seq, created_at, id`, f.sessionID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var order []string
	claim := map[string]string{}
	for rows.Next() {
		var id, compactID string
		if err := rows.Scan(&id, &compactID); err != nil {
			f.t.Fatal(err)
		}
		order = append(order, id)
		claim[id] = compactID
	}
	return order, claim
}

type compactRecord struct {
	status, reason           string
	count, covered, attempts int
}

func (f progressFixture) compact(id string) compactRecord {
	f.t.Helper()
	var record compactRecord
	if err := f.pool.QueryRow(f.ctx, `
		SELECT status, failure_reason, message_count, jsonb_array_length(coverage), failure_attempts
		FROM bot_history_message_compacts WHERE id = $1`, id).Scan(&record.status, &record.reason, &record.count, &record.covered, &record.attempts); err != nil {
		f.t.Fatalf("load compact %s: %v", id, err)
	}
	return record
}

func (f progressFixture) config(model *countingSummarizer) compaction.TriggerConfig {
	return compaction.TriggerConfig{
		BotID: f.botID, SessionID: f.sessionID,
		ModelID: "stub-model", ClientType: "openai-completions", APIKey: "test", BaseURL: "http://stub.invalid",
		HTTPClient:   &http.Client{Transport: model},
		TargetTokens: 50,
		Manual:       true,
	}
}

func (f progressFixture) run(svc *compaction.Service, model *countingSummarizer) (compaction.Result, error) {
	return svc.RunCompactionSync(f.ctx, f.config(model))
}

// replay loads the session history the way a turn does and substitutes
// committed summaries, returning one label per replayed message.
func (f progressFixture) replay(labels map[string]string) []string {
	f.t.Helper()
	svc := &Service{logger: slog.New(slog.DiscardHandler), queries: postgresstore.NewQueries(f.queries), messageService: f.messages}
	records, err := svc.loadHistoryRecords(f.ctx, historyfrag.ScopeFallback{}, f.sessionID, 60*24, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	records, err = svc.replaceCompactedMessages(f.ctx, f.sessionID, contextfrag.Scope{BotID: f.botID, SessionID: f.sessionID}, records, compactionArtifactBoundary{})
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]string, 0, len(records))
	for _, record := range records {
		if record.SourceKind == historyfrag.SourceCompactionLog {
			out = append(out, "summary:"+record.Ref.ID)
			continue
		}
		out = append(out, labels[record.DBMessageID])
	}
	return out
}

func TestPostgresCompactionAdvancesPastHeldBackHistory(t *testing.T) {
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	f := progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, postgresstore.NewQueries(queries)), botID: botID, sessionID: sessionID}
	store := postgresstore.NewQueries(queries)
	labels := map[string]string{}
	label := func(name string, msgs ...messagepkg.Message) []messagepkg.Message {
		for i, msg := range msgs {
			labels[msg.ID] = fmt.Sprintf("%s#%d", name, i)
		}
		return msgs
	}

	// The production order: protected ask_user exchanges and reasoning-only
	// rows, a 14-character user message, a reasoning-only row, then the later
	// history and the current turn.
	for n := 1; n <= 14; n++ {
		label("ask", f.askUser(n)...)
		if n == 5 || n == 10 {
			label("reasoning", f.reasoning())
		}
	}
	tiny := label("tiny", f.text("user", "请帮我继续处理剩下的这些任务"))[0]
	label("reasoning", f.reasoning())
	var later []messagepkg.Message
	for n := 1; n <= 3; n++ {
		later = append(later, label("later", f.text("user", fmt.Sprintf("LATER request %d: migrate the build scripts", n)))...)
		later = append(later, label("later", f.exec(n)...)...)
		later = append(later, label("later", f.text("assistant", strings.Repeat(fmt.Sprintf("LATER answer %d. ", n), 30)))...)
	}
	current := label("current", f.text("user", "current question"))[0]

	// The failure production already holds: the tiny row claimed by an
	// ineffective attempt recorded before failure reasons existed.
	epoch := int64(0)
	if err := pool.QueryRow(ctx, `SELECT compaction_epoch FROM bot_sessions WHERE id = $1`, sessionID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	legacy, err := queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{BotID: f.uuid(botID), SessionID: f.uuid(sessionID), ExpectedEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{CompactID: legacy.ID, MessageIds: []pgtype.UUID{f.uuid(tiny.ID)}, ExpectedCompactIds: []pgtype.UUID{{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{ID: legacy.ID, Status: "error", ErrorMessage: "compaction: summary does not reduce replay tokens: summary_tokens=254 raw_tokens=27", Coverage: []byte("[]")}); err != nil {
		t.Fatal(err)
	}

	model := &countingSummarizer{summary: summaryTokens(254)}
	started := time.Now()
	res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model)
	if err != nil || res.Status != compaction.StatusOK || model.calls != 1 {
		t.Fatalf("first pass = %+v, %v after %d calls; want the later span committed by one call", res, err, model.calls)
	}
	order, claim := f.claims()
	committed := claim[later[0].ID]
	for _, msg := range later {
		if claim[msg.ID] != committed {
			t.Fatalf("later row %s claimed by %q, want one summary %s for the whole span", labels[msg.ID], claim[msg.ID], committed)
		}
	}
	if claim[tiny.ID] != formatPGUUID(legacy.ID) || claim[current.ID] != "" {
		t.Fatalf("tiny claim = %q, current claim = %q; want the legacy claim kept and the current turn raw", claim[tiny.ID], claim[current.ID])
	}
	for _, id := range order {
		if id == later[0].ID {
			break
		}
		if id != tiny.ID && claim[id] != "" {
			t.Fatalf("held-back row %s was claimed", labels[id])
		}
	}
	if got := f.compact(committed); got.status != "ok" || got.count != len(later) || got.covered != len(later) || got.reason != "" {
		t.Fatalf("committed summary = %+v, want ok covering %d rows", got, len(later))
	}
	// The held-back prefix is recorded as scanned through the row in front
	// of the committed span.
	var scanAfter string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(compaction_scan_after::text, '') FROM bot_sessions WHERE id = $1`, sessionID).Scan(&scanAfter); err != nil {
		t.Fatal(err)
	}
	for i, id := range order {
		if id == later[0].ID && scanAfter != order[i-1] {
			t.Fatalf("scan position = %s (%s), want %s", scanAfter, labels[scanAfter], labels[order[i-1]])
		}
	}

	// SQL view of the window: the current turn now follows a summary.
	window, err := queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 1 << 20, IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) == 0 || formatPGUUID(window[0].ID) != current.ID {
		t.Fatal("a read without a cursor must start after the recorded scan position, at the current turn")
	}
	for _, row := range window {
		id := formatPGUUID(row.ID)
		if wantGap := id == current.ID; row.GapBefore != wantGap {
			t.Fatalf("gap_before(%s) = %v, want %v", labels[id], row.GapBefore, wantGap)
		}
		if row.IneffectiveClaim {
			t.Fatalf("row %s reported an ineffective claim; the legacy attempt has no reason", labels[id])
		}
	}
	replayed := f.replay(labels)
	want := append([]string{}, replayed[:len(replayed)-2]...)
	if strings.Join(replayed[len(replayed)-2:], ",") != "summary:"+committed+",current#0" {
		t.Fatalf("replay tail = %v, want the summary in place of the later span then the current turn", replayed[len(replayed)-2:])
	}
	for _, name := range want {
		if strings.HasPrefix(name, "later") || strings.HasPrefix(name, "summary") {
			t.Fatalf("replay before the summary carries %s: %v", name, replayed)
		}
	}

	// The next turn: a span that cannot shrink, a reasoning barrier, and a
	// span that can. Same process: the failure moves the next pass on.
	nextA := label("nextA", f.text("user", strings.Repeat("NEXT-A question. ", 40)), f.text("assistant", strings.Repeat("NEXT-A answer. ", 40)))
	label("reasoning", f.reasoning())
	nextB := label("nextB", f.text("user", strings.Repeat("NEXT-B question. ", 120)), f.text("assistant", strings.Repeat("NEXT-B answer. ", 120)))
	label("current2", f.text("user", "current question two"))

	svc := compaction.NewService(slog.New(slog.DiscardHandler), store)
	model.summary = summaryTokens(600)
	if res, err := f.run(svc, model); err != nil || res.Status != compaction.StatusNoop || res.Reason != compaction.ReasonNoBeneficialSpan {
		t.Fatalf("manual pass over NEXT-A = %+v, %v; want its summary rejected and reported as history that does not shrink", res, err)
	}
	_, claim = f.claims()
	failed := claim[nextA[0].ID]
	if got := f.compact(failed); got.status != "error" || got.reason != "ineffective_summary" {
		t.Fatalf("NEXT-A attempt = %+v, want an error recorded as ineffective_summary", got)
	}
	model.summary = summaryTokens(254)
	auto := f.config(model)
	auto.Manual, auto.HardPressure = false, true
	if res, err := svc.RunCompactionSync(ctx, auto); err != nil || res.Status != compaction.StatusOK {
		t.Fatalf("same-process automatic next pass = %+v, %v; want NEXT-B committed without a cooldown", res, err)
	}
	_, claim = f.claims()
	if claim[nextB[0].ID] == "" || claim[nextB[0].ID] != claim[nextB[1].ID] || claim[nextA[0].ID] != failed {
		t.Fatal("same-process next pass did not commit NEXT-B while leaving NEXT-A with its failed claim")
	}

	// Restart: a fresh service reads the failure back from the database and
	// does not resend NEXT-A.
	callsBefore := model.calls
	res, err = f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model)
	if err != nil || res.Status != compaction.StatusNoop || res.Reason != compaction.ReasonNoBeneficialSpan || model.calls != callsBefore {
		t.Fatalf("pass after restart = %+v, %v with %d calls; want a noop without a call", res, err, model.calls-callsBefore)
	}

	// A new epoch voids every claim of the old one, the failure included.
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET compaction_epoch = compaction_epoch + 1 WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	window, err = queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 1 << 20, IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range window {
		if row.IneffectiveClaim {
			t.Fatalf("row %s still reports an ineffective claim from the old epoch", labels[formatPGUUID(row.ID)])
		}
	}
	if first := formatPGUUID(window[0].ID); first != order[0] {
		t.Fatalf("new epoch window starts at %s, want the start of the session: the old scan position is void", labels[first])
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("integration run took %v", elapsed)
	}
}

func formatPGUUID(id pgtype.UUID) string { return pgUUIDString(id) }

func TestPostgresCompactionReachesSpanBeyondReadWindow(t *testing.T) {
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	f := progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, postgresstore.NewQueries(queries)), botID: botID, sessionID: sessionID}

	// More protected history than one 512 KiB read window holds.
	var filler []messagepkg.Message
	for n := 0; n < 140; n++ {
		id := fmt.Sprintf("fill-%d", n)
		filler = append(filler,
			f.persist("assistant", []map[string]any{{"type": "tool-call", "toolCallId": id, "toolName": "ask_user", "input": map[string]any{"question": strings.Repeat("q", 4096)}}}),
			f.persist("tool", []map[string]any{{"type": "tool-result", "toolCallId": id, "toolName": "ask_user", "output": "ok"}}),
		)
	}
	later := []messagepkg.Message{f.text("user", strings.Repeat("LATER question. ", 120)), f.text("assistant", strings.Repeat("LATER answer. ", 120))}
	f.text("user", "current question")

	// The cursor continues strictly after its anchor and counts what is left.
	anchor := filler[len(filler)-1]
	window, err := queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 1 << 20, AfterMessageID: f.uuid(anchor.ID), IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 3 || formatPGUUID(window[0].ID) != later[0].ID || window[0].CandidateCount != 3 {
		t.Fatalf("window after the last filler row = %d rows starting %s with count %d, want the 3 rows after it", len(window), formatPGUUID(window[0].ID), window[0].CandidateCount)
	}

	model := &countingSummarizer{summary: summaryTokens(254)}
	res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), postgresstore.NewQueries(queries)), model)
	if err != nil || res.Status != compaction.StatusOK || model.calls != 1 || res.MessageCount != 2 {
		t.Fatalf("result = %+v, %v after %d calls; want the span past the first window committed", res, err, model.calls)
	}
	_, claim := f.claims()
	if claim[later[0].ID] == "" || claim[later[0].ID] != claim[later[1].ID] || claim[filler[0].ID] != "" {
		t.Fatal("claims do not cover exactly the later span")
	}
}

func TestPostgresCompactionScanStopsAtFreshClaim(t *testing.T) {
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	f := progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, postgresstore.NewQueries(queries)), botID: botID, sessionID: sessionID}

	before := f.askUser(1)
	claimed := []messagepkg.Message{f.text("user", strings.Repeat("CLAIMED question. ", 80)), f.text("assistant", strings.Repeat("CLAIMED answer. ", 80))}
	after := f.askUser(2)
	f.text("user", "current question")

	// An unfinished attempt holds the middle rows.
	attempt, err := queries.CreateCompactionLog(ctx, dbsqlc.CreateCompactionLogParams{BotID: f.uuid(botID), SessionID: f.uuid(sessionID)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: attempt.ID, MessageIds: []pgtype.UUID{f.uuid(claimed[0].ID), f.uuid(claimed[1].ID)}, ExpectedCompactIds: []pgtype.UUID{{}, {}},
	}); err != nil {
		t.Fatal(err)
	}
	window, err := queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 1 << 20, IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range window {
		if want := formatPGUUID(row.ID) == after[0].ID; row.PendingBefore != want || row.GapBefore != want {
			t.Fatalf("row %s: gap=%v pending=%v, want both %v", formatPGUUID(row.ID), row.GapBefore, row.PendingBefore, want)
		}
	}

	model := &countingSummarizer{summary: summaryTokens(254)}
	if res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), postgresstore.NewQueries(queries)), model); err != nil || res.Status != compaction.StatusNoop || model.calls != 0 {
		t.Fatalf("pass during the claim = %+v, %v after %d calls", res, err, model.calls)
	}
	var scanAfter string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(compaction_scan_after::text, '') FROM bot_sessions WHERE id = $1`, sessionID).Scan(&scanAfter); err != nil {
		t.Fatal(err)
	}
	if scanAfter != before[1].ID {
		t.Fatalf("scan position = %q, want the row before the fresh claim", scanAfter)
	}

	// The claim lapses; its rows are reached again.
	if _, err := pool.Exec(ctx, `UPDATE bot_history_message_compacts SET started_at = now() - INTERVAL '16 minutes' WHERE id = $1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), postgresstore.NewQueries(queries)), model)
	if err != nil || res.Status != compaction.StatusOK || res.MessageCount != 2 {
		t.Fatalf("pass after the claim lapsed = %+v, %v", res, err)
	}
	_, claim := f.claims()
	if claim[claimed[0].ID] == formatPGUUID(attempt.ID) || claim[claimed[0].ID] != claim[claimed[1].ID] {
		t.Fatal("lapsed rows were not reclaimed by the new summary")
	}
}

func TestPostgresCompactionPassesOversizedRowAndRetriesProvedRowsWithNewOnes(t *testing.T) {
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	f := progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, postgresstore.NewQueries(queries)), botID: botID, sessionID: sessionID}
	store := postgresstore.NewQueries(queries)

	huge := f.text("assistant", strings.Repeat("H", 8_500_000))
	old := []messagepkg.Message{f.text("user", strings.Repeat("OLD question. ", 50)), f.text("assistant", strings.Repeat("OLD answer. ", 50))}
	f.text("user", "current question")

	window, err := queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 512 << 10, IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 1 || formatPGUUID(window[0].ID) != huge.ID || !window[0].Oversized || len(window[0].Content) != 0 {
		t.Fatalf("leading oversized row came back as %d rows, oversized=%v, %d content bytes; want it alone without payload", len(window), len(window) > 0 && window[0].Oversized, len(window[0].Content))
	}
	all, err := queries.ListUncompactedMessagesBySessionWithinBytes(ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(sessionID), MaxBytes: 1 << 20, IneffectiveFailureReason: "ineffective_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range all {
		if row.LatestUser != (row.Role == "user" && formatPGUUID(row.ID) != old[0].ID) {
			t.Fatalf("latest_user(%s, %s) = %v", formatPGUUID(row.ID), row.Role, row.LatestUser)
		}
	}

	// The span behind the oversized row fails as ineffective.
	model := &countingSummarizer{summary: summaryTokens(600)}
	auto := f.config(model)
	auto.Manual = false
	if _, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, auto); !errors.Is(err, compaction.ErrIneffectiveSummary) {
		t.Fatalf("first pass = %v, want the span past the oversized row tried and rejected", err)
	}
	_, claim := f.claims()
	failed := claim[old[0].ID]
	if failed == "" || claim[huge.ID] != "" {
		t.Fatal("the span past the oversized row was not the one tried")
	}

	// The turn continues right after the failed rows: they are resent with
	// the new ones and the claim moves off the failed attempt.
	current := f.text("assistant", strings.Repeat("NEW answer continues the work. ", 80))
	f.text("user", "next question")
	model.summary = summaryTokens(254)
	res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model)
	if err != nil || res.Status != compaction.StatusOK {
		t.Fatalf("second pass = %+v, %v", res, err)
	}
	_, claim = f.claims()
	committed := claim[old[0].ID]
	if committed == failed || committed != claim[old[1].ID] || claim[current.ID] == "" {
		t.Fatalf("claims after retry: old=%q/%q new=%q (failed %q); want the failed rows and the new ones in one summary", committed, claim[old[1].ID], claim[current.ID], failed)
	}
	if got := f.compact(committed); got.status != "ok" {
		t.Fatalf("retry summary = %+v", got)
	}
}

func newProgressFixture(t *testing.T) (progressFixture, *postgresstore.Queries) {
	t.Helper()
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	store := postgresstore.NewQueries(queries)
	return progressFixture{t: t, ctx: ctx, pool: pool, queries: queries, messages: messagepkg.NewService(nil, store), botID: botID, sessionID: sessionID}, store
}

func (f progressFixture) scanAfter() string {
	f.t.Helper()
	var after string
	if err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(compaction_scan_after::text, '') FROM bot_sessions WHERE id = $1`, f.sessionID).Scan(&after); err != nil {
		f.t.Fatal(err)
	}
	return after
}

// elapse moves the session's recorded attempt and scan times back by d, as if
// that much time had passed.
func (f progressFixture) elapse(d string) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_message_compacts SET completed_at = completed_at - $2::interval WHERE session_id = $1`, f.sessionID, d); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_sessions SET compaction_scan_at = compaction_scan_at - $2::interval WHERE id = $1`, f.sessionID, d); err != nil {
		f.t.Fatal(err)
	}
}

func (f progressFixture) claimStatus(id string) string {
	f.t.Helper()
	var status string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT COALESCE(c.status, '') FROM bot_history_messages m
		LEFT JOIN bot_history_message_compacts c ON c.id = m.compact_id
		WHERE m.id = $1`, id).Scan(&status); err != nil {
		f.t.Fatal(err)
	}
	return status
}

func (f progressFixture) filler(n int) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("fill-%d", i)
		f.persist("assistant", []map[string]any{{"type": "tool-call", "toolCallId": id, "toolName": "ask_user", "input": map[string]any{"question": strings.Repeat("q", 4096)}}})
		f.persist("tool", []map[string]any{{"type": "tool-result", "toolCallId": id, "toolName": "ask_user", "output": "ok"}})
	}
}

func TestPostgresCompactionReadsPastTheCurrentTasksWindow(t *testing.T) {
	f, store := newProgressFixture(t)
	// The task is followed by more protected steps than one window holds: the
	// windows after the task's have no candidate user row at all.
	task := f.text("user", strings.Repeat("TASK ", 320))
	f.filler(140)
	model := &countingSummarizer{summary: summaryTokens(100)}
	cfg := f.config(model)
	cfg.Manual = false
	res, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg)
	if err != nil || res.Status != compaction.StatusNoop || f.scanAfter() != "" {
		t.Fatalf("pass during the task = %+v, %v, scan position %q; want a clean noop that settles nothing past the task", res, err, f.scanAfter())
	}
	f.text("user", "NEXT-TASK")
	res, err = f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model)
	if err != nil || res.Status != compaction.StatusOK || f.claimStatus(task.ID) != "ok" {
		t.Fatalf("pass after the next task = %+v, %v; want the old task claimed", res, err)
	}
}

func TestPostgresCompactionScanStopsAtFreshClaimBehindASummary(t *testing.T) {
	f, store := newProgressFixture(t)
	f.askUser(1)
	p := []messagepkg.Message{f.text("user", strings.Repeat("P question. ", 80)), f.text("assistant", strings.Repeat("P answer. ", 80))}
	f.text("user", strings.Repeat("S question. ", 80))
	f.text("assistant", strings.Repeat("S answer. ", 80))
	f.askUser(2)
	f.text("user", strings.Repeat("C question. ", 80))
	f.text("assistant", strings.Repeat("C answer. ", 80))
	f.text("user", "current question")

	// A process died while summarizing P; later passes commit what follows.
	var epoch int64
	if err := f.pool.QueryRow(f.ctx, `SELECT compaction_epoch FROM bot_sessions WHERE id = $1`, f.sessionID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.queries.CreateCompactionLog(f.ctx, dbsqlc.CreateCompactionLogParams{BotID: f.uuid(f.botID), SessionID: f.uuid(f.sessionID), ExpectedEpoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.queries.MarkMessagesCompacted(f.ctx, dbsqlc.MarkMessagesCompactedParams{CompactID: attempt.ID, MessageIds: []pgtype.UUID{f.uuid(p[0].ID), f.uuid(p[1].ID)}, ExpectedCompactIds: []pgtype.UUID{{}, {}}}); err != nil {
		t.Fatal(err)
	}
	model := &countingSummarizer{summary: summaryTokens(100)}
	for pass := 0; pass < 3; pass++ {
		if _, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_message_compacts SET started_at = now() - INTERVAL '16 minutes' WHERE id = $1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil || res.Status != compaction.StatusOK || f.claimStatus(p[0].ID) != "ok" {
		t.Fatalf("pass after the claim lapsed = %+v, %v; P status %q, want it summarized", res, err, f.claimStatus(p[0].ID))
	}
}

// epochBumpingQueries raises the session's compaction epoch before the
// second window of a pass is read.
type epochBumpingQueries struct {
	dbstore.Queries
	reads int
	bump  func()
}

func (q *epochBumpingQueries) ListUncompactedMessagesBySessionWithinBytes(ctx context.Context, arg dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]dbsqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	if q.reads++; q.reads == 2 {
		q.bump()
	}
	return q.Queries.ListUncompactedMessagesBySessionWithinBytes(ctx, arg)
}

func TestPostgresCompactionEpochChangeMidPassRecordsNoScanPosition(t *testing.T) {
	f, store := newProgressFixture(t)
	early := f.text("user", strings.Repeat("P0 question. ", 80))
	f.text("assistant", strings.Repeat("P0 answer. ", 80))
	f.text("user", "q0")
	model := &countingSummarizer{summary: summaryTokens(100)}
	if res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil || res.Status != compaction.StatusOK {
		t.Fatalf("setup pass = %+v, %v", res, err)
	}
	f.filler(140)
	f.text("user", strings.Repeat("LATER question. ", 120))
	f.text("assistant", strings.Repeat("LATER answer. ", 120))
	f.text("user", "current question")

	racing := &epochBumpingQueries{Queries: store, bump: func() {
		if _, err := f.pool.Exec(f.ctx, `UPDATE bot_sessions SET compaction_epoch = compaction_epoch + 1 WHERE id = $1`, f.sessionID); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), racing), model); err != nil {
		t.Fatal(err)
	}
	if after := f.scanAfter(); after != "" {
		var scanEpoch, epoch int64
		_ = f.pool.QueryRow(f.ctx, `SELECT compaction_scan_epoch, compaction_epoch FROM bot_sessions WHERE id = $1`, f.sessionID).Scan(&scanEpoch, &epoch)
		if scanEpoch == epoch {
			t.Fatal("a pass that saw the epoch change recorded a scan position for the new epoch")
		}
	}
	// The voided summary's rows are candidates again and still reached.
	live := func() bool {
		var ok bool
		if err := f.pool.QueryRow(f.ctx, `
			SELECT EXISTS (SELECT 1 FROM bot_history_messages m JOIN bot_history_message_compacts c ON c.id = m.compact_id
			JOIN bot_sessions s ON s.id = c.session_id WHERE m.id = $1 AND c.status = 'ok' AND c.compaction_epoch = s.compaction_epoch)`, early.ID).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for pass := 0; pass < 3 && !live(); pass++ {
		if _, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil {
			t.Fatal(err)
		}
	}
	if !live() {
		t.Fatal("history voided by the new epoch was not summarized again")
	}
}

func TestPostgresCompactionKeepsRowsInFrontOfTheCurrentTaskUnsettled(t *testing.T) {
	f, store := newProgressFixture(t)
	s0 := f.text("user", strings.Repeat("S0 ask. ", 50))
	f.text("assistant", strings.Repeat("S0 ok. ", 50))
	task := f.text("user", strings.Repeat("TASK detail. ", 50))
	// Two ordinary steps make the task's joint; the large ones after it fill
	// the read windows.
	f.exec(1)
	f.exec(2)
	for n := 1; n <= 2; n++ {
		id := fmt.Sprintf("big-%d", n)
		f.persist("assistant", []map[string]any{{"type": "tool-call", "toolCallId": id, "toolName": "exec", "input": map[string]any{"command": "cat big"}}})
		f.persist("tool", []map[string]any{{"type": "tool-result", "toolCallId": id, "toolName": "exec", "output": map[string]any{"type": "text", "value": strings.Repeat("line ok; ", 33_000)}}})
	}
	model := &countingSummarizer{summary: summaryTokens(100)}
	if _, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil {
		t.Fatal(err)
	}
	f.text("user", "next question")
	for pass := 0; pass < 3 && f.claimStatus(s0.ID) != "ok"; pass++ {
		if _, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model); err != nil {
			t.Fatal(err)
		}
	}
	if f.claimStatus(s0.ID) != "ok" || f.claimStatus(task.ID) != "ok" {
		t.Fatalf("S0 %q, task %q: the rows in front of the then-current task were settled", f.claimStatus(s0.ID), f.claimStatus(task.ID))
	}
}

func TestPostgresCompactionRefusedSpanIsHeldThenRetried(t *testing.T) {
	f, store := newProgressFixture(t)
	labels := map[string]string{}
	label := func(name string, msgs ...messagepkg.Message) []messagepkg.Message {
		for i, msg := range msgs {
			labels[msg.ID] = fmt.Sprintf("%s#%d", name, i)
		}
		return msgs
	}
	refused := label("refused", f.text("user", strings.Repeat("REFUSED question. ", 80)), f.text("assistant", strings.Repeat("REFUSED answer. ", 80)))
	label("reasoning", f.reasoning())
	later := label("later", f.text("user", strings.Repeat("LATER question. ", 80)), f.text("assistant", strings.Repeat("LATER answer. ", 80)))
	label("current", f.text("user", "current question"))

	// The provider refuses the oldest span: the manual request says so, and
	// the attempt is recorded against both rows.
	model := &countingSummarizer{summary: summaryTokens(120), refuse: "REFUSED"}
	svc := compaction.NewService(slog.New(slog.DiscardHandler), store)
	if res, err := f.run(svc, model); err != nil || res.Status != compaction.StatusNoop || res.Reason != compaction.ReasonSummaryUnusable {
		t.Fatalf("first pass = %+v, %v; want the refusal reported as an unusable summary", res, err)
	}
	_, claim := f.claims()
	failed := claim[refused[0].ID]
	if got := f.compact(failed); got.status != "error" || got.reason != "unusable_summary" || claim[refused[1].ID] != failed {
		t.Fatalf("refused attempt = %+v, want an error recorded against both rows", got)
	}

	// While held, the next request moves past it, on a fresh service as after
	// a restart, and the scan position stays in front of the held rows.
	res, err := f.run(compaction.NewService(slog.New(slog.DiscardHandler), store), model)
	if err != nil || res.Status != compaction.StatusOK || res.MessageCount != len(later) {
		t.Fatalf("second pass = %+v, %v; want the later span committed", res, err)
	}
	_, claim = f.claims()
	committed := claim[later[0].ID]
	if got := f.compact(committed); got.status != "ok" || got.covered != len(later) || claim[later[1].ID] != committed {
		t.Fatalf("committed summary = %+v, want ok covering the later span", got)
	}
	if after := f.scanAfter(); labels[after] != "reasoning#0" {
		t.Fatalf("scan position = %s, want it past the held rows: they no longer pin it", labels[after])
	}
	auto := f.config(model)
	auto.Manual = false
	if res, err := svc.RunCompactionSync(f.ctx, auto); err != nil || res.Reason != compaction.ReasonNoBeneficialSpan || model.calls != 2 {
		t.Fatalf("third pass = %+v, %v after %d calls; want no call while the refused rows are held", res, err, model.calls)
	}
	if got := strings.Join(f.replay(labels), ","); got != "refused#0,refused#1,reasoning#0,summary:"+committed+",current#0" {
		t.Fatalf("replay = %s, want the refused rows raw in place before the summary", got)
	}

	// With nothing else to claim, a manual request retries them right away,
	// half at a time: refused again, the question is held four times as long,
	// and the answer, never tried on its own, keeps its first attempt.
	if res, err := f.run(svc, model); err != nil || res.Reason != compaction.ReasonSummaryUnusable || model.calls != 3 {
		t.Fatalf("manual pass = %+v, %v after %d calls; want the held rows retried", res, err, model.calls)
	}
	_, claim = f.claims()
	again := claim[refused[0].ID]
	if got := f.compact(again); again == failed || got.status != "error" || got.attempts != 2 || got.count != 1 || claim[refused[1].ID] != failed {
		t.Fatalf("manual retry = %+v, want a second unusable attempt on the first half only", got)
	}
	// Fresh services, as after a restart: only the recorded holds apply.
	restarted := func() *compaction.Service { return compaction.NewService(slog.New(slog.DiscardHandler), store) }
	model.refuse = ""
	f.elapse("16 minutes")
	if res, err := restarted().RunCompactionSync(f.ctx, auto); err != nil || res.Status != compaction.StatusOK || res.MessageCount != 1 {
		t.Fatalf("pass a quarter of an hour on = %+v, %v; want the once-refused answer retried", res, err)
	}
	if _, claim = f.claims(); claim[refused[0].ID] != again {
		t.Fatal("the twice-refused question was retried within the hour")
	}
	f.elapse("45 minutes")
	if res, err := restarted().RunCompactionSync(f.ctx, auto); err != nil || res.Status != compaction.StatusOK || res.MessageCount != 1 {
		t.Fatalf("pass an hour on = %+v, %v; want the twice-refused question retried", res, err)
	}
	_, claim = f.claims()
	if got := strings.Join(f.replay(labels), ","); got != "summary:"+claim[refused[0].ID]+",summary:"+claim[refused[1].ID]+",reasoning#0,summary:"+committed+",current#0" {
		t.Fatalf("replay = %s, want every summary in place", got)
	}
}

func TestPostgresCompactionCutOffSummaryRetriesHalfAtOnce(t *testing.T) {
	f, store := newProgressFixture(t)
	var rows []messagepkg.Message
	for n := 1; n <= 2; n++ {
		rows = append(rows, f.text("user", strings.Repeat(fmt.Sprintf("Q%d question. ", n), 160)), f.text("assistant", strings.Repeat(fmt.Sprintf("A%d answer. ", n), 160)))
	}
	f.reasoning()
	f.text("user", "current question")
	model := &countingSummarizer{summary: summaryTokens(120), cutOffOver: 7000}
	cfg := f.config(model)
	cfg.Manual = false
	svc := compaction.NewService(slog.New(slog.DiscardHandler), store)
	if _, err := svc.RunCompactionSync(f.ctx, cfg); err == nil {
		t.Fatal("first pass succeeded, want the summary cut off")
	}
	_, claim := f.claims()
	if got := f.compact(claim[rows[0].ID]); got.reason != "retry_half" || got.count != len(rows) {
		t.Fatalf("cut-off attempt = %+v, want it recorded against all four rows", got)
	}
	for pass := 1; pass <= 2; pass++ {
		if res, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg); err != nil || res.Status != compaction.StatusOK || res.MessageCount != 2 {
			t.Fatalf("pass %d = %+v, %v; want half the rows committed right away", pass, res, err)
		}
	}
	for _, row := range rows {
		if f.claimStatus(row.ID) != "ok" {
			t.Fatalf("row %s still raw", row.ID)
		}
	}
}

func TestPostgresCompactionScreenshotFeedbackIsNotTheLatestUser(t *testing.T) {
	f, store := newProgressFixture(t)
	f.text("user", "OLD question")
	f.text("assistant", "OLD answer")
	task := f.text("user", "TASK open the settings page")
	f.exec(1)
	raw, err := json.Marshal(map[string]any{"role": "user", "content": []map[string]any{{"type": "image", "image": "data:image/png;base64,iVBORw0KGgo="}}})
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := f.messages.Persist(f.ctx, messagepkg.PersistInput{
		BotID: f.botID, SessionID: f.sessionID, Role: "user", Content: raw, TurnRequestMessageID: task.ID,
		Metadata: map[string]any{messagepkg.MessageSourceMetadataKey: messagepkg.MessageSourceInternalFeedback},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(2)
	rows, err := store.ListUncompactedMessagesBySessionWithinBytes(f.ctx, dbsqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: f.uuid(f.sessionID),
		MaxBytes:  1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	latest := map[string]bool{}
	for _, row := range rows {
		latest[formatPGUUID(row.ID)] = row.LatestUser
	}
	if !latest[task.ID] || latest[feedback.ID] {
		t.Fatalf("latest user: task=%v feedback=%v; a screenshot read back in the turn starts no turn", latest[task.ID], latest[feedback.ID])
	}
}

func TestPostgresCompactionUnusableHoldIsCapped(t *testing.T) {
	f, store := newProgressFixture(t)
	refused := []messagepkg.Message{f.text("user", strings.Repeat("REFUSED question. ", 80)), f.text("assistant", strings.Repeat("REFUSED answer. ", 80))}
	f.text("user", "current question")
	model := &countingSummarizer{summary: summaryTokens(120), refuse: "REFUSED"}
	cfg := f.config(model)
	cfg.Manual = false
	if _, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg); err == nil {
		t.Fatal("first pass succeeded, want the refusal recorded")
	}
	_, claim := f.claims()
	// Refused many times over, the rows are still held for six hours at most.
	hold := func(ago string) {
		if _, err := f.pool.Exec(f.ctx, `UPDATE bot_history_message_compacts SET failure_attempts = 8, completed_at = now() - $2::interval WHERE id = $1`, claim[refused[0].ID], ago); err != nil {
			t.Fatal(err)
		}
	}
	model.refuse = ""
	hold("5 hours 59 minutes")
	if res, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg); err != nil || res.Status != compaction.StatusNoop || model.calls != 1 {
		t.Fatalf("pass within six hours = %+v, %v after %d calls; want the rows still held", res, err, model.calls)
	}
	hold("6 hours 1 minute")
	if res, err := compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg); err != nil || res.Status != compaction.StatusOK {
		t.Fatalf("pass after six hours = %+v, %v; want the rows retried", res, err)
	}
}

func TestPostgresCompactionScanPassesHeldRowsAndComesBackForThem(t *testing.T) {
	f, store := newProgressFixture(t)
	refused := []messagepkg.Message{f.text("user", strings.Repeat("REFUSED question. ", 80)), f.text("assistant", strings.Repeat("REFUSED answer. ", 80))}
	f.reasoning()
	f.filler(20)
	f.text("user", "current question")
	model := &countingSummarizer{summary: summaryTokens(120), refuse: "REFUSED"}
	cfg := f.config(model)
	cfg.Manual = false
	pass := func() (compaction.Result, error) {
		return compaction.NewService(slog.New(slog.DiscardHandler), store).RunCompactionSync(f.ctx, cfg)
	}
	if _, err := pass(); err == nil {
		t.Fatal("first pass succeeded, want the refusal recorded")
	}
	if _, err := pass(); err != nil {
		t.Fatal(err)
	}
	order, _ := f.claims()
	position := map[string]int{}
	for i, id := range order {
		position[id] = i
	}
	if after := f.scanAfter(); after == "" || position[after] <= position[refused[1].ID] {
		t.Fatalf("scan position %q is not past the held rows", after)
	}

	// The hold lapses after the scan position was recorded: the next pass
	// reads from the start again.
	f.elapse("16 minutes")
	model.refuse = ""
	if res, err := pass(); err != nil || res.Status != compaction.StatusOK || f.claimStatus(refused[0].ID) != "ok" {
		t.Fatalf("pass after the hold = %+v, %v; want the held rows behind the scan position tried again", res, err)
	}
}

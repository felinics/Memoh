package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/chat/checkpoint"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// A fold from rows reads the session's history as one text message per row
// under its role. The current input after the newest step stays raw after
// the summary, and so do the newest complete call groups the verbatim budget
// holds; the summary stands for the rows before them, and the checkpoint
// moves to the last row it folds.
func TestCompactKeepsTheCurrentInputAndTheNewestGroupsRaw(t *testing.T) {
	t.Parallel()

	rows := machineryCorpus(t)[:9]
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "Deployed after a screenshot."}
	svc := newMachineryService(q)
	cfg := currentConfig(stub, 4000, rows[8])

	passes, err := svc.Compact(context.Background(), cfg)
	if err != nil || len(passes) != 1 || passes[0].More {
		t.Fatalf("passes=%+v err=%v, want one pass", passes, err)
	}
	if !slices.Equal(q.folded, rowIDs(rows[:4])) {
		t.Fatalf("folded %d rows, want the 4 before the newest groups the budget keeps", len(q.folded))
	}
	active := q.checkpoints[q.active]
	if position := (messagepkg.HistoryPosition{Turn: active.TurnPosition, Seq: active.TurnMessageSeq}); position != rowPosition(rows[3]) || len(q.held()) != 1 || q.held()[0].SummaryID == "" {
		t.Fatalf("checkpoint at %+v holds %+v, want the summary at the last folded row", position, q.held())
	}
	for _, want := range []string{"deploy please", "[media]", instructionMarker, "<current_request>"} {
		if !strings.Contains(stub.prompt, want) {
			t.Fatalf("the summarizer request lacks %q:\n%s", want, stub.prompt)
		}
	}
	if strings.Contains(stub.prompt, "build ok done") {
		t.Fatal("the summarizer read a call group the fold keeps raw")
	}
}

// A row larger than one bounded read is read alone, and the summarizer reads
// its head and tail: the event folds it like any other row.
func TestCompactReadsARowLargerThanTheReadBoundAlone(t *testing.T) {
	t.Parallel()

	huge := "HUGE_HEAD " + strings.Repeat("payload ", 80000) + " HUGE_TAIL"
	rows := []candidateRow{
		mkRow(t, "user", `[{"type":"text","text":"`+huge+`"}]`),
		mkRow(t, "assistant", `[{"type":"text","text":"read it"}]`),
		mkRow(t, "user", `[{"type":"text","text":"recent question"}]`),
	}
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "A long payload was shared and read."}
	svc := newMachineryService(q)
	cfg := currentConfig(stub, 4000, rows[2])
	cfg.SummaryWindowTokens = 32000
	if size := payloadBytesForTest(rows[0]); size <= compactionReadMaxBytes(cfg.SummaryWindowTokens) {
		t.Fatalf("the row takes %d bytes, want more than one read admits", size)
	}

	passes, err := svc.Compact(context.Background(), cfg)
	if err != nil || len(passes) == 0 || !slices.Contains(q.folded, rows[0].ID) {
		t.Fatalf("passes=%d err=%v folded=%d rows, want the large row folded", len(passes), err, len(q.folded))
	}
	if !strings.Contains(stub.prompt, "HUGE_HEAD") || !strings.Contains(stub.prompt, "HUGE_TAIL") || len(stub.prompt) >= len(huge) {
		t.Fatalf("the summarizer read %d bytes, want the row's head and tail", len(stub.prompt))
	}
}

// An anchor a fold from rows keeps stays raw in its place: the checkpoint
// keeps it by reference before the summary, which folds the rows around it.
func TestCompactKeepsAnAnchorByReference(t *testing.T) {
	t.Parallel()

	rows := machineryCorpus(t)
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "Everything around the build request."}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 4000)
	cfg.Anchors = historySources(rows[4])

	if passes, err := svc.Compact(context.Background(), cfg); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	held := q.held()
	if len(held) != 2 || held[0].RowID != rows[4].ID.String() || held[1].SummaryID == "" || slices.Contains(q.folded, rows[4].ID) {
		t.Fatalf("checkpoint holds %+v, want the anchor kept before the summary", held)
	}
}

// A fold from rows reads only the history before its caller's cutoff, and an
// edit or retry first moves the session to the checkpoint its replacement
// keeps.
func TestCompactReadsBeforeItsCutoffAfterRewindingTheReplacedTurn(t *testing.T) {
	t.Parallel()

	rows := machineryCorpus(t)
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "The deployment and the screenshot."}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 4000)
	position := rowPosition(q.ordered()[4])
	cfg.CandidatesBefore = &position
	cfg.ReplacedTurnID = "00000000-0000-0000-0000-0000000000aa"

	if passes, err := svc.Compact(context.Background(), cfg); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	if !slices.Equal(q.folded, rowIDs(rows[:4])) || len(q.rewinds) != 1 {
		t.Fatalf("folded %d rows with %d rewinds, want the 4 before the cutoff after one rewind", len(q.folded), len(q.rewinds))
	}
	if strings.Contains(stub.prompt, "now build") {
		t.Fatal("the summarizer read a row from the cutoff on")
	}
}

// A row heavy in bytes and light in tokens, an inline screenshot, takes what
// its bytes fill of a history load: the event does not keep it raw, so the
// history advances until it loads whole, whether one read holds it or the
// byte bound truncates the read.
func TestCompactFoldsRowsTooLargeToLoad(t *testing.T) {
	t.Parallel()

	for _, screenshots := range []int{1, 4} {
		t.Run(fmt.Sprintf("%d screenshots", screenshots), func(t *testing.T) {
			t.Parallel()
			var rows []candidateRow
			for i := range screenshots {
				message := sdk.UserMessage(fmt.Sprintf("Screenshot %d", i), sdk.ImagePart{
					Image: "data:image/png;base64," + strings.Repeat("A", 1_330_000), MediaType: "image/png",
				})
				raw, err := historyfrag.MarshalStoredSDKMessage(message)
				if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, mkRow(t, "user", string(raw)), mkRow(t, "assistant", `"seen"`))
			}
			q := &fakeQueries{history: rows}
			stub := &stubModel{summary: "The earlier screenshots show the UI states before the current task."}
			svc := newMachineryService(q)
			cfg := machineryConfig(stub, 168_000)
			cfg.SummaryWindowTokens = 200_000

			if passes, err := svc.Compact(context.Background(), cfg); err != nil || len(passes) == 0 {
				t.Fatalf("passes=%d err=%v, want the screenshots folded", len(passes), err)
			}
			active := q.checkpoints[q.active]
			position := messagepkg.HistoryPosition{Turn: active.TurnPosition, Seq: active.TurnMessageSeq}
			last := rowPosition(rows[len(rows)-2])
			if position.Turn < last.Turn || position.Turn == last.Turn && position.Seq < last.Seq || slices.ContainsFunc(q.held(), func(item checkpoint.Item) bool { return item.RowID != "" }) {
				t.Fatalf("checkpoint at %+v holds %+v, want every screenshot folded", position, q.held())
			}
			if after, err := svc.synthesize(context.Background(), cfg); err != nil || after.truncated {
				t.Fatalf("err=%v truncated=%v, want the history to load whole after the fold", err, after.truncated)
			}
		})
	}
}

// A history larger than one bounded read is folded in passes: each reads the
// rows the one before could not, with the summary it wrote, until only the
// newest rows the verbatim budget keeps are left.
func TestCompactPassesOverWhatOneReadLeaves(t *testing.T) {
	t.Parallel()

	var rows []candidateRow
	for i := range 40 {
		rows = append(rows, mkRow(t, "user", `"`+strings.Repeat("history ", 80)+string(rune('a'+i%26))+`"`))
	}
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "The long history, condensed."}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 4000)
	cfg.SummaryWindowTokens = 4000

	passes, err := svc.Compact(context.Background(), cfg)
	if err != nil || len(passes) < 2 || stub.calls != len(passes) {
		t.Fatalf("passes=%d calls=%d err=%v, want several passes", len(passes), stub.calls, err)
	}
	for i := 1; i < len(passes); i++ {
		// A request built from rows renders its parent's summary first.
		if slices.Contains(passes[i].Order, 0) {
			t.Fatalf("pass %d left the summary of the pass before: %v", i, passes[i].Order)
		}
	}
	active := q.checkpoints[q.active]
	position := messagepkg.HistoryPosition{Turn: active.TurnPosition, Seq: active.TurnMessageSeq}
	last := slices.IndexFunc(rows, func(row candidateRow) bool { return rowPosition(row) == position })
	if kept := len(rows) - 1 - last; len(q.held()) != 1 || last < 0 || kept < 1 || kept > 5 {
		t.Fatalf("checkpoint at row %d holds %+v, want one summary over every row but the newest few", last, q.held())
	}
}

// A read the byte bound truncates ends at a call group's end: the tool rows
// after the last row it reads come with it. No checkpoint an event commits,
// an unfinished event's included, has a call behind its position and a
// result of it after, whether the bound falls at the call or between results
// written as rows of their own, and a call larger than a read alone still
// folds with its results.
func TestCompactNeverFoldsACallApartFromItsResults(t *testing.T) {
	t.Parallel()

	call := func(t *testing.T, size int, ids ...string) candidateRow {
		var parts []string
		for _, id := range ids {
			parts = append(parts, `{"type":"tool-call","toolCallId":"`+id+`","toolName":"write","input":{"content":"`+strings.Repeat("x", size/len(ids))+`"}}`)
		}
		return mkRow(t, "assistant", "["+strings.Join(parts, ",")+"]")
	}
	result := func(t *testing.T, id string, size int) candidateRow {
		return mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"`+id+`","toolName":"write","result":"written `+strings.Repeat("r", size)+`"}]`)
	}
	note := func(t *testing.T, size int) candidateRow {
		return mkRow(t, "user", `"`+strings.Repeat("plan the release ", size/17)+`"`)
	}
	for _, tc := range []struct {
		name string
		// rows returns the session's rows and the indexes of a call and its
		// results.
		rows func(t *testing.T) ([]candidateRow, int, []int)
	}{
		{name: "the bound falls at the call", rows: func(t *testing.T) ([]candidateRow, int, []int) {
			return []candidateRow{note(t, 400000), call(t, 200000, "W"), result(t, "W", 10), mkRow(t, "assistant", `"released"`), mkRow(t, "user", `"current request"`)}, 1, []int{2}
		}},
		{name: "the bound falls between results of their own", rows: func(t *testing.T) ([]candidateRow, int, []int) {
			return []candidateRow{note(t, 400000), call(t, 20, "A", "B"), result(t, "A", 150000), result(t, "B", 10), mkRow(t, "assistant", `"released"`), mkRow(t, "user", `"current request"`)}, 1, []int{2, 3}
		}},
		{name: "a call larger than a read", rows: func(t *testing.T) ([]candidateRow, int, []int) {
			return []candidateRow{call(t, 600000, "W"), result(t, "W", 10), mkRow(t, "assistant", `"released"`), mkRow(t, "user", `"current request"`)}, 0, []int{1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows, at, results := tc.rows(t)
			q := &fakeQueries{history: rows}
			stub := &stubModel{summary: "The release was planned and its notes written."}
			svc := newMachineryService(q)
			cfg := currentConfig(stub, 40000, rows[len(rows)-1])
			cfg.SummaryWindowTokens = 64000
			ordered := q.ordered()
			var split bool
			q.onComplete = func() {
				active := q.checkpoints[q.active]
				position := messagepkg.HistoryPosition{Turn: active.TurnPosition, Seq: active.TurnMessageSeq}
				for _, i := range results {
					split = split || !position.Before(rowPosition(ordered[at])) && position.Before(rowPosition(ordered[i]))
				}
			}

			passes, err := svc.Compact(context.Background(), cfg)
			if err != nil || len(passes) == 0 || split {
				t.Fatalf("passes=%d split=%v err=%v, want the call folded with its results", len(passes), split, err)
			}
			for _, i := range append([]int{at}, results...) {
				if !slices.Contains(q.folded, rows[i].ID) {
					t.Fatalf("the newest checkpoint does not fold row %d of the call group", i)
				}
			}
		})
	}
}

// A call group larger than any read admits is not left behind in silence: a
// read that takes nothing after the checkpoint, only the row it keeps,
// fails the event as an input overflow before any summarizer call.
func TestCompactFailsOnACallGroupNoReadAdmits(t *testing.T) {
	t.Parallel()

	kept := mkRow(t, "user", `"keep this request"`)
	rows := []candidateRow{
		kept,
		mkRow(t, "assistant", `[{"type":"tool-call","toolCallId":"W","toolName":"read","input":{}}]`),
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"W","toolName":"read","result":"`+strings.Repeat("r", 9<<20)+`"}]`),
	}
	q := &fakeQueries{history: rows}
	held := q.hold("Earlier work summarized.")
	items, _ := json.Marshal(append([]checkpoint.Item{{RowID: kept.ID.String()}}, decodeItemsForTest(held.Items)...))
	held.Items, held.TurnPosition, held.TurnMessageSeq = items, q.ordered()[0].TurnPosition.Int64, q.ordered()[0].TurnMessageSeq.Int64
	q.putCheckpoint(held)
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	cfg := currentConfig(stub, 40000, kept)
	cfg.SummaryWindowTokens = 64000

	passes, err := svc.Compact(context.Background(), cfg)
	if ReasonOf(err) != ReasonInputOverflow || len(passes) != 0 || stub.calls != 0 || q.active != held.ID {
		t.Fatalf("passes=%d calls=%d err=%v, want an input overflow before any summarizer call", len(passes), stub.calls, err)
	}
}

// Rows the checkpoint keeps that one read cannot hold together are read and
// folded one read after another: a read that stops among them still leaves
// something to fold.
func TestCompactFoldsKeptRowsLargerThanARead(t *testing.T) {
	t.Parallel()

	first := mkRow(t, "user", `"`+strings.Repeat("first long note ", 5<<20/16)+`"`)
	second := mkRow(t, "user", `"`+strings.Repeat("second long note ", 5<<20/17)+`"`)
	current := mkRow(t, "user", `"current request"`)
	q := &fakeQueries{history: []candidateRow{first, second, current}}
	held := q.hold("Earlier work summarized.")
	items, _ := json.Marshal(append([]checkpoint.Item{{RowID: first.ID.String()}, {RowID: second.ID.String()}}, decodeItemsForTest(held.Items)...))
	ordered := q.ordered()
	held.Items, held.TurnPosition, held.TurnMessageSeq = items, ordered[1].TurnPosition.Int64, ordered[1].TurnMessageSeq.Int64
	q.putCheckpoint(held)
	stub := &stubModel{summary: "Two long notes, condensed."}
	svc := newMachineryService(q)
	cfg := currentConfig(stub, 40000, current)
	cfg.SummaryWindowTokens = 64000

	passes, err := svc.Compact(context.Background(), cfg)
	if err != nil || len(passes) < 2 {
		t.Fatalf("passes=%d err=%v, want both kept rows folded, one read each", len(passes), err)
	}
	if kept := q.held(); slices.ContainsFunc(kept, func(item checkpoint.Item) bool { return item.RowID != "" }) {
		t.Fatalf("checkpoint holds %+v, want both long notes folded", kept)
	}
}

// A call the conversation moved on from without an answer is history like
// any other: a fold from rows reads it as text and takes it with the rows
// around it.
func TestCompactFoldsAnAskUserCallLaterHistoryClosed(t *testing.T) {
	t.Parallel()

	detail := strings.Repeat(" with the detail a real exchange carries", 120)
	rows := []candidateRow{
		mkRow(t, "user", `"plan the migration`+detail+`"`),
		mkRow(t, "assistant", `[{"type":"tool-call","toolCallId":"ask-1","toolName":"ask_user","input":{"questions":[{"question":"which database?"}]}}]`),
		mkRow(t, "user", `"never mind, do the docs instead`+detail+`"`),
		mkRow(t, "assistant", `"the docs are done`+detail+`"`),
		mkRow(t, "user", `"current request"`),
	}
	q := &fakeQueries{history: rows}
	stub := &stubModel{summary: "Asked which database, then did the docs instead."}
	svc := newMachineryService(q)

	if passes, err := svc.Compact(context.Background(), currentConfig(stub, 4000, rows[4])); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	if !slices.Equal(q.folded, rowIDs(rows[:4])) || !strings.Contains(stub.prompt, "[tool_call: ask_user]") {
		t.Fatalf("folded %d rows, want the closed call folded with the history around it", len(q.folded))
	}
}

// The summarizer reads the caller's current input to judge what the work
// needs, and the log records which model wrote the summary.
func TestCompactShowsTheCurrentRequestAndRecordsTheModel(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{history: machineryCorpus(t)}
	stub := &stubModel{summary: "Deployment history."}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 4000)
	cfg.CurrentRequest = "SHIP_IT: release the build"
	cfg.ModelRecordID = "00000000-0000-0000-0000-0000000000bb"

	if passes, err := svc.Compact(context.Background(), cfg); err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	if !strings.Contains(stub.prompt, "<current_request>") || !strings.Contains(stub.prompt, "SHIP_IT: release the build") {
		t.Fatalf("the summarizer was not told the current request:\n%s", stub.prompt)
	}
	if q.completed.Status != "ok" || q.completed.ModelID.String() != cfg.ModelRecordID {
		t.Fatalf("log = %+v, want it completed with the summarizer's model", q.completed)
	}
}

// A transient summarizer failure is asked once more; a rejected request is
// not. A failed pass settles its log as an error and leaves the session's
// checkpoint as it was.
func TestCompactRetriesATransientSummarizerFailureOnce(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		statuses []int
		calls    int
		reason   Reason
	}{
		{name: "transient", statuses: []int{http.StatusServiceUnavailable}, calls: 2},
		{name: "rejected", statuses: []int{http.StatusBadRequest}, calls: 1, reason: ReasonSummarizerFailed},
		{name: "down", statuses: []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable}, calls: 2, reason: ReasonSummarizerFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := &fakeQueries{history: machineryCorpus(t)}
			stub := &stubModel{summary: "Deployment history.", statuses: tc.statuses}
			svc := newMachineryService(q)

			passes, err := svc.Compact(context.Background(), machineryConfig(stub, 4000))
			if ReasonOf(err) != tc.reason || stub.calls != tc.calls {
				t.Fatalf("passes=%d err=%v calls=%d, want reason %q after %d calls", len(passes), err, stub.calls, tc.reason, tc.calls)
			}
			if tc.reason != "" && (q.completed.Status != "error" || len(q.held()) != 0) {
				t.Fatalf("log=%+v held=%+v, want the log settled as an error and no checkpoint", q.completed, q.held())
			}
		})
	}
}

// A summary that does not finish naturally is not committed.
func TestCompactRejectsATruncatedSummary(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{history: machineryCorpus(t)}
	stub := &stubModel{summary: "Deployment hist", finishReason: "length"}
	svc := newMachineryService(q)

	if _, err := svc.Compact(context.Background(), machineryConfig(stub, 4000)); ReasonOf(err) != ReasonTruncated || len(q.held()) != 0 {
		t.Fatalf("err=%v held=%+v, want a truncated summary refused", err, q.held())
	}
}

func rowIDs(rows []candidateRow) []pgtype.UUID {
	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids
}

type panickingHookProvider struct {
	armed atomic.Bool
	calls atomic.Int32
}

func (p *panickingHookProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	p.calls.Add(1)
	if p.armed.Load() {
		panic("boom: injected hook provider panic")
	}
	return nil, errors.New("no workspace client")
}

func TestPreHookPanicPropagates(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{history: machineryCorpus(t)}
	stub := &stubModel{summary: "unused"}
	svc := newMachineryService(q)
	provider := &panickingHookProvider{}
	provider.armed.Store(true)
	svc.SetHookService(hooks.NewService(slog.New(slog.DiscardHandler), provider))
	cfg := machineryConfig(stub, 4000)

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = svc.Compact(context.Background(), cfg)
	}()
	if r := <-recovered; r == nil {
		t.Fatal("a pre-hook panic must propagate, not be swallowed")
	}
	if active := svc.ActiveSessions(cfg.BotID); len(active) != 0 || stub.calls != 0 {
		t.Fatalf("active sessions %v, model calls %d, want neither after a pre-hook panic", active, stub.calls)
	}
}

// TestPostCompactHookPanicDoesNotPoisonASuccessfulRun pins the post-hook
// panic containment: the compaction itself succeeded and its outcome is
// already persisted, so a panicking PostCompact hook must not escape or turn
// the published result into a lie. The panicking hook service is swapped in
// mid-run, after the pre-hook already passed.
func TestPostCompactHookPanicDoesNotPoisonASuccessfulRun(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{history: machineryCorpus(t)}
	stub := &stubModel{summary: "SUMMARY-OK"}
	svc := newMachineryService(q)
	provider := &panickingHookProvider{}
	provider.armed.Store(true)
	q.onComplete = func() {
		svc.SetHookService(hooks.NewService(slog.New(slog.DiscardHandler), provider))
	}
	cfg := machineryConfig(stub, 4000)

	recovered := make(chan any, 1)
	results := make(chan []Pass, 1)
	errs := make(chan error, 1)
	go func() {
		defer func() { recovered <- recover() }()
		passes, err := svc.Compact(context.Background(), cfg)
		results <- passes
		errs <- err
	}()
	if r := <-recovered; r != nil {
		t.Fatalf("a post-hook panic must be contained, but escaped: %v", r)
	}
	if err := <-errs; err != nil {
		t.Fatalf("a post-hook panic must not fail the completed compaction: %v", err)
	}
	if passes := <-results; len(passes) != 1 || passes[0].Summary != "SUMMARY-OK" {
		t.Fatalf("completed compaction result lost to a post-hook panic: %#v", passes)
	}
	if provider.calls.Load() == 0 {
		t.Fatal("the post-hook must actually have run (and panicked)")
	}
}

func mkRow(t *testing.T, role, content string) candidateRow {
	t.Helper()
	return candidateRow{
		ID:      pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Role:    role,
		Content: json.RawMessage(content),
	}
}

// The summarizer of a request built from rows is offered no tools, so it
// reads every message as text under its role.
func TestWrittenOutTurnsToolsAndMediaIntoText(t *testing.T) {
	t.Parallel()
	b64 := strings.Repeat("QUJD", 100)
	got := writtenOut([]sdk.Message{
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.TextPart{Text: "running"},
			sdk.ToolCallPart{ToolCallID: "B", ToolName: "exec", Input: toolexec.ArgumentsFromValue(map[string]any{"cmd": "make"})},
		}},
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "B", ToolName: "exec", Result: sdk.TextOutput("ok " + b64)}),
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.ImagePart{Image: b64}}},
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "thinking"}}},
	})
	want := []string{"running\n[tool_call: exec] {\"cmd\":\"make\"}", "[tool_result: exec] ok [media]", "[image]", "[no visible content]"}
	roles := []sdk.MessageRole{sdk.MessageRoleAssistant, sdk.MessageRoleUser, sdk.MessageRoleUser, sdk.MessageRoleAssistant}
	for i, message := range got {
		if message.Role != roles[i] || messageText(message) != want[i] {
			t.Fatalf("message %d = %s %q, want %s %q", i, message.Role, messageText(message), roles[i], want[i])
		}
	}
}

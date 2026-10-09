package compaction

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// prose is a plain text row whose rendered entry costs about renderTokens and
// whose recorded usage (what splitByTarget weighs) is usageTokens.
func prose(t *testing.T, role, tag string, renderTokens, usageTokens int) sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	word := tag + " "
	return mkRow(t, role, jsonStr(strings.Repeat(word, max(1, renderTokens*4/len(word)))), usageTokens)
}

func markedSet(q *sessionStore) map[pgtype.UUID]bool { return idSet(q.markedIDs) }

func TestCompactionNeverClaimsAcrossAnEarlierSummary(t *testing.T) {
	t.Parallel()

	task := prose(t, "user", "TASK", 300, 100)
	q := newSessionStore(task)
	var steps []sqlc.ListUncompactedMessagesBySessionRow
	for i := 0; i < 6; i++ {
		steps = append(steps, prose(t, "assistant", fmt.Sprintf("STEP%d", i+1), 300, 1000))
	}
	q.append(steps...)
	stub := &stubModel{summary: "steps condensed"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 1500)

	// One long turn: the task prompt stays raw while its older steps compact.
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("first pass = %+v, %v", res, err)
	}
	if marked := markedSet(q); marked[task.ID] || len(marked) != 5 {
		t.Fatalf("first pass claimed %d rows (task claimed: %v), want steps 1-5 only", len(marked), marked[task.ID])
	}

	// The next turn: the task prompt is no longer protected, but the summary
	// of steps 1-5 sits between it and step 6.
	q.append(prose(t, "user", "NEXT", 10, 10), prose(t, "assistant", "NEW", 300, 1000), prose(t, "assistant", "NEW", 300, 1000))
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("second pass = %+v, %v", res, err)
	}
	if len(q.markedIDs) != 1 || q.markedIDs[0] != task.ID {
		t.Fatalf("second pass claimed %d rows, want only the task prompt in front of the earlier summary", len(q.markedIDs))
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionMovesPastIneffectiveSpanAcrossRestart(t *testing.T) {
	t.Parallel()

	first := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "FIRST", 150, 100), prose(t, "assistant", "FIRST", 150, 100)}
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 1000, 100), prose(t, "assistant", "LATER", 1000, 100)}
	q := newSessionStore(first...)
	q.append(reasoningOnlyRow(t))
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 400)}
	cfg := machineryConfig(stub, 50)
	cfg.Manual = true

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), errIneffectiveSummary.Error()) {
		t.Fatalf("first pass error = %v, want the first span rejected as ineffective", err)
	}
	if q.completed.FailureReason != failureReasonIneffectiveSummary {
		t.Fatalf("failure reason = %q, want %q", q.completed.FailureReason, failureReasonIneffectiveSummary)
	}

	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("second pass = %+v, %v; want the later span", res, err)
	}
	if marked := markedSet(q); len(marked) != 2 || !marked[later[0].ID] || !marked[later[1].ID] {
		t.Fatalf("second pass claimed %v, want the later span", q.markedIDs)
	}

	res, err = newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusNoop || res.Reason != ReasonNoBeneficialSpan {
		t.Fatalf("third pass = %+v, %v; want a noop that leaves the ineffective span raw", res, err)
	}
	if stub.calls != 2 {
		t.Fatalf("provider calls = %d, want 2: an ineffective span is never resent", stub.calls)
	}
	for _, row := range first {
		if q.logStatuses[q.claims[row.ID]] == "ok" {
			t.Fatal("ineffective span was published")
		}
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionRetriesSpanAfterProviderFailure(t *testing.T) {
	t.Parallel()

	span := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "SPAN", 400, 100), prose(t, "assistant", "SPAN", 400, 100)}
	q := newSessionStore(span...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	failing := machineryConfig(&stubModel{}, 50)
	failing.Manual = true
	failing.HTTPClient = &http.Client{Transport: &failingModel{}}
	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), failing); err == nil {
		t.Fatal("provider failure must surface")
	}
	if q.completed.FailureReason != "" {
		t.Fatalf("provider failure recorded reason %q; only an ineffective summary holds rows back", q.completed.FailureReason)
	}

	stub := &stubModel{summary: "span condensed"}
	cfg := machineryConfig(stub, 50)
	cfg.Manual = true
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK || res.MessageCount != 2 {
		t.Fatalf("retry = %+v, %v; want the same span committed", res, err)
	}
}

func TestCompactionManyShortSpansCostOneProviderCall(t *testing.T) {
	t.Parallel()

	q := newSessionStore()
	for i := 0; i < 20; i++ {
		q.append(prose(t, "user", fmt.Sprintf("tiny%d", i), 8, 10), reasoningOnlyRow(t))
	}
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 600, 100), prose(t, "assistant", "LATER", 600, 100)}
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if stub.calls != 1 || len(q.markedIDs) != 2 || q.markedIDs[0] != later[0].ID {
		t.Fatalf("calls=%d marked=%d, want one call claiming only the later span", stub.calls, len(q.markedIDs))
	}
}

func TestCompactionHeldBackHistoryEndsWithReasonAndNoProviderCall(t *testing.T) {
	t.Parallel()

	cases := map[string][]sqlc.ListUncompactedMessagesBySessionRow{
		"protected":  append(askUserExchange(t, 1), askUserExchange(t, 2)...),
		"unrendered": {reasoningOnlyRow(t), reasoningOnlyRow(t)},
		"too small":  {prose(t, "user", "tiny", 8, 10), reasoningOnlyRow(t), prose(t, "assistant", "tiny", 8, 10)},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			q := newSessionStore(rows...)
			q.append(prose(t, "user", "CURRENT", 10, 10))
			stub := &stubModel{summary: "never"}
			cfg := machineryConfig(stub, 5)
			cfg.Manual = true
			svc := newMachineryService(q)
			for pass := 0; pass < 3; pass++ {
				res, err := svc.RunCompactionSync(context.Background(), cfg)
				if err != nil || res.Status != StatusNoop || res.Reason != ReasonNoBeneficialSpan {
					t.Fatalf("pass %d = %+v, %v; want noop %s", pass+1, res, err, ReasonNoBeneficialSpan)
				}
			}
			if stub.calls != 0 || q.created {
				t.Fatalf("calls=%d created=%v, want no attempt at all", stub.calls, q.created)
			}
		})
	}
}

// fillerExchange is a protected ask_user exchange of roughly bytes payload.
func fillerExchange(t *testing.T, n, bytes int) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	id := fmt.Sprintf("fill-%d", n)
	question := strings.Repeat("q", max(0, bytes-200))
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"tool-call","toolCallId":"`+id+`","toolName":"ask_user","input":{"q":"`+question+`"}}]`, 10),
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"`+id+`","toolName":"ask_user","output":"ok"}]`, 10),
	}
}

func TestCompactionReachesSpanBeyondFirstReadWindow(t *testing.T) {
	t.Parallel()

	q := newSessionStore()
	for i := 0; i < 150; i++ {
		q.append(fillerExchange(t, i, 4096)...)
		if i%10 == 0 {
			q.append(prose(t, "user", "tiny", 8, 10), reasoningOnlyRow(t))
		}
	}
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 800, 100), prose(t, "assistant", "LATER", 800, 100)}
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	measure, _ := q.MeasureUncompactedMessagesBySession(context.Background(), pgtype.UUID{})
	if measure.CandidateBytes <= minCompactionReadBytes {
		t.Fatalf("fixture holds %d bytes; it must overflow one %d-byte window", measure.CandidateBytes, minCompactionReadBytes)
	}

	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	started := time.Now()
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if stub.calls != 1 || len(q.markedIDs) != 2 || q.markedIDs[0] != later[0].ID {
		t.Fatalf("calls=%d marked=%d, want the later span in one call", stub.calls, len(q.markedIDs))
	}
	if q.windows < 2 || q.readBytes > 2*measure.CandidateBytes || time.Since(started) > 5*time.Second {
		t.Fatalf("windows=%d read=%d of %d candidate bytes in %v", q.windows, q.readBytes, measure.CandidateBytes, time.Since(started))
	}
}

func TestCompactionClaimsToolExchangeSplitByWindowEdgeWhole(t *testing.T) {
	t.Parallel()

	exchange := execExchange(t, 7)
	call, result := payloadBytes(exchange[0]), payloadBytes(exchange[1])
	q := newSessionStore()
	var filled int64
	for i := 0; filled+4096+call <= minCompactionReadBytes; i++ {
		rows := fillerExchange(t, i, 4096)
		q.append(rows...)
		filled += payloadBytes(rows[0]) + payloadBytes(rows[1])
	}
	pad := minCompactionReadBytes - filled - call - result/2
	q.append(fillerExchange(t, -1, int(pad))...)
	q.append(exchange...)
	q.append(prose(t, "assistant", "AFTER", 600, 100), prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	marked := markedSet(q)
	if !marked[exchange[0].ID] || !marked[exchange[1].ID] {
		t.Fatalf("claimed call=%v result=%v, want the exchange claimed whole from the next window", marked[exchange[0].ID], marked[exchange[1].ID])
	}
	if q.windows < 2 {
		t.Fatalf("windows = %d, want the exchange to cross the first window edge", q.windows)
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionOversizedCandidateEndsWithReason(t *testing.T) {
	t.Parallel()

	q := newSessionStore(prose(t, "user", "HUGE", int(minCompactionReadBytes), 100), prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: "never"}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 5))
	if err != nil || res.Status != StatusNoop || res.Reason != ReasonReadBudgetExceeded || stub.calls != 0 {
		t.Fatalf("result = %+v, %v after %d calls; want noop %s", res, err, stub.calls, ReasonReadBudgetExceeded)
	}
}

func TestCompactionSkipsShortRowInFrontOfAnEarlierSummary(t *testing.T) {
	t.Parallel()

	task := prose(t, "user", "TASK", 30, 100)
	q := newSessionStore(task)
	for i := 0; i < 6; i++ {
		q.append(prose(t, "assistant", fmt.Sprintf("STEP%d", i+1), 300, 1000))
	}
	stub := &stubModel{summary: "steps condensed"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 1500)
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("first pass = %+v, %v", res, err)
	}
	stepSix := q.history[6]
	q.append(prose(t, "user", "NEXT", 10, 10), prose(t, "assistant", "NEW", 300, 1000), prose(t, "assistant", "NEW", 300, 1000))
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
		t.Fatalf("second pass = %+v, %v", res, err)
	}
	if len(q.markedIDs) != 1 || q.markedIDs[0] != stepSix.ID {
		t.Fatalf("second pass claimed %d rows, want step 6 alone: the short task prompt ends at the earlier summary", len(q.markedIDs))
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionReadsSpanCrossingWindowEdgeWhole(t *testing.T) {
	t.Parallel()

	var run []sqlc.ListUncompactedMessagesBySessionRow
	for i := 0; i < 12; i++ {
		run = append(run, prose(t, "assistant", fmt.Sprintf("RUN%d", i), 120, 100))
	}
	// Leave room for exactly two run rows in the first window: together they
	// stay under the span floor, the whole run does not.
	room := payloadBytes(run[0]) + payloadBytes(run[1]) + payloadBytes(run[2])/2
	q := newSessionStore()
	var filled int64
	for i := 0; filled+8192 < minCompactionReadBytes-room; i++ {
		rows := fillerExchange(t, i, 4096)
		q.append(rows...)
		filled += payloadBytes(rows[0]) + payloadBytes(rows[1])
	}
	pad := fillerExchange(t, -1, 4096)
	pad = fillerExchange(t, -1, 4096+int(minCompactionReadBytes-room-filled-payloadBytes(pad[0])-payloadBytes(pad[1])))
	q.append(pad...)
	q.append(run...)
	q.append(prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if q.windows < 2 {
		t.Fatalf("windows = %d, want the run to cross the first window edge", q.windows)
	}
	if len(q.markedIDs) != len(run) || q.markedIDs[0] != run[0].ID {
		t.Fatalf("claimed %d rows, want the %d-row run read whole across the window edge", len(q.markedIDs), len(run))
	}
}

func TestCompactionPassesWindowFilledByOneSmallRun(t *testing.T) {
	t.Parallel()

	// Heavy stored metadata, little summarizer text: one run fills the whole
	// first window yet stays under the span floor. Re-reading it could not
	// advance, so the scan moves past it and ends with a reason.
	metadata := []byte(`{"trace":"` + strings.Repeat("m", 60_000) + `"}`)
	q := newSessionStore()
	for i := 0; i < 12; i++ {
		row := prose(t, "assistant", fmt.Sprintf("R%d", i), 4, 10)
		row.Metadata = metadata
		q.append(row)
	}
	q.append(prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: "s"}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 5))
	if err != nil || res.Status != StatusNoop || res.Reason != ReasonNoBeneficialSpan {
		t.Fatalf("result = %+v, %v; want a bounded noop past the window-filling run", res, err)
	}
	if q.windows != 2 || stub.calls != 0 {
		t.Fatalf("windows=%d calls=%d, want the second window read and no call", q.windows, stub.calls)
	}
}

func TestCompactionRetriesIneffectiveRowsWithNewHistory(t *testing.T) {
	t.Parallel()

	old := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "OLD", 150, 100), prose(t, "assistant", "OLD", 150, 100)}
	q := newSessionStore(old...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: summaryOfTokens(t, 400)}
	cfg := machineryConfig(stub, 50)
	cfg.Manual = true
	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); err == nil {
		t.Fatal("first pass must reject the ineffective summary")
	}

	// The turn continues right after the failed rows: together they are
	// worth another call, and the claim covers both.
	current := q.history[len(q.history)-1]
	q.history = q.history[:len(q.history)-1]
	q.append(current, prose(t, "assistant", "NEW", 600, 100), prose(t, "user", "NEXT", 10, 10))
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("second pass = %+v, %v", res, err)
	}
	if marked := markedSet(q); !marked[old[0].ID] || !marked[old[1].ID] || len(marked) != 4 {
		t.Fatalf("claimed %d rows (old: %v %v), want the failed rows retried with the new ones", len(marked), marked[old[0].ID], marked[old[1].ID])
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionNeverResendsOnlyIneffectiveRows(t *testing.T) {
	t.Parallel()

	old := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "OLD", 300, 100), prose(t, "assistant", "OLD", 300, 100)}
	fresh := prose(t, "assistant", "NEW", 300, 100)
	q := newSessionStore(old...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: summaryOfTokens(t, 700)}
	cfg := machineryConfig(stub, 50)
	cfg.Manual = true
	cfg.MaxCompactTokens = 700
	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); err == nil {
		t.Fatal("first pass must reject the ineffective summary")
	}

	// The entries budget from the span start only reaches the failed rows
	// and a short one; the claim moves to where new rows clear the floor.
	current := q.history[len(q.history)-1]
	q.history = q.history[:len(q.history)-1]
	q.append(current, fresh, prose(t, "user", "NEXT", 10, 10))
	stub.summary = "new rows condensed"
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("second pass = %+v, %v", res, err)
	}
	if marked := markedSet(q); marked[old[0].ID] || !marked[fresh.ID] {
		t.Fatalf("claimed %v, want the claim to start past the oldest failed row and carry the new row", q.markedIDs)
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionNeverClaimsOrphanedToolResult(t *testing.T) {
	t.Parallel()

	// A result whose call was summarized earlier (a claim the read window
	// edge used to split) must stay raw instead of being summarized alone.
	orphan := execExchange(t, 3)[1]
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 400, 100), prose(t, "assistant", "LATER", 400, 100)}
	q := newSessionStore(orphan)
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if marked := markedSet(q); marked[orphan.ID] || len(marked) != 2 {
		t.Fatalf("claimed orphan=%v rows=%d, want only the later span", marked[orphan.ID], len(marked))
	}
}

func TestCompactionFusionStillAbsorbsFrontierThroughSmallSpan(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "fused frontier"}
	cfg := machineryConfig(stub, 5)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	rows := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "note", 8, 10), prose(t, "user", "CURRENT", 10, 10)}
	setFusionRowScopeAndTimes(t, cfg, rows)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	q := &fakeQueries{uncompacted: rows, priorLogs: parents}

	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK || len(q.rollupCalls) != 1 {
		t.Fatalf("result = %+v, %v with %d rollups; a rollup replaces the frontier too, so a small span still pays", res, err, len(q.rollupCalls))
	}
}

func TestCompactionScanResumesPastItsByteBudgetOnTheNextPass(t *testing.T) {
	t.Parallel()

	q := newSessionStore()
	var filled int64
	for i := 0; filled <= maxCompactionScanBytes+minCompactionReadBytes; i++ {
		rows := fillerExchange(t, i, 4096)
		q.append(rows...)
		filled += payloadBytes(rows[0]) + payloadBytes(rows[1])
	}
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 800, 100), prose(t, "assistant", "LATER", 800, 100)}
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusNoop || res.Reason != ReasonReadBudgetExceeded || stub.calls != 0 {
		t.Fatalf("first pass = %+v, %v after %d calls; want the scan to stop at its budget", res, err, stub.calls)
	}
	if q.readBytes > maxCompactionScanBytes+minCompactionReadBytes || !q.scanAfter.Valid {
		t.Fatalf("first pass read %d bytes, recorded position %v", q.readBytes, q.scanAfter.Valid)
	}

	// A restarted service continues after the recorded position instead of
	// rescanning the protected prefix.
	q.readBytes, q.windows = 0, 0
	res, err = newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 50))
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 2 || q.markedIDs[0] != later[0].ID {
		t.Fatalf("second pass = %+v, %v claiming %d rows; want the later span", res, err, len(q.markedIDs))
	}
	if q.readBytes > filled-maxCompactionScanBytes+minCompactionReadBytes {
		t.Fatalf("second pass read %d bytes; it must start where the first one settled", q.readBytes)
	}
}

func TestCompactionScanPositionNeverPassesAFreshClaim(t *testing.T) {
	t.Parallel()

	q := newSessionStore()
	q.append(askUserExchange(t, 1)...)
	crashed := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "CRASHED", 400, 100), prose(t, "assistant", "CRASHED", 400, 100)}
	q.append(crashed...)
	q.append(askUserExchange(t, 2)...)
	q.append(prose(t, "user", "CURRENT", 10, 10))

	// An attempt claimed the span and never finished: its rows are held
	// back while the claim is fresh.
	ctx := context.Background()
	attempt, _ := q.CreateCompactionLog(ctx, sqlc.CreateCompactionLogParams{})
	_, _ = q.MarkMessagesCompacted(ctx, sqlc.MarkMessagesCompactedParams{CompactID: attempt.ID, MessageIds: []pgtype.UUID{crashed[0].ID, crashed[1].ID}})
	stub := &stubModel{summary: "crashed span condensed"}
	cfg := machineryConfig(stub, 5)
	res, err := newMachineryService(q).RunCompactionSync(ctx, cfg)
	if err != nil || res.Status != StatusNoop {
		t.Fatalf("pass during the fresh claim = %+v, %v", res, err)
	}
	if q.scanAfter != q.history[1].ID {
		t.Fatal("scan position passed the freshly claimed rows")
	}

	// The claim lapses: its rows come back and are still reached.
	q.logStatuses[attempt.ID] = "error"
	res, err = newMachineryService(q).RunCompactionSync(ctx, cfg)
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 2 || q.markedIDs[0] != crashed[0].ID {
		t.Fatalf("pass after the claim lapsed = %+v, %v; want the span claimed", res, err)
	}
}

func TestCompactionScanPositionResetsWithTheEpoch(t *testing.T) {
	t.Parallel()

	q := newSessionStore(askUserExchange(t, 1)...)
	q.append(prose(t, "user", "tiny", 8, 10), reasoningOnlyRow(t), prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: "never"}
	if res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 5)); err != nil || res.Reason != ReasonNoBeneficialSpan || !q.scanAfter.Valid {
		t.Fatalf("first pass = %+v, %v, position recorded %v", res, err, q.scanAfter.Valid)
	}
	q.epoch++
	window, err := q.ListUncompactedMessagesBySessionWithinBytes(context.Background(), sqlc.ListUncompactedMessagesBySessionWithinBytesParams{MaxBytes: minCompactionReadBytes})
	if err != nil || window[0].ID != q.history[0].ID {
		t.Fatal("a new epoch must read from the start of the session again")
	}
}

func TestCompactionTrimmedClaimStillClearsTheFloor(t *testing.T) {
	t.Parallel()

	// A short row followed by a tool exchange that alone fills the entries
	// budget: trimming must not shrink the claim to the short row.
	short := prose(t, "user", "short", 20, 10)
	callID := "big"
	output := strings.Repeat("BIG output line; ", 150)
	exchange := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", `[{"type":"tool-call","toolCallId":"`+callID+`","toolName":"exec","input":{"command":"make"}}]`, 10),
		mkRow(t, "tool", `[{"type":"tool-result","toolCallId":"`+callID+`","toolName":"exec","output":{"type":"text","value":`+jsonStr(output)+`}}]`, 10),
	}
	q := newSessionStore(short)
	q.append(exchange...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	items, _ := itemsFromRows(exchange)
	exchangeCost := markableGroupCost(items, []int{0, 1})
	stub := &stubModel{summary: summaryOfTokens(t, 254)}
	cfg := machineryConfig(stub, 5)
	cfg.MaxCompactTokens = exchangeCost + 5
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if marked := markedSet(q); marked[short.ID] || !marked[exchange[0].ID] || !marked[exchange[1].ID] {
		t.Fatalf("claimed short=%v exchange=%v/%v, want the exchange that clears the floor", marked[short.ID], marked[exchange[0].ID], marked[exchange[1].ID])
	}
}

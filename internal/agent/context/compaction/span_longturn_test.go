package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// longTurn appends one task prompt followed by n tool steps and returns the
// prompt and the steps.
func longTurn(t *testing.T, q *sessionStore, n int) (sqlc.ListUncompactedMessagesBySessionRow, []sqlc.ListUncompactedMessagesBySessionRow) {
	t.Helper()
	task := prose(t, "user", "TASK", 120, 50)
	q.append(task)
	var steps []sqlc.ListUncompactedMessagesBySessionRow
	for i := 0; i < n; i++ {
		steps = append(steps, execExchange(t, 1000+i)...)
	}
	q.append(steps...)
	return task, steps
}

func TestCompactionLongTurnCompactsStepsBehindAShortRow(t *testing.T) {
	t.Parallel()

	// A short row in front of the task prompt can still grow once the turn
	// is over, so nothing older than the turn is claimable. The turn's older
	// steps must compact anyway; the task prompt and the latest step stay.
	q := newSessionStore(prose(t, "user", "ok", 4, 5))
	task, steps := longTurn(t, q, 40)
	stub := &stubModel{summary: "steps condensed"}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 2000))
	if err != nil || res.Status != StatusOK {
		t.Fatalf("result = %+v, %v; want the turn's older steps committed", res, err)
	}
	marked := markedSet(q)
	if marked[task.ID] || marked[steps[len(steps)-1].ID] || !marked[steps[0].ID] {
		t.Fatalf("claimed task=%v last=%v first=%v; want older steps only", marked[task.ID], marked[steps[len(steps)-1].ID], marked[steps[0].ID])
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionTruncatedWindowNeverClaimsTheCurrentTask(t *testing.T) {
	t.Parallel()

	q := newSessionStore()
	var filled int64
	for i := 0; filled < minCompactionReadBytes-16_384; i++ {
		rows := fillerExchange(t, i, 4096)
		q.append(rows...)
		filled += payloadBytes(rows[0]) + payloadBytes(rows[1])
	}
	task, steps := longTurn(t, q, 6)
	// The latest step's result is too large to share a window with the rest.
	huge := execExchange(t, 9999)
	huge[1].Content = []byte(`[{"type":"tool-result","toolCallId":"exec-9999","toolName":"exec","output":{"type":"text","value":"` + strings.Repeat("x", 400_000) + `"}}]`)
	q.append(huge...)

	stub := &stubModel{summary: "steps condensed"}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 2000))
	if err != nil {
		t.Fatal(err)
	}
	marked := markedSet(q)
	if marked[task.ID] {
		t.Fatalf("the current task prompt was claimed (result %+v)", res)
	}
	if res.Status == StatusOK && !marked[steps[0].ID] {
		t.Fatal("a commit that skips the turn's older steps")
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionPassesAnOversizedRowToReachLaterHistory(t *testing.T) {
	t.Parallel()

	early := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "EARLY", 400, 100), prose(t, "assistant", "EARLY", 400, 100)}
	q := newSessionStore(early...)
	q.append(prose(t, "assistant", "HUGE", int(minCompactionReadBytes)/4+1000, 100))
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 400, 100), prose(t, "assistant", "LATER", 400, 100)}
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: "condensed"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 5)
	for pass := 0; pass < 2; pass++ {
		if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusOK {
			t.Fatalf("pass %d = %+v, %v", pass+1, res, err)
		}
	}
	if marked := markedSet(q); len(marked) != 2 || !marked[later[0].ID] {
		t.Fatalf("second pass claimed %v, want the span past the oversized row", q.markedIDs)
	}
	assertClaimsContiguous(t, q)
}

func TestCompactionSkipsSpanItsBudgetCannotClaim(t *testing.T) {
	t.Parallel()

	// The first span holds enough new rows in total, but a row already proved
	// ineffective sits between them, and no stretch within the entries budget
	// clears the floor. Selection must move on to the later span instead of
	// stopping there.
	a, proved, c := prose(t, "user", "A", 200, 10), prose(t, "assistant", "PROVED", 300, 10), prose(t, "user", "C", 120, 10)
	q := newSessionStore(a, proved, c)
	ctx := context.Background()
	failed, _ := q.CreateCompactionLog(ctx, sqlc.CreateCompactionLogParams{})
	_, _ = q.MarkMessagesCompacted(ctx, sqlc.MarkMessagesCompactedParams{CompactID: failed.ID, MessageIds: []pgtype.UUID{proved.ID}})
	_, _ = q.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{ID: failed.ID, Status: "error", FailureReason: failureReasonIneffectiveSummary})
	q.append(reasoningOnlyRow(t))
	later := prose(t, "user", "LATER", 300, 100)
	q.append(later, prose(t, "user", "CURRENT", 10, 10))

	stub := &stubModel{summary: summaryOfTokens(t, 120)}
	cfg := machineryConfig(stub, 5)
	cfg.MaxCompactTokens = 520 // entries budget at least 260
	res, err := newMachineryService(q).RunCompactionSync(ctx, cfg)
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 1 || q.markedIDs[0] != later.ID {
		t.Fatalf("result = %+v, %v claiming %d rows; want the later span", res, err, len(q.markedIDs))
	}
}

func TestCompactionIneffectiveSummaryDoesNotCoolDownTheNextPass(t *testing.T) {
	t.Parallel()

	first := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "FIRST", 150, 100), prose(t, "assistant", "FIRST", 150, 100)}
	later := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "LATER", 600, 100), prose(t, "assistant", "LATER", 600, 100)}
	q := newSessionStore(first...)
	q.append(reasoningOnlyRow(t))
	q.append(later...)
	q.append(prose(t, "user", "CURRENT", 10, 10))
	stub := &stubModel{summary: summaryOfTokens(t, 400)}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 50)
	cfg.HardPressure = true
	if _, err := svc.RunCompactionSync(context.Background(), cfg); !errors.Is(err, ErrIneffectiveSummary) {
		t.Fatalf("first automatic pass = %v, want an ineffective summary", err)
	}
	stub.summary = summaryOfTokens(t, 254)
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 2 || q.markedIDs[0] != later[0].ID {
		t.Fatalf("next automatic pass = %+v, %v; want the later span without waiting out a cooldown", res, err)
	}
}

func TestCompactionScanPositionNeverPassesTheCurrentTask(t *testing.T) {
	t.Parallel()

	// The task is followed by more protected steps than one window holds:
	// the window settles nothing past the task, which becomes claimable once
	// a newer task takes over.
	task := prose(t, "user", "TASK", 400, 100)
	q := newSessionStore(task)
	var filled int64
	for i := 0; filled <= minCompactionReadBytes; i++ {
		rows := fillerExchange(t, i, 4096)
		q.append(rows...)
		filled += payloadBytes(rows[0]) + payloadBytes(rows[1])
	}
	stub := &stubModel{summary: "task condensed"}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 5)
	if res, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || res.Status != StatusNoop {
		t.Fatalf("pass during the task = %+v, %v", res, err)
	}
	if q.scanAfter.Valid {
		t.Fatal("scan position recorded past the current task")
	}
	q.append(prose(t, "user", "NEXT-TASK", 20, 10))
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 1 || q.markedIDs[0] != task.ID {
		t.Fatalf("pass after the next task = %+v, %v; want the old task prompt claimed", res, err)
	}
}

func TestCompactionIneffectiveRollupDoesNotMarkRowsIneffective(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: summaryOfTokens(t, 2000)}
	cfg := machineryConfig(stub, 5)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	rows := []sqlc.ListUncompactedMessagesBySessionRow{prose(t, "user", "NEW", 300, 10), prose(t, "user", "CURRENT", 10, 10)}
	setFusionRowScopeAndTimes(t, cfg, rows)
	q := &fakeQueries{uncompacted: rows, priorLogs: fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))}
	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); !errors.Is(err, ErrIneffectiveSummary) {
		t.Fatalf("rollup = %v, want it rejected as ineffective", err)
	}
	if q.completed.Status != "error" || q.completed.FailureReason != "" {
		t.Fatalf("rollup attempt = %s/%q; its rows were never judged on their own", q.completed.Status, q.completed.FailureReason)
	}
}

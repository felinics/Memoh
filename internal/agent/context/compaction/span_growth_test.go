package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// longHorizonTurn appends one deterministic turn: a user message (every third
// one tiny), sometimes a reasoning-only row or an ask_user exchange, one to
// three exec exchanges and an answer.
func longHorizonTurn(t *testing.T, q *sessionStore, turn int) {
	t.Helper()
	userTokens := 60
	if turn%3 == 0 {
		userTokens = 8
	}
	q.append(prose(t, "user", fmt.Sprintf("U%d", turn), userTokens, userTokens))
	if turn%5 == 0 {
		q.append(reasoningOnlyRow(t))
	}
	if turn%7 == 0 {
		q.append(askUserExchange(t, turn)...)
	}
	for n := 0; n <= turn%3; n++ {
		q.append(execExchange(t, turn*10+n)...)
	}
	q.append(prose(t, "assistant", fmt.Sprintf("A%d", turn), 80, 80))
}

func TestCompactionLongHorizonGrowthStaysBoundedAndOrdered(t *testing.T) {
	t.Parallel()

	const turns = 60
	const maxDrainPasses = 4
	q := newSessionStore()
	stub := &stubModel{}
	svc := newMachineryService(q)
	clock := time.Unix(1_800_000_000, 0)
	svc.nowFn = func() time.Time { return clock }
	cfg := machineryConfig(stub, 3000)
	cfg.HardPressure = true

	rowCost := map[pgtype.UUID]int{}
	proved := map[pgtype.UUID]bool{}
	var passes, failures, committed, recovered int
	started := time.Now()
	var task sqlc.ListUncompactedMessagesBySessionRow
	var taskSteps []sqlc.ListUncompactedMessagesBySessionRow
	for turn := 1; turn <= turns; turn++ {
		switch {
		case turn == 31:
			// One task worked on through turns 31-45 without a new user
			// message: its steps must keep compacting behind the task.
			task = prose(t, "user", "LONG-TASK", 120, 120)
			q.append(task)
			fallthrough
		case turn > 31 && turn <= 45:
			for n := 0; n < 3; n++ {
				step := execExchange(t, turn*10+n)
				taskSteps = append(taskSteps, step...)
				q.append(step...)
			}
		default:
			longHorizonTurn(t, q, turn)
		}
		for _, row := range q.history {
			if _, ok := rowCost[row.ID]; !ok {
				items, _ := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
				rowCost[row.ID] = estimateBytesAsTokens(strings.TrimSpace(renderCandidateEntry(items[0].Record))) + estimateBytesAsTokens(row.Role) + 1
			}
		}
		clock = clock.Add(time.Minute)
		for pass := 0; pass < maxDrainPasses; pass++ {
			passes++
			callsBefore := stub.calls
			stub.summary = summaryOfTokens(t, 120)
			if turn%11 == 0 && pass == 0 {
				stub.summary = summaryOfTokens(t, 6000)
			}
			res, err := svc.RunCompactionSync(context.Background(), cfg)
			if stub.calls-callsBefore > 1 {
				t.Fatalf("turn %d pass %d made %d provider calls", turn, pass, stub.calls-callsBefore)
			}
			assertClaimsContiguous(t, q)
			if stub.calls > callsBefore {
				fresh := 0
				for _, id := range q.markedIDs {
					if !proved[id] {
						fresh += rowCost[id]
					}
				}
				if fresh < minCompactionSpanTokens {
					t.Fatalf("turn %d pass %d sent %d new tokens with proved-ineffective rows; want at least %d", turn, pass, fresh, minCompactionSpanTokens)
				}
			}
			if err != nil {
				if !errors.Is(err, ErrIneffectiveSummary) {
					t.Fatalf("turn %d pass %d: %v", turn, pass, err)
				}
				failures++
				for _, id := range q.markedIDs {
					proved[id] = true
				}
				break
			}
			if res.Status != StatusOK {
				break
			}
			committed++
			for _, id := range q.markedIDs {
				if proved[id] {
					recovered++
					delete(proved, id)
				}
			}
		}
		if turn >= 31 && turn <= 45 && q.logStatuses[q.claims[task.ID]] == "ok" {
			t.Fatalf("turn %d compacted the prompt of the task still being worked on", turn)
		}
		if turn == 45 {
			compactedSteps := 0
			for _, row := range taskSteps {
				if q.logStatuses[q.claims[row.ID]] == "ok" {
					compactedSteps++
				}
			}
			// The newest steps within the target stay raw; the older ones
			// must not.
			if compactedSteps < len(taskSteps)/4 {
				t.Fatalf("only %d of %d steps of the long task compacted while it ran", compactedSteps, len(taskSteps))
			}
		}
	}

	// Every span worth a call has been committed: one more pass finds nothing.
	clock = clock.Add(time.Hour)
	callsBefore := stub.calls
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil || res.Status != StatusNoop || stub.calls != callsBefore {
		t.Fatalf("final pass = %+v, %v with %d calls; want the backlog already drained", res, err, stub.calls-callsBefore)
	}
	if failures == 0 || committed < turns/2 || stub.calls != committed+failures {
		t.Fatalf("committed=%d failures=%d calls=%d", committed, failures, stub.calls)
	}

	// What stays raw outside the kept tail is only what cannot shrink.
	items, _ := itemsFromRows(q.candidateRows())
	kept := splitByTarget(items, cfg.TargetTokens)
	rawTokens := 0
	for _, item := range kept {
		rawTokens += estimateBytesAsTokens(renderCandidateEntry(item.Record))
	}
	elapsed := time.Since(started)
	t.Logf("turns=%d rows=%d passes=%d calls=%d committed=%d ineffective=%d recovered_rows=%d still_proved_rows=%d raw_outside_tail=%d rows/%d tokens elapsed=%v",
		turns, len(q.history), passes, stub.calls, committed, failures, recovered, len(proved), len(kept), rawTokens, elapsed)
	if rawTokens > 20*minCompactionSpanTokens {
		t.Fatalf("raw history outside the kept tail = %d tokens, want it bounded", rawTokens)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("long-horizon run took %v", elapsed)
	}
}

func TestCompactionLongHorizonResidueStaysFlat(t *testing.T) {
	t.Parallel()

	// 120 turns of the production shapes: chat-only turns, an ask_user or a
	// reasoning-only row right after the task, and long tool turns compacted
	// while they run. With every summary effective, what stays raw outside
	// the latest turns must not grow with the turn count.
	q := newSessionStore()
	stub := &stubModel{summary: summaryOfTokens(t, 120)}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, 300)
	cfg.HardPressure = true
	pass := func() {
		calls := stub.calls
		if _, err := svc.RunCompactionSync(context.Background(), cfg); err != nil || stub.calls-calls > 1 {
			t.Fatalf("pass: %v after %d calls", err, stub.calls-calls)
		}
	}
	turnOf := map[pgtype.UUID]int{}
	add := func(turn int, rows ...sqlc.ListUncompactedMessagesBySessionRow) {
		for _, row := range rows {
			turnOf[row.ID] = turn
		}
		q.append(rows...)
	}
	residue := func(turn int) int {
		tokens := 0
		for _, row := range q.candidateRows() {
			items, _ := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{row})
			if turnOf[row.ID] < turn-3 && classifyGroup(items, []int{0}, func(int) bool { return strings.TrimSpace(renderCandidateEntry(items[0].Record)) != "" }) == groupMarkable {
				tokens += estimateBytesAsTokens(renderCandidateEntry(items[0].Record))
			}
		}
		return tokens
	}
	measured := map[int]int{}
	for turn := 1; turn <= 120; turn++ {
		tokens := 50
		if turn%3 == 0 {
			tokens = 4
		}
		add(turn, prose(t, "user", fmt.Sprintf("U%d", turn), tokens, tokens))
		steps := 0
		switch turn % 4 {
		case 2:
			add(turn, askUserExchange(t, turn)...)
			steps = 6
		case 3:
			add(turn, reasoningOnlyRow(t))
			steps = 12
		case 0:
			steps = 16
		}
		for s := 0; s < steps; s++ {
			add(turn, execExchange(t, turn*100+s)...)
			if s%4 == 3 {
				pass()
			}
		}
		add(turn, prose(t, "assistant", fmt.Sprintf("A%d", turn), 60, 60))
		for p := 0; p < 3; p++ {
			pass()
		}
		if turn == 40 || turn == 120 {
			measured[turn] = residue(turn)
		}
	}
	t.Logf("raw tokens older than three turns: %v, provider calls %d", measured, stub.calls)
	if measured[120] > measured[40]+minCompactionSpanTokens {
		t.Fatalf("raw history older than the latest turns grew from %d to %d tokens over 80 turns", measured[40], measured[120])
	}
	assertClaimsContiguous(t, q)
}

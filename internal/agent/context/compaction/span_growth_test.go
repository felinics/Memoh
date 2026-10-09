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
	for turn := 1; turn <= turns; turn++ {
		longHorizonTurn(t, q, turn)
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
				if !errors.Is(err, errIneffectiveSummary) {
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

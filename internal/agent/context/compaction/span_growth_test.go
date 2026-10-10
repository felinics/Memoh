package compaction

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
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
	// the latest turns must not grow with the turn count, whether the kept
	// recent tail is shorter than one turn or spans several.
	for _, target := range []int{300, 2000, 4000} {
		t.Run(strconv.Itoa(target), func(t *testing.T) {
			t.Parallel()
			measured := longHorizonResidue(t, target)
			if measured[120] > measured[40]+minCompactionSpanTokens {
				t.Fatalf("raw history older than the latest turns grew from %d to %d tokens over 80 turns", measured[40], measured[120])
			}
		})
	}
}

// longHorizonResidue runs the turns and reports the raw markable tokens older
// than three turns after turn 40 and turn 120.
func longHorizonResidue(t *testing.T, target int) map[int]int {
	t.Helper()
	q := newSessionStore()
	stub := &stubModel{summary: summaryOfTokens(t, 120)}
	svc := newMachineryService(q)
	cfg := machineryConfig(stub, target)
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
	t.Logf("target %d: raw tokens older than three turns: %v, provider calls %d", target, measured, stub.calls)
	assertClaimsContiguous(t, q)
	return measured
}

// TestCompactionLongHorizonLeavesOnlyRunsBelowTheFloor drives seeded,
// production-shaped sessions: tasks followed by barriers, ask_user, large and
// tiny steps, screenshots read back mid-turn, answers whose usage counts
// reasoning, and occasional manual requests. Once later turns have moved the
// recent tail past that history, every row of it still raw must be one the
// selection cannot claim: a barrier, or a run below the floor that barriers
// or summaries close.
func TestCompactionLongHorizonLeavesOnlyRunsBelowTheFloor(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	inherent, stranded := 0, 0
	t.Run("sessions", func(t *testing.T) {
		for seed := int64(1); seed <= 20; seed++ {
			t.Run(strconv.FormatInt(seed, 10), func(t *testing.T) {
				t.Parallel()
				between, next := longHorizonMixedResidue(t, seed)
				mu.Lock()
				defer mu.Unlock()
				inherent += between
				stranded += next
			})
		}
	})
	t.Logf("20 sessions: %d tokens stay raw between barriers, %d next to summaries", inherent, stranded)
	// Measured on these seeds: 1.13 times the history between barriers. A
	// claim that leaves a closed rest behind, or no rows held with the
	// current task, raises it to 1.64, 2.55 or 4.04.
	if 2*stranded > 3*inherent {
		t.Errorf("%d tokens stay raw next to summaries, %d between barriers: claims leave too much behind", stranded, inherent)
	}
}

// longHorizonMixedResidue runs one seeded session and returns the entry
// tokens of its old history still raw between barriers and next to
// summaries, failing the test on any run that could still be claimed.
func longHorizonMixedResidue(t *testing.T, seed int64) (int, int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // seeded, reproducible test input
	q := newSessionStore()
	stub := &stubModel{summary: summaryOfTokens(t, 100)}
	svc := newMachineryService(q)
	clock := time.Unix(1_800_000_000, 0)
	svc.nowFn = func() time.Time { return clock }
	q.now = func() time.Time { return clock }
	cfg := machineryConfig(stub, []int{800, 4000}[seed%2])
	cfg.HardPressure = true
	manual := cfg
	manual.Manual = true
	pass := func(c TriggerConfig) bool {
		calls := stub.calls
		res, err := svc.RunCompactionSync(context.Background(), c)
		if err != nil && !errors.Is(err, ErrIneffectiveSummary) {
			t.Fatal(err)
		}
		if stub.calls-calls > 1 {
			t.Fatalf("%d summarizer calls in one pass", stub.calls-calls)
		}
		return err == nil && res.Status == StatusOK
	}
	drain := func() {
		for p := 0; p < 4 && pass(cfg); p++ {
		}
	}
	for turn := 1; turn <= 120; turn++ {
		clock = clock.Add(time.Minute)
		q.append(prose(t, "user", fmt.Sprintf("U%d", turn), 4+rng.Intn(80), 5+rng.Intn(60)))
		switch rng.Intn(6) {
		case 0:
			q.append(reasoningOnlyRow(t))
		case 1:
			q.append(askUserExchange(t, turn)...)
		}
		for s := 0; s < []int{0, 0, 1, 3, 8, 20}[rng.Intn(6)]; s++ {
			switch n := turn*100 + s; rng.Intn(10) {
			case 0:
				q.append(reasoningOnlyRow(t))
			case 1:
				q.append(bigStep(t, n)...)
			case 2:
				q.append(screenshotFeedback(t))
			default:
				q.append(execExchange(t, n)...)
			}
			if rng.Intn(3) == 0 {
				drain()
			}
		}
		answer := prose(t, "assistant", fmt.Sprintf("A%d", turn), []int{10, 30, 80, 300, 1500}[rng.Intn(5)], 0)
		if rng.Intn(4) == 0 {
			answer.Usage = []byte(`{"outputTokens":6000}`)
		}
		q.append(answer)
		drain()
		if turn%13 == 0 {
			pass(manual)
		}
	}
	history := len(q.history)
	for turn := 0; turn < 30; turn++ {
		clock = clock.Add(time.Minute)
		q.append(prose(t, "user", fmt.Sprintf("LATER%d", turn), 300, 300), prose(t, "assistant", fmt.Sprintf("LATER%d", turn), 300, 300))
		drain()
	}

	// Each run still raw is bounded on both sides by a barrier, a
	// summary or the session start. Between barriers it is history
	// no claim may join; next to a summary, a claim left it behind.
	items, _ := itemsFromRows(q.history[:history])
	raw, inherent, stranded := 0, 0, 0
	start, left := -1, false
	for i := 0; i <= len(items); i++ {
		summary, claimable := false, false
		if i < len(items) {
			summary = q.logStatuses[q.claims[items[i].ID]] == "ok"
			_, kind := groupCost(items, []int{i})
			claimable = !summary && (kind == groupMarkable || kind == groupOrphanResult && start >= 0)
		}
		if claimable {
			raw++
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			run := items[start:i]
			fresh := 0
			for _, group := range toolExchangeGroups(run) {
				if !provedIneffective(run, group) {
					fresh += markableGroupCost(run, group)
				}
			}
			if fresh >= minCompactionSpanTokens {
				t.Errorf("a run of %d rows worth %d tokens stays raw from row %d", len(run), fresh, start)
			}
			if left || summary || i == len(items) {
				stranded += fresh
			} else {
				inherent += fresh
			}
			start = -1
		}
		left = summary
	}
	t.Logf("seed %d: %d of %d rows raw, %d tokens between barriers, %d next to summaries, %d calls", seed, raw, history, inherent, stranded, stub.calls)
	assertClaimsContiguous(t, q)
	return inherent, stranded
}

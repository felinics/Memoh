package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/chat/timeline"
)

// A Discuss message that arrives while a run folds in place is not part of
// what the run read, so the run's summary does not cover it. When the run
// then fails, its committed steps stay, once, and prove nothing consumed: a
// worker that comes back with no consumed position of its own answers the
// input the run failed on together with the message that arrived during it.
func TestPostgresDiscussRunFailingAfterAFoldLeavesLaterInputUnconsumed(t *testing.T) {
	const steps, arrival = 14, "ARRIVED_DURING_THE_RUN"
	entry := runEntries[slices.IndexFunc(runEntries, func(e runEntry) bool { return e.name == "discuss" })]
	s := newEffectLoop(t, entry)
	f := s.f
	s.arrival, s.arriveAt, s.downAt = arrival, 2, 8
	code, _ := s.run(entry, loopInput, steps)
	if code != "context.budget_unsatisfied" || s.downFrom < 1 || len(s.effects) < s.downAt || len(s.effects) >= steps {
		t.Fatalf("code=%q summaries_before_down=%d effects=%v, want the run to fold, then fail once the summarizer is down", code, s.downFrom, s.effects)
	}
	if len(s.arrived) != 1 {
		t.Fatalf("%d messages arrived during the run, want one", len(s.arrived))
	}
	var arrivedCovered bool
	var toolRows, unmarked int
	if err := f.pool.QueryRow(f.ctx, `SELECT compact_id IS NOT NULL FROM bot_history_messages WHERE session_id=$1 AND source_message_id=$2`, f.sessionID, s.arrived[0].MessageID).Scan(&arrivedCovered); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE role='tool' AND content::text LIKE '%TOOL_RESULT_%'), count(*) FILTER (WHERE metadata->>'discuss_step' IS DISTINCT FROM 'true') FROM bot_history_messages WHERE session_id=$1 AND role IN ('assistant','tool')`, f.sessionID).Scan(&toolRows, &unmarked); err != nil {
		t.Fatal(err)
	}
	if arrivedCovered || toolRows != len(s.effects) || unmarked != 0 {
		t.Fatalf("arrived covered=%v tool rows=%d for %d effects, %d rows without the step mark, want the arrival raw and every step written once as a Discuss step", arrivedCovered, toolRows, len(s.effects), unmarked)
	}

	f.rc = append(f.rc, s.arrived...)
	f.failSummary.Store(false)
	s.arriveAt, s.downAt, s.marker, s.effects = 0, 0, "", nil
	f.restartDriver()
	calls := f.modelCalls.Load()
	f.driver.NotifyRC(f.ctx, f.sessionID, f.rc, f.cfg)
	select {
	case position := <-f.cursor.advanced:
		if position != timeline.ConsumedDiscussCursor(f.rc) {
			t.Fatalf("cursor did not consume the batch: %+v", position)
		}
	case code := <-f.broadcaster.failures:
		t.Fatalf("after the restart the batch failed with %s", code)
	case <-time.After(15 * time.Second):
		t.Fatal("after the restart no run answered the input the failed run left unconsumed")
	}
	request, _ := f.lastRequest.Load().(string)
	if f.modelCalls.Load()-calls != 1 || strings.Count(request, loopInput) != 1 || strings.Count(request, arrival) != 1 || len(s.effects) != 0 {
		t.Fatalf("provider calls=%d input=%d arrival=%d effects=%v, want one answer carrying both inputs once without replaying an effect",
			f.modelCalls.Load()-calls, strings.Count(request, loopInput), strings.Count(request, arrival), s.effects)
	}
}

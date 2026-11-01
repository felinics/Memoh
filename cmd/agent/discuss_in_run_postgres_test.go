package main

import (
	"strings"
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// A clean Discuss run's answer is what proves its batch consumed: the steps
// before it carry the step mark and the answer does not. A worker that comes
// back without the durable position, because the run's advance never landed,
// starts after the answer instead of answering the batch again.
func TestPostgresDiscussAnswerProvesItsBatchConsumed(t *testing.T) {
	const steps = 14
	entry := runEntryNamed("discuss")
	s := newEffectLoop(t, entry)
	f := s.f
	if code, _ := s.run(entry, loopInput, steps); code != "" || f.summaries.Load() == 0 || len(s.effects) != steps {
		t.Fatalf("code=%q summaries=%d effects=%v, want the run to fold and answer", code, f.summaries.Load(), s.effects)
	}
	sources, err := f.messages.ListTurnResponseSourcesWithinBytes(f.ctx, f.sessionID, messagepkg.HistoryPosition{}, 1<<24)
	if err != nil {
		t.Fatal(err)
	}
	responses := timeline.DecodeTurnResponseEntries(sources.Messages)
	var anchor int64
	var answers int
	for _, response := range responses {
		if !response.DiscussStep {
			answers++
			anchor = max(anchor, response.RequestedAtMs)
		}
	}
	if len(responses) < 2 || answers != 1 || responses[len(responses)-1].DiscussStep {
		t.Fatalf("%d replies with %d unmarked, want the steps marked and only the answer, last, unmarked", len(responses), answers)
	}
	if timeline.HasUncoveredExternalEvent(f.rc, timeline.DiscussCursorPosition{SourceCursor: anchor}) {
		t.Fatal("a worker without the durable position would answer the batch again")
	}
}

// A Discuss message that arrives while a run folds in place is not part of
// what the run read, so the run's summary does not cover it. When the run
// then fails, its committed steps stay, once, and prove nothing consumed: a
// worker that comes back with no consumed position of its own answers the
// input the run failed on together with the message that arrived during it.
func TestPostgresDiscussRunFailingAfterAFoldLeavesLaterInputUnconsumed(t *testing.T) {
	const steps, arrival = 14, "ARRIVED_DURING_THE_RUN"
	entry := runEntryNamed("discuss")
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
	if err := f.pool.QueryRow(f.ctx, `SELECT folded FROM (`+historyRowsSQL+`) h WHERE h.source_message_id=$2`, f.sessionID, s.arrived[0].MessageID).Scan(&arrivedCovered); err != nil {
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
	if request := f.answer("after the restart", arrival); strings.Count(request, loopInput) != 1 || len(s.effects) != 0 {
		t.Fatalf("input=%d effects=%v, want the answer to carry the failed run's input once without replaying an effect", strings.Count(request, loopInput), s.effects)
	}
}

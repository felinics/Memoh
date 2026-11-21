package sessionruntime

import (
	"context"
	"testing"
)

type waitingObservation struct{ botID, sessionID string }

func newWaitingObserverManager(t *testing.T, runs *fakeLedger) (*Manager, *[]waitingObservation) {
	t.Helper()
	manager := NewManager(NewMemoryBackend(), Options{Ledger: runs})
	var seen []waitingObservation
	manager.SetWaitingDecisionObserver(func(_ context.Context, botID, sessionID string) {
		seen = append(seen, waitingObservation{botID, sessionID})
	})
	return manager, &seen
}

func TestWaitingDecisionObserverFiresOnceWhenApplied(t *testing.T) {
	runs := newFakeLedger()
	runs.InsertClaimed("run-waiting", "session-waiting", 3, "generation")
	manager, seen := newWaitingObserverManager(t, runs)

	handle := RunHandle{BotID: "bot-waiting", SessionID: "session-waiting", RunID: "run-waiting", FencingToken: 3}
	if err := manager.setWaitingDecision(context.Background(), handle); err != nil {
		t.Fatalf("setWaitingDecision: %v", err)
	}
	if len(*seen) != 1 || (*seen)[0] != (waitingObservation{"bot-waiting", "session-waiting"}) {
		t.Fatalf("observed %+v, want one call for bot-waiting/session-waiting", *seen)
	}
}

func TestWaitingDecisionObserverSkipsUnapplied(t *testing.T) {
	runs := newFakeLedger()
	runs.InsertClaimed("run-stale", "session-stale", 5, "generation")
	manager, seen := newWaitingObserverManager(t, runs)

	stale := RunHandle{BotID: "bot-stale", SessionID: "session-stale", RunID: "run-stale", FencingToken: 4}
	if err := manager.setWaitingDecision(context.Background(), stale); err == nil {
		t.Fatalf("setWaitingDecision with a stale token succeeded")
	}
	if len(*seen) != 0 {
		t.Fatalf("observer fired for an unapplied transition: %+v", *seen)
	}
}

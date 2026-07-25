package postgres

import "testing"

func TestNewRequiresPool(t *testing.T) {
	if _, err := New(nil, exclusiveBotLock{}); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}

func TestDeterministicEventIDIsStable(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	first := deterministicID(botID, "event", "old-event")
	if second := deterministicID(botID, "event", "old-event"); second != first {
		t.Fatalf("deterministic id changed: %s != %s", first, second)
	}
	if other := deterministicID(botID, "event", "other-event"); other == first {
		t.Fatal("different source events received the same id")
	}
}

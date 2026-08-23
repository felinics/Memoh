package client

import "testing"

// TestHandoverSessionSpoolSlot pins the capture-slot handover contract: a
// finished spool inside the byte budget frees its capture slot immediately and
// its lifetime is accounted in bytes; a spool that would exceed the budget
// keeps the slot (the serialized fallback) so total spool disk stays bounded.
func TestHandoverSessionSpoolSlot(t *testing.T) {
	baseline := runtimeSessionSpoolBudgetUsed.Load()

	slotReleased := false
	release := handoverSessionSpoolSlot(1024, func() { slotReleased = true })
	if !slotReleased {
		t.Fatal("in-budget handover did not release the capture slot")
	}
	if got := runtimeSessionSpoolBudgetUsed.Load() - baseline; got != 1024 {
		t.Fatalf("budget usage delta = %d, want 1024", got)
	}
	release()
	release() // must be idempotent
	if got := runtimeSessionSpoolBudgetUsed.Load() - baseline; got != 0 {
		t.Fatalf("budget usage delta after release = %d, want 0", got)
	}

	slotReleased = false
	release = handoverSessionSpoolSlot(runtimeSessionSpoolBudgetBytes+1, func() { slotReleased = true })
	if slotReleased {
		t.Fatal("over-budget handover must keep the capture slot")
	}
	release()
	if !slotReleased {
		t.Fatal("over-budget release must free the retained capture slot")
	}
	if got := runtimeSessionSpoolBudgetUsed.Load() - baseline; got != 0 {
		t.Fatalf("budget usage delta after fallback = %d, want 0", got)
	}

	if got := handoverSessionSpoolSlot(64, nil); got != nil {
		t.Fatal("nil slot release must stay nil")
	}
}

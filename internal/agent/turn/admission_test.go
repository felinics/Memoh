package turn_test

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestAdmitContextEntriesEmptyInput(t *testing.T) {
	decision := turn.AdmitContextEntries(nil, 100)
	if decision.Selected != nil || decision.EstimatedTokens != 0 || decision.ProtectedOverflow {
		t.Fatalf("empty input must decide nothing: %+v", decision)
	}
}

func TestAdmitContextEntriesUnderBudgetSelectsAll(t *testing.T) {
	entries := []turn.AdmissionEntry{{Cost: 10}, {Cost: 20, Pinned: true}, {Cost: 30}}
	decision := turn.AdmitContextEntries(entries, 100)
	if decision.ProtectedOverflow || decision.DroppedEntries != 0 {
		t.Fatalf("under budget must select everything: %+v", decision)
	}
	if decision.EstimatedTokens != 60 || decision.SelectedTokens != 60 {
		t.Fatalf("token accounting wrong: %+v", decision)
	}
	for i, kept := range decision.Selected {
		if !kept {
			t.Fatalf("entry %d must be selected", i)
		}
	}
}

func TestAdmitContextEntriesZeroBudgetAdmitsEverything(t *testing.T) {
	entries := []turn.AdmissionEntry{{Cost: 1 << 20}, {Cost: 1 << 20}}
	decision := turn.AdmitContextEntries(entries, 0)
	if decision.ProtectedOverflow || decision.DroppedEntries != 0 {
		t.Fatalf("zero budget must admit everything: %+v", decision)
	}
}

func TestAdmitContextEntriesDropsOldestKeepsPinnedAndNewest(t *testing.T) {
	entries := []turn.AdmissionEntry{
		{Cost: 1000},
		{Cost: 50, Pinned: true},
		{Cost: 1000},
		{Cost: 100},
	}
	decision := turn.AdmitContextEntries(entries, 300)
	if decision.ProtectedOverflow {
		t.Fatalf("unexpected overflow: %+v", decision)
	}
	want := []bool{false, true, false, true}
	for i := range want {
		if decision.Selected[i] != want[i] {
			t.Fatalf("selection[%d] = %v, want %v (%+v)", i, decision.Selected[i], want[i], decision)
		}
	}
	if decision.SelectedTokens != 150 || decision.DroppedEntries != 2 {
		t.Fatalf("accounting wrong: %+v", decision)
	}
}

func TestAdmitContextEntriesFillStopsAtFirstOverflow(t *testing.T) {
	// The small oldest entry would fit, but the big middle entry breaks the
	// fill first: selection must stay a contiguous suffix.
	entries := []turn.AdmissionEntry{
		{Cost: 10},
		{Cost: 1000},
		{Cost: 100},
	}
	decision := turn.AdmitContextEntries(entries, 200)
	want := []bool{false, false, true}
	for i := range want {
		if decision.Selected[i] != want[i] {
			t.Fatalf("selection[%d] = %v, want %v (%+v)", i, decision.Selected[i], want[i], decision)
		}
	}
}

func TestAdmitContextEntriesProtectedOverflowFailsClosed(t *testing.T) {
	entries := []turn.AdmissionEntry{
		{Cost: 80, Pinned: true},
		{Cost: 80},
	}
	decision := turn.AdmitContextEntries(entries, 100)
	if !decision.ProtectedOverflow {
		t.Fatalf("pinned plus newest over budget must overflow: %+v", decision)
	}
	if decision.Selected != nil {
		t.Fatalf("overflow decision must not offer a selection: %+v", decision)
	}
}

func TestAdmitContextEntriesAllPinnedOverBudgetOverflows(t *testing.T) {
	entries := []turn.AdmissionEntry{
		{Cost: 80, Pinned: true},
		{Cost: 80, Pinned: true},
	}
	decision := turn.AdmitContextEntries(entries, 100)
	if !decision.ProtectedOverflow {
		t.Fatalf("pinned-only set over budget must overflow: %+v", decision)
	}
}

func TestAdmitContextEntriesTrimsLeadingOrphanToolResponses(t *testing.T) {
	entries := []turn.AdmissionEntry{
		{Cost: 1000},
		{Cost: 40, ToolResponse: true},
		{Cost: 40},
		{Cost: 40},
	}
	decision := turn.AdmitContextEntries(entries, 130)
	if decision.ProtectedOverflow {
		t.Fatalf("unexpected overflow: %+v", decision)
	}
	want := []bool{false, false, true, true}
	for i := range want {
		if decision.Selected[i] != want[i] {
			t.Fatalf("selection[%d] = %v, want %v (%+v)", i, decision.Selected[i], want[i], decision)
		}
	}
	if decision.SelectedTokens != 80 {
		t.Fatalf("trimmed orphan must leave the accounting: %+v", decision)
	}
}

func TestAdmitContextEntriesUnderBudgetStillTrimsLeadingOrphanTool(t *testing.T) {
	// A byte-budgeted history load can cut a tool pair in half even when the
	// composed total fits the budget; the window must still not open on the
	// orphaned response.
	entries := []turn.AdmissionEntry{
		{Cost: 40, ToolResponse: true},
		{Cost: 40},
		{Cost: 40},
	}
	decision := turn.AdmitContextEntries(entries, 1000)
	if decision.ProtectedOverflow {
		t.Fatalf("unexpected overflow: %+v", decision)
	}
	want := []bool{false, true, true}
	for i := range want {
		if decision.Selected[i] != want[i] {
			t.Fatalf("selection[%d] = %v, want %v (%+v)", i, decision.Selected[i], want[i], decision)
		}
	}
	if decision.DroppedEntries != 1 {
		t.Fatalf("orphan must count as dropped: %+v", decision)
	}
}

func TestAdmitContextEntriesNewestOrphanToolFailsClosed(t *testing.T) {
	// When the orphan trim reaches the protected newest entry, the window has
	// no valid shape: the decision must fail closed instead of silently
	// admitting an empty or summary-only context.
	entries := []turn.AdmissionEntry{
		{Cost: 1000},
		{Cost: 50, Pinned: true},
		{Cost: 40, ToolResponse: true},
	}
	decision := turn.AdmitContextEntries(entries, 120)
	if !decision.ProtectedOverflow {
		t.Fatalf("newest orphan tool response must overflow, not vanish: %+v", decision)
	}
	if decision.Selected != nil {
		t.Fatalf("overflow decision must not offer a selection: %+v", decision)
	}
}

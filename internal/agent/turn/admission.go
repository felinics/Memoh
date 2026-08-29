package turn

import (
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// The turn port re-exports the agent context ledger's estimation vocabulary
// for the packages the architecture guards keep on this contract:
// chat/timeline and channel may not import agent/context, yet their
// admission decisions must agree with the fragment ledger (CM-EST-001).
// internal/agent/context/fragment stays the authority and the single swap
// point for a real tokenizer; no package may keep a private bytes-per-token
// constant.
const (
	// ContextBytesPerToken is the shared byte-per-token heuristic.
	ContextBytesPerToken = contextfrag.EstimateBytesPerToken
	// DefaultContextCapTokens is the fail-closed admission cap used when no
	// explicit configuration reaches a consumer (CM-ADM-001).
	DefaultContextCapTokens = contextfrag.DefaultAbsoluteCapTokens
)

// EstimateTokensFromBytes converts a byte count to the shared token estimate.
func EstimateTokensFromBytes(n int) int {
	return contextfrag.TokensFromBytes(n)
}

// AdmissionEntry describes one context entry, ordered oldest to newest, for
// the shared pre-materialization admission decision (CM-ADM-001).
type AdmissionEntry struct {
	// Cost is the entry's estimate in shared-estimator tokens.
	Cost int
	// Pinned entries (compaction-artifact summaries) are always selected.
	Pinned bool
	// ToolResponse marks a raw tool-response entry. The selected window must
	// never open on one, because its tool call would fall outside the window.
	ToolResponse bool
}

// AdmissionDecision reports one admission outcome (CM-ADM-002).
type AdmissionDecision struct {
	// Selected holds one keep-flag per input entry. It is nil when the input
	// was empty or the decision is a ProtectedOverflow rejection.
	Selected []bool
	// EstimatedTokens is the pre-selection estimate of the full input.
	EstimatedTokens int
	// SelectedTokens is the estimate of the selected entries.
	SelectedTokens int
	// DroppedEntries counts entries not selected.
	DroppedEntries int
	// ProtectedOverflow is set when the protected set alone (pinned entries
	// plus the newest raw entry) exceeds the budget, or when the newest raw
	// entry is a tool response that cannot open a valid window. Callers must
	// fail closed instead of materializing (CM-ADM-002).
	ProtectedOverflow bool
}

// AdmitContextEntries makes the deterministic admission decision shared by
// the channel-side composer and the agent-side re-admission (CM-ADM-002):
// pinned entries are always kept, the newest raw entry is protected, older
// raw entries fill a contiguous recent window that stops at the first entry
// that does not fit, and the window never opens on an orphaned tool
// response. A budget of zero or less admits everything; the orphan trim
// still applies because a byte-budgeted history load can cut a tool pair in
// half even when the composed total fits the budget.
func AdmitContextEntries(entries []AdmissionEntry, budgetTokens int) AdmissionDecision {
	decision := AdmissionDecision{}
	if len(entries) == 0 {
		return decision
	}
	total := 0
	for i := range entries {
		total += entries[i].Cost
	}
	decision.EstimatedTokens = total

	selected := make([]bool, len(entries))
	used := 0
	if budgetTokens <= 0 || total <= budgetTokens {
		for i := range selected {
			selected[i] = true
		}
		used = total
	} else {
		for i := range entries {
			if entries[i].Pinned {
				selected[i] = true
				used += entries[i].Cost
			}
		}
		newest := newestRawEntry(entries)
		if newest >= 0 {
			selected[newest] = true
			used += entries[newest].Cost
		}
		if used > budgetTokens {
			decision.SelectedTokens = used
			decision.ProtectedOverflow = true
			return decision
		}
		// Fill a contiguous recent window: stop at the first entry that does
		// not fit so selection stays a suffix (plus pinned entries) and
		// remains deterministic.
		for i := newest - 1; i >= 0; i-- {
			if selected[i] {
				continue
			}
			if used+entries[i].Cost > budgetTokens {
				break
			}
			selected[i] = true
			used += entries[i].Cost
		}
	}

	// Never open the raw window on an orphaned tool response. When the trim
	// reaches the protected newest entry the window has no valid shape left:
	// fail closed instead of silently admitting an empty or summary-only
	// context.
	protected := newestRawEntry(entries)
	for i := range entries {
		if !selected[i] || entries[i].Pinned {
			continue
		}
		if !entries[i].ToolResponse {
			break
		}
		if i == protected {
			decision.SelectedTokens = used
			decision.ProtectedOverflow = true
			return decision
		}
		selected[i] = false
		used -= entries[i].Cost
	}

	decision.Selected = selected
	decision.SelectedTokens = used
	for i := range selected {
		if !selected[i] {
			decision.DroppedEntries++
		}
	}
	return decision
}

// newestRawEntry returns the index of the newest non-pinned entry, or -1
// when every entry is pinned.
func newestRawEntry(entries []AdmissionEntry) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if !entries[i].Pinned {
			return i
		}
	}
	return -1
}

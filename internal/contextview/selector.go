package contextview

import contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"

type FragmentSelector struct{}

func (*FragmentSelector) ProfileFor(intent contextfrag.Intent) IntentProfile {
	if intent != contextfrag.IntentRunConfigPreProvider {
		return IntentProfile{Intent: intent}
	}
	return IntentProfile{
		Intent:          intent,
		MustKeepSlots:   []contextfrag.Slot{contextfrag.SlotSystem, contextfrag.SlotCurrentUser},
		SlotTrustFloors: map[contextfrag.Slot]contextfrag.TrustLevel{contextfrag.SlotSystem: contextfrag.TrustWorkspace},
	}
}

func (*FragmentSelector) Select(frags []contextfrag.ContextFrag, profile IntentProfile, budget BudgetEnvelope) SelectionResult {
	collected := len(frags)
	selected, gated := applyTrustGate(frags, profile)
	selected, superseded := resolveConflictGroups(selected)
	selected, budgetDropped, edits, warnings := enforceFragBudgets(selected, profile)
	selected, exchangeDropped, exchangeEdits := applyToolExchangePolicy(selected, budget.ToolExchange)

	result := SelectionResult{
		Selected: selected,
		Edited:   append(edits, exchangeEdits...),
		Warnings: warnings,
		Summary: SelectionSummary{
			TotalCollected: collected,
			TotalSelected:  len(selected),
		},
	}
	for _, frag := range gated {
		result.recordDrop(frag, "trust_gate:"+string(frag.Slot)+"_requires_"+string(profile.SlotTrustFloors[frag.Slot]))
	}
	for _, loser := range superseded {
		result.recordDrop(loser.frag, "precedence:superseded_by_"+loser.winnerID)
	}
	for _, dropped := range budgetDropped {
		result.recordDrop(dropped.frag, dropped.reason)
	}
	for _, frag := range exchangeDropped {
		result.recordDrop(frag, toolExchangeDropReason)
	}
	result.Summary.TotalDropped = len(result.Dropped)
	return result
}

func (r *SelectionResult) recordDrop(frag contextfrag.ContextFrag, reason string) {
	r.Dropped = append(r.Dropped, frag)
	r.Summary.DropReasons = append(r.Summary.DropReasons, DropRecord{
		FragID: frag.ID,
		Ref:    frag.Ref,
		Reason: reason,
	})
}

func applyTrustGate(frags []contextfrag.ContextFrag, profile IntentProfile) ([]contextfrag.ContextFrag, []contextfrag.ContextFrag) {
	if len(profile.SlotTrustFloors) == 0 {
		return frags, nil
	}
	kept := make([]contextfrag.ContextFrag, 0, len(frags))
	var dropped []contextfrag.ContextFrag
	for _, frag := range frags {
		floor, ok := profile.SlotTrustFloors[frag.Slot]
		if ok && contextfrag.TrustRank(frag.Trust) < contextfrag.TrustRank(floor) {
			dropped = append(dropped, frag)
			continue
		}
		kept = append(kept, frag)
	}
	return kept, dropped
}

type conflictLoser struct {
	frag     contextfrag.ContextFrag
	winnerID string
}

func resolveConflictGroups(frags []contextfrag.ContextFrag) ([]contextfrag.ContextFrag, []conflictLoser) {
	winners := make(map[string]int)
	for i, frag := range frags {
		if frag.ConflictKey == "" {
			continue
		}
		current, ok := winners[frag.ConflictKey]
		if !ok || conflictBeats(frag, frags[current]) {
			winners[frag.ConflictKey] = i
		}
	}
	if len(winners) == 0 {
		return frags, nil
	}
	kept := make([]contextfrag.ContextFrag, 0, len(frags))
	var losers []conflictLoser
	for i, frag := range frags {
		if frag.ConflictKey != "" && winners[frag.ConflictKey] != i {
			losers = append(losers, conflictLoser{frag: frag, winnerID: frags[winners[frag.ConflictKey]].ID})
			continue
		}
		kept = append(kept, frag)
	}
	return kept, losers
}

func conflictBeats(challenger, incumbent contextfrag.ContextFrag) bool {
	if challengerScope, incumbentScope := challenger.Scope.SpecificityRank(), incumbent.Scope.SpecificityRank(); challengerScope != incumbentScope {
		return challengerScope > incumbentScope
	}
	if challengerTrust, incumbentTrust := contextfrag.TrustRank(challenger.Trust), contextfrag.TrustRank(incumbent.Trust); challengerTrust != incumbentTrust {
		return challengerTrust > incumbentTrust
	}
	return true
}

func isMustKeepFrag(frag contextfrag.ContextFrag, profile IntentProfile) bool {
	if frag.Budget.Overflow == contextfrag.OverflowKeep {
		return true
	}
	for _, slot := range profile.MustKeepSlots {
		if frag.Slot == slot {
			return true
		}
	}
	return false
}

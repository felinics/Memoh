package contextfrag

import (
	"strings"
	"sync"
)

const MetadataContextLifecycleKey = "context_lifecycle"

// LifecycleSnapshot is the durable, content-light audit for one provider
// context build. It intentionally excludes manifest items and payloads.
type LifecycleSnapshot struct {
	Version            int                 `json:"version"`
	View               ManifestView        `json:"view,omitempty"`
	Counts             ManifestCounts      `json:"counts"`
	AssistantMessageID string              `json:"assistant_message_id,omitempty"`
	SelectionDecisions []SelectionDecision `json:"selection_decisions,omitempty"`
	Selection          SelectionTrace      `json:"selection"`
	BudgetPlan         *ContextBudgetPlan  `json:"budget_plan,omitempty"`
	CachePlan          *CachePlan          `json:"cache_plan,omitempty"`
	Mutations          []MutationRecord    `json:"mutations,omitempty"`
	FinalInputHash     string              `json:"final_input_hash,omitempty"`
}

// LifecycleHolder shares the latest audit across copied RunConfig values.
type LifecycleHolder struct {
	mu       sync.RWMutex
	snapshot LifecycleSnapshot
	ledger   *MutationLedger
	set      bool
}

func NewLifecycleHolder() *LifecycleHolder {
	return &LifecycleHolder{}
}

func (h *LifecycleHolder) SetManifest(manifest Manifest) {
	if h == nil {
		return
	}
	next := BuildLifecycleSnapshot(manifest)
	h.mu.Lock()
	next.AssistantMessageID = h.snapshot.AssistantMessageID
	h.snapshot = next
	h.ledger = manifest.Mutations
	h.set = true
	h.mu.Unlock()
}

func (h *LifecycleHolder) SetAssistantMessageID(messageID string) {
	if h == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	h.mu.Lock()
	h.snapshot.AssistantMessageID = messageID
	h.mu.Unlock()
}

func (h *LifecycleHolder) Snapshot() (LifecycleSnapshot, bool) {
	if h == nil {
		return LifecycleSnapshot{}, false
	}
	h.mu.RLock()
	snapshot := cloneLifecycleSnapshot(h.snapshot)
	ledger := h.ledger
	ok := h.set
	h.mu.RUnlock()
	if !ok {
		return LifecycleSnapshot{}, false
	}
	if ledger != nil {
		snapshot.Mutations = ledger.Records()
		snapshot.FinalInputHash = ledger.FinalInputHash()
	}
	return snapshot, true
}

func BuildLifecycleSnapshot(manifest Manifest) LifecycleSnapshot {
	snapshot := LifecycleSnapshot{
		Version:            1,
		View:               manifest.View,
		Counts:             manifest.Counts,
		SelectionDecisions: append([]SelectionDecision(nil), manifest.SelectionDecisions...),
	}
	if manifest.Selection != nil {
		snapshot.Selection = cloneSelectionTrace(*manifest.Selection)
	}
	if manifest.BudgetPlan != nil {
		plan := *manifest.BudgetPlan
		snapshot.BudgetPlan = &plan
	}
	if manifest.CachePlan != nil {
		plan := *manifest.CachePlan
		snapshot.CachePlan = &plan
	}
	if manifest.Mutations != nil {
		snapshot.Mutations = manifest.Mutations.Records()
		snapshot.FinalInputHash = manifest.Mutations.FinalInputHash()
	}
	return snapshot
}

func cloneLifecycleSnapshot(snapshot LifecycleSnapshot) LifecycleSnapshot {
	snapshot.SelectionDecisions = append([]SelectionDecision(nil), snapshot.SelectionDecisions...)
	snapshot.Selection = cloneSelectionTrace(snapshot.Selection)
	snapshot.Mutations = append([]MutationRecord(nil), snapshot.Mutations...)
	if snapshot.BudgetPlan != nil {
		plan := *snapshot.BudgetPlan
		snapshot.BudgetPlan = &plan
	}
	if snapshot.CachePlan != nil {
		plan := *snapshot.CachePlan
		snapshot.CachePlan = &plan
	}
	return snapshot
}

func cloneSelectionTrace(selection SelectionTrace) SelectionTrace {
	if len(selection.DropReasons) > 0 {
		reasons := make(map[string]int, len(selection.DropReasons))
		for reason, count := range selection.DropReasons {
			reasons[reason] = count
		}
		selection.DropReasons = reasons
	}
	return selection
}

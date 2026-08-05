package contextfrag

import (
	"encoding/json"
	"strings"
	"sync"
)

const MetadataContextLifecycleKey = "context_lifecycle"

// LifecycleSnapshotFromMetadata extracts the persisted lifecycle snapshot
// from a message metadata JSON payload, reporting whether one was present.
func LifecycleSnapshotFromMetadata(raw []byte) (LifecycleSnapshot, bool) {
	if len(raw) == 0 {
		return LifecycleSnapshot{}, false
	}
	var metadata struct {
		ContextLifecycle *LifecycleSnapshot `json:"context_lifecycle"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.ContextLifecycle == nil {
		return LifecycleSnapshot{}, false
	}
	return cloneLifecycleSnapshot(*metadata.ContextLifecycle), true
}

const maxMemoryRecallTraceRefs = 32

// LifecycleSnapshot is the durable, content-light audit for one provider
// context build. It intentionally excludes manifest items and payloads.
type LifecycleSnapshot struct {
	Version                   int                 `json:"version"`
	View                      ManifestView        `json:"view,omitempty"`
	Counts                    ManifestCounts      `json:"counts"`
	Breakdown                 []KindBreakdown     `json:"breakdown,omitempty"`
	TrustBreakdown            []TrustBreakdown    `json:"trust_breakdown,omitempty"`
	ToolDefs                  []ToolDefAccounting `json:"tool_defs,omitempty"`
	AssistantMessageID        string              `json:"assistant_message_id,omitempty"`
	SelectionDecisions        []SelectionDecision `json:"selection_decisions,omitempty"`
	Selection                 SelectionTrace      `json:"selection"`
	BudgetPlan                *ContextBudgetPlan  `json:"budget_plan,omitempty"`
	StablePrefixHash          string              `json:"stable_prefix_hash,omitempty"`
	StableMessageCount        int                 `json:"stable_message_count,omitempty"`
	StablePrefixTokenEstimate int                 `json:"stable_prefix_token_estimate,omitempty"`
	CacheReadTokens           int                 `json:"cache_read_tokens"`
	CacheWriteTokens          int                 `json:"cache_write_tokens"`
	CacheUsage                []CacheUsageRecord  `json:"cache_usage,omitempty"`
	CacheComparison           *CacheComparison    `json:"cache_comparison,omitempty"`
	Mutations                 []MutationRecord    `json:"mutations,omitempty"`
	FinalInputHash            string              `json:"final_input_hash,omitempty"`
	Model                     string              `json:"model,omitempty"`
	ClientType                string              `json:"client_type,omitempty"`
	LoopSelectionMode         string              `json:"loop_selection_mode,omitempty"`
	Steps                     []StepSnapshot      `json:"steps,omitempty"`
	MemoryRecall              *MemoryRecallTrace  `json:"memory_recall,omitempty"`
}

type MemoryRecallTrace struct {
	ProviderID     string                  `json:"provider_id"`
	MemoryVersion  string                  `json:"memory_version,omitempty"`
	CacheState     string                  `json:"cache_state"`
	RetrievalMode  string                  `json:"retrieval_mode,omitempty"`
	FallbackReason string                  `json:"fallback_reason,omitempty"`
	Query          MemoryRecallQueryTrace  `json:"query"`
	Result         MemoryRecallResultTrace `json:"result"`
}

type MemoryRecallQueryTrace struct {
	Source         string `json:"source"`
	RecentMessages int    `json:"recent_messages"`
	Truncated      bool   `json:"truncated"`
}

type MemoryRecallResultTrace struct {
	Count        int      `json:"count"`
	Refs         []string `json:"refs,omitempty"`
	ContextBytes int      `json:"context_bytes"`
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

func (h *LifecycleHolder) SetMemoryRecall(trace MemoryRecallTrace) {
	if h == nil {
		return
	}
	trace.Result.Refs = normalizeMemoryRecallRefs(trace.Result.Refs)
	h.mu.Lock()
	h.snapshot.Version = 1
	h.snapshot.MemoryRecall = cloneMemoryRecallTrace(&trace)
	h.set = true
	h.mu.Unlock()
}

func (h *LifecycleHolder) SetManifest(manifest Manifest) {
	if h == nil {
		return
	}
	next := BuildLifecycleSnapshot(manifest)
	h.mu.Lock()
	next.AssistantMessageID = h.snapshot.AssistantMessageID
	next.MemoryRecall = cloneMemoryRecallTrace(h.snapshot.MemoryRecall)
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
		snapshot.CacheUsage = ledger.CacheUsageRecords()
		snapshot.CacheReadTokens, snapshot.CacheWriteTokens = cacheUsageTotals(snapshot.CacheUsage)
		snapshot.Model, snapshot.ClientType = ledger.ModelInfo()
		snapshot.LoopSelectionMode = ledger.LoopSelectionMode()
		snapshot.Steps = ledger.StepSnapshots()
	}
	return snapshot, true
}

func BuildLifecycleSnapshot(manifest Manifest) LifecycleSnapshot {
	snapshot := LifecycleSnapshot{
		Version:            1,
		View:               manifest.View,
		Counts:             manifest.Counts,
		Breakdown:          append([]KindBreakdown(nil), manifest.Breakdown...),
		TrustBreakdown:     append([]TrustBreakdown(nil), manifest.TrustBreakdown...),
		ToolDefs:           append([]ToolDefAccounting(nil), manifest.ToolDefs...),
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
		snapshot.StablePrefixHash = manifest.CachePlan.StablePrefixHash
		snapshot.StableMessageCount = manifest.CachePlan.StableMessageCount
		snapshot.StablePrefixTokenEstimate = manifest.CachePlan.StablePrefixTokenEstimate
	}
	if manifest.Mutations != nil {
		snapshot.Mutations = manifest.Mutations.Records()
		snapshot.FinalInputHash = manifest.Mutations.FinalInputHash()
		snapshot.CacheUsage = manifest.Mutations.CacheUsageRecords()
		snapshot.CacheReadTokens, snapshot.CacheWriteTokens = cacheUsageTotals(snapshot.CacheUsage)
		snapshot.Model, snapshot.ClientType = manifest.Mutations.ModelInfo()
		snapshot.LoopSelectionMode = manifest.Mutations.LoopSelectionMode()
		snapshot.Steps = manifest.Mutations.StepSnapshots()
	}
	return snapshot
}

func cloneLifecycleSnapshot(snapshot LifecycleSnapshot) LifecycleSnapshot {
	snapshot.Breakdown = append([]KindBreakdown(nil), snapshot.Breakdown...)
	snapshot.TrustBreakdown = append([]TrustBreakdown(nil), snapshot.TrustBreakdown...)
	snapshot.ToolDefs = append([]ToolDefAccounting(nil), snapshot.ToolDefs...)
	snapshot.SelectionDecisions = append([]SelectionDecision(nil), snapshot.SelectionDecisions...)
	snapshot.Selection = cloneSelectionTrace(snapshot.Selection)
	snapshot.Mutations = append([]MutationRecord(nil), snapshot.Mutations...)
	snapshot.CacheUsage = append([]CacheUsageRecord(nil), snapshot.CacheUsage...)
	snapshot.Steps = cloneStepSnapshots(snapshot.Steps)
	snapshot.MemoryRecall = cloneMemoryRecallTrace(snapshot.MemoryRecall)
	if snapshot.CacheComparison != nil {
		comparison := *snapshot.CacheComparison
		snapshot.CacheComparison = &comparison
	}
	if snapshot.BudgetPlan != nil {
		plan := *snapshot.BudgetPlan
		snapshot.BudgetPlan = &plan
	}
	return snapshot
}

func cloneMemoryRecallTrace(trace *MemoryRecallTrace) *MemoryRecallTrace {
	if trace == nil {
		return nil
	}
	out := *trace
	out.Result.Refs = append([]string(nil), trace.Result.Refs...)
	return &out
}

func normalizeMemoryRecallRefs(refs []string) []string {
	out := make([]string, 0, min(len(refs), maxMemoryRecallTraceRefs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
		if len(out) == maxMemoryRecallTraceRefs {
			break
		}
	}
	return out
}

func cacheUsageTotals(records []CacheUsageRecord) (readTokens, writeTokens int) {
	for _, record := range records {
		readTokens += record.CacheReadTokens
		writeTokens += record.CacheWriteTokens
	}
	return readTokens, writeTokens
}

func cloneStepSnapshots(steps []StepSnapshot) []StepSnapshot {
	if len(steps) == 0 {
		return nil
	}
	out := make([]StepSnapshot, len(steps))
	for i, step := range steps {
		out[i] = cloneStepSnapshot(step)
	}
	return out
}

func cloneSelectionTrace(selection SelectionTrace) SelectionTrace {
	if selection.DropReasons != nil {
		reasons := make(map[string]int, len(selection.DropReasons))
		for reason, count := range selection.DropReasons {
			reasons[reason] = count
		}
		selection.DropReasons = reasons
	}
	return selection
}

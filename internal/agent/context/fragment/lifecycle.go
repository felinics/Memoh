package contextfrag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const MetadataContextLifecycleKey = "context_lifecycle"

// LifecycleSnapshotVersion is the durable snapshot schema version. Version 2
// flattened the version-1 cache_plan object into stable_prefix_hash,
// stable_message_count, and stable_prefix_token_estimate and added the
// enriched attempt, breakdown, and memory recall audits.
const LifecycleSnapshotVersion = 2

// LifecycleSnapshotFromMetadata extracts the persisted lifecycle snapshot
// from a message metadata JSON payload, reporting whether one was present.
// LifecycleSnapshotRawFromMetadata extracts the nested lifecycle snapshot
// exactly as persisted, validating that it decodes. Persistence paths must
// carry these raw bytes: decoding into the typed snapshot drops fields a
// newer schema may have written.
func LifecycleSnapshotRawFromMetadata(raw []byte) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var metadata struct {
		ContextLifecycle json.RawMessage `json:"context_lifecycle"`
	}
	if json.Unmarshal(raw, &metadata) != nil || len(metadata.ContextLifecycle) == 0 {
		return nil, false
	}
	if _, err := DecodeLifecycleSnapshot(metadata.ContextLifecycle); err != nil {
		return nil, false
	}
	return metadata.ContextLifecycle, true
}

// StampLifecycleAssistantMessageID sets assistant_message_id on a raw
// snapshot without losing fields the current schema does not know about.
func StampLifecycleAssistantMessageID(raw json.RawMessage, messageID string) (json.RawMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	fields["assistant_message_id"] = messageID
	return json.Marshal(fields)
}

func LifecycleSnapshotFromMetadata(raw []byte) (LifecycleSnapshot, bool) {
	if len(raw) == 0 {
		return LifecycleSnapshot{}, false
	}
	var metadata struct {
		ContextLifecycle json.RawMessage `json:"context_lifecycle"`
	}
	if json.Unmarshal(raw, &metadata) != nil || len(metadata.ContextLifecycle) == 0 {
		return LifecycleSnapshot{}, false
	}
	snapshot, err := DecodeLifecycleSnapshot(metadata.ContextLifecycle)
	if err != nil {
		return LifecycleSnapshot{}, false
	}
	return snapshot, true
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
	RunTrace                  *RunTrace           `json:"run_trace,omitempty"`
	// Fragments lists the injected fragments of the run, bounded by the prompt
	// rather than the conversation; their texts live in the content store.
	Fragments []FragmentRef `json:"fragments,omitempty"`
}

// RunTrace is the fixed-size timing and usage rollup of one run: request
// count, wall clock per lane, and provider tokens. TTFT belongs to the first
// request; DecodeMs and DecodeOutputTokens cover only requests that reported
// both a first token and output tokens, so a throughput reading stays honest.
type RunTrace struct {
	Steps              int   `json:"steps"`
	ToolCalls          int   `json:"tool_calls,omitempty"`
	StartedAtMS        int64 `json:"started_at_ms,omitempty"`
	EndedAtMS          int64 `json:"ended_at_ms,omitempty"`
	LLMMs              int64 `json:"llm_ms,omitempty"`
	ToolMs             int64 `json:"tool_ms,omitempty"`
	TTFTMs             int64 `json:"ttft_ms,omitempty"`
	DecodeMs           int64 `json:"decode_ms,omitempty"`
	DecodeOutputTokens int   `json:"decode_output_tokens,omitempty"`
	InputTokens        int   `json:"input_tokens,omitempty"`
	CachedInputTokens  int   `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens   int   `json:"cache_write_tokens,omitempty"`
	OutputTokens       int   `json:"output_tokens,omitempty"`
	ReasoningTokens    int   `json:"reasoning_tokens,omitempty"`
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
	mu            sync.RWMutex
	snapshot      LifecycleSnapshot
	ledger        *MutationLedger
	runTrace      func() *RunTrace
	set           bool
	textSink      FragmentTextSink
	recordedTexts map[string]struct{}
	// textHashes maps a fragment's content hash to the store key of the text
	// this run recorded for it.
	textHashes map[string]string
}

func NewLifecycleHolder() *LifecycleHolder {
	return &LifecycleHolder{}
}

// SetRunTraceSource registers the run rollup read at snapshot time, so the
// terminal write sees the complete trace regardless of manifest replacement.
func (h *LifecycleHolder) SetRunTraceSource(source func() *RunTrace) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.runTrace = source
	h.mu.Unlock()
}

func (h *LifecycleHolder) SetMemoryRecall(trace MemoryRecallTrace) {
	if h == nil {
		return
	}
	trace.Result.Refs = normalizeMemoryRecallRefs(trace.Result.Refs)
	h.mu.Lock()
	h.snapshot.Version = LifecycleSnapshotVersion
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
	runTrace := h.runTrace
	ok := h.set
	for i := range snapshot.Fragments {
		snapshot.Fragments[i].TextHash = h.textHashes[snapshot.Fragments[i].ContentHash]
	}
	h.mu.RUnlock()
	if !ok {
		return LifecycleSnapshot{}, false
	}
	if runTrace != nil {
		if trace := runTrace(); trace != nil {
			copied := *trace
			snapshot.RunTrace = &copied
		}
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
		Version:            LifecycleSnapshotVersion,
		View:               manifest.View,
		Counts:             manifest.Counts,
		Breakdown:          append([]KindBreakdown(nil), manifest.Breakdown...),
		TrustBreakdown:     append([]TrustBreakdown(nil), manifest.TrustBreakdown...),
		ToolDefs:           append([]ToolDefAccounting(nil), manifest.ToolDefs...),
		SelectionDecisions: append([]SelectionDecision(nil), manifest.SelectionDecisions...),
		Fragments:          fragmentRefs(manifest.Items),
	}
	if manifest.Selection != nil {
		snapshot.Selection = cloneSelectionTrace(*manifest.Selection)
	}
	snapshot.Selection.Trimmed, snapshot.Selection.DropReasonTokens = rollUpSelectionDecisions(manifest.SelectionDecisions)
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

// rollUpSelectionDecisions reduces the per-fragment audit to the bounded
// facts the UI shows: how many fragments were trimmed, and how many tokens each
// drop reason cost. Blank reasons share the selector's "unknown" bucket.
func rollUpSelectionDecisions(decisions []SelectionDecision) (trimmed int, dropReasonTokens map[string]int) {
	for _, decision := range decisions {
		switch decision.Decision {
		case DecisionTrimmed:
			trimmed++
		case DecisionDropped:
			reason := strings.TrimSpace(decision.Reason)
			if reason == "" {
				reason = "unknown"
			}
			if dropReasonTokens == nil {
				dropReasonTokens = make(map[string]int, 4)
			}
			dropReasonTokens[reason] += decision.TokenEstimate
		}
	}
	return trimmed, dropReasonTokens
}

// Summary returns the snapshot without its per-fragment selection decisions,
// the only part whose size grows with the conversation. Everything a list or
// status reader needs is already rolled up on the remaining fields.
func (s LifecycleSnapshot) Summary() LifecycleSnapshot {
	s.SelectionDecisions = nil
	return s
}

// RowCopy is the copy stamped on every assistant row of a run. The run row
// keeps the trace, the fragment refs, and the tool definition hashes; the
// per-row copy exists for readers of sessions that predate the run table
// and carries only the bounded accounting.
func (s LifecycleSnapshot) RowCopy() LifecycleSnapshot {
	s = s.Summary()
	s.RunTrace = nil
	s.Fragments = nil
	if len(s.ToolDefs) > 0 {
		defs := make([]ToolDefAccounting, len(s.ToolDefs))
		copy(defs, s.ToolDefs)
		for i := range defs {
			defs[i].ContentHash = ""
		}
		s.ToolDefs = defs
	}
	return s
}

// DecodeLifecycleSnapshot parses a durable snapshot of any persisted version.
// Version-1 rows carried a nested cache_plan object; its fields map onto the
// flattened version-2 fields and the decoded snapshot is normalized to the
// current version.
func DecodeLifecycleSnapshot(raw []byte) (LifecycleSnapshot, error) {
	var compat struct {
		LifecycleSnapshot
		LegacyCachePlan *CachePlan `json:"cache_plan"`
	}
	if err := json.Unmarshal(raw, &compat); err != nil {
		return LifecycleSnapshot{}, err
	}
	snapshot := compat.LifecycleSnapshot
	if compat.LegacyCachePlan != nil {
		if snapshot.StablePrefixHash == "" {
			snapshot.StablePrefixHash = compat.LegacyCachePlan.StablePrefixHash
		}
		if snapshot.StableMessageCount == 0 {
			snapshot.StableMessageCount = compat.LegacyCachePlan.StableMessageCount
		}
		if snapshot.StablePrefixTokenEstimate == 0 {
			snapshot.StablePrefixTokenEstimate = compat.LegacyCachePlan.StablePrefixTokenEstimate
		}
	}
	if snapshot.Version <= 0 {
		return LifecycleSnapshot{}, fmt.Errorf("lifecycle snapshot is unversioned (version %d)", snapshot.Version)
	}
	// Known past versions normalize to the current schema. Versions from the
	// future keep their version number, but this typed view necessarily drops
	// fields the current schema does not know: it is a read projection only.
	// Any path that persists a snapshot again must carry the raw bytes (see
	// LifecycleSnapshotRawFromMetadata and StampLifecycleAssistantMessageID).
	if snapshot.Version < LifecycleSnapshotVersion {
		snapshot.Version = LifecycleSnapshotVersion
	}
	return snapshot, nil
}

func cloneLifecycleSnapshot(snapshot LifecycleSnapshot) LifecycleSnapshot {
	snapshot.Breakdown = append([]KindBreakdown(nil), snapshot.Breakdown...)
	snapshot.TrustBreakdown = append([]TrustBreakdown(nil), snapshot.TrustBreakdown...)
	snapshot.ToolDefs = append([]ToolDefAccounting(nil), snapshot.ToolDefs...)
	snapshot.SelectionDecisions = append([]SelectionDecision(nil), snapshot.SelectionDecisions...)
	snapshot.Fragments = append([]FragmentRef(nil), snapshot.Fragments...)
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
	selection.DropReasons = cloneCounts(selection.DropReasons)
	selection.DropReasonTokens = cloneCounts(selection.DropReasonTokens)
	return selection
}

func cloneCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return nil
	}
	out := make(map[string]int, len(counts))
	for key, value := range counts {
		out[key] = value
	}
	return out
}

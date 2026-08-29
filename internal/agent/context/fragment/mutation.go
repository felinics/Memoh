package contextfrag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sync"
)

// MutationKind identifies an observable provider-context decision or a
// post-render mutation that changes the provider payload.
type MutationKind string

const (
	MutationBeforeModelCallHook   MutationKind = "before_model_call_hook"
	MutationBackgroundSummary     MutationKind = "background_summary"
	MutationMidTaskPrune          MutationKind = "mid_task_prune"
	MutationLoopStepReselection   MutationKind = "loop_step_reselection"
	MutationInjectedMessage       MutationKind = "injected_message"
	MutationContextViewFallback   MutationKind = "context_view_fallback"
	MutationContextBudgetFailure  MutationKind = "context_budget_failure"
	MutationContextBudgetDisabled MutationKind = "context_budget_disabled"
	MutationCapabilityGate        MutationKind = "capability_gate"
	MutationReadMedia             MutationKind = "read_media"
	MutationMidStreamRetry        MutationKind = "mid_stream_retry"
)

// MutationRecord is one ledger entry describing a post-render mutation.
type MutationRecord struct {
	Kind   MutationKind `json:"kind"`
	Detail string       `json:"detail,omitempty"`
}

type CacheUsageRecord struct {
	Attempt            int `json:"attempt,omitempty"`
	StepIndex          int `json:"step_index"`
	NoCacheTokens      int `json:"no_cache_tokens,omitempty"`
	CacheReadTokens    int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens   int `json:"cache_write_tokens,omitempty"`
	CacheWrite5mTokens int `json:"cache_write_5m_tokens,omitempty"`
	CacheWrite1hTokens int `json:"cache_write_1h_tokens,omitempty"`
}

// Loop-selection modes identify which policy governed mid-run context.
const (
	LoopSelectionSuffixOnly       = "suffix_only"
	LoopSelectionLegacyPrune      = "legacy_prune"
	LoopSelectionSuffixOnlyShadow = "suffix_only_shadow"
)

// Provider-attempt reselection outcomes distinguish an applied decision from
// the same decision observed under shadow mode. The empty value means no
// reselector governed that attempt.
const (
	ReselectionOutcomeUnchanged  = "unchanged"
	ReselectionOutcomeApplied    = "applied"
	ReselectionOutcomeWouldApply = "would_apply"
	ReselectionOutcomeFailed     = "failed"
	ReselectionOutcomeWouldFail  = "would_fail"
)

// StepSnapshot records the content-light audit for one provider attempt.
type StepSnapshot struct {
	Attempt              int            `json:"attempt,omitempty"`
	StepIndex            int            `json:"step_index"`
	PostPrepareInputHash string         `json:"post_prepare_input_hash,omitempty"`
	ReselectionOutcome   string         `json:"reselection_outcome,omitempty"`
	ReselectionApplied   bool           `json:"reselection_applied,omitempty"`
	Dropped              int            `json:"dropped,omitempty"`
	Truncated            int            `json:"truncated,omitempty"`
	DropReasons          map[string]int `json:"drop_reasons,omitempty"`
}

// MutationLedger records post-render changes, provider-attempt metadata, and
// content-light step snapshots. All methods are nil-safe.
type MutationLedger struct {
	mu                sync.Mutex
	records           []MutationRecord
	cacheUsage        []CacheUsageRecord
	finalInputHash    string
	steps             []StepSnapshot
	attempt           int
	model             string
	clientType        string
	loopSelectionMode string
}

func NewMutationLedger() *MutationLedger {
	return &MutationLedger{}
}

func (l *MutationLedger) Record(kind MutationKind, detail string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, MutationRecord{Kind: kind, Detail: detail})
}

func (l *MutationLedger) Records() []MutationRecord {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]MutationRecord, len(l.records))
	copy(out, l.records)
	return out
}

func (l *MutationLedger) RecordCacheUsage(record CacheUsageRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record.Attempt = l.attempt
	l.cacheUsage = append(l.cacheUsage, record)
}

func (l *MutationLedger) CacheUsageRecords() []CacheUsageRecord {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.cacheUsage) == 0 {
		return nil
	}
	out := make([]CacheUsageRecord, len(l.cacheUsage))
	copy(out, l.cacheUsage)
	return out
}

func (l *MutationLedger) SetFinalInputHash(hash string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finalInputHash = hash
}

func (l *MutationLedger) FinalInputHash() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.finalInputHash
}

// AppendStepSnapshot stamps a provider-step audit with the current attempt.
func (l *MutationLedger) AppendStepSnapshot(snapshot StepSnapshot) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot.Attempt = l.attempt
	l.steps = append(l.steps, cloneStepSnapshot(snapshot))
}

func (l *MutationLedger) StepSnapshots() []StepSnapshot {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.steps) == 0 {
		return nil
	}
	out := make([]StepSnapshot, len(l.steps))
	for i, snapshot := range l.steps {
		out[i] = cloneStepSnapshot(snapshot)
	}
	return out
}

func cloneStepSnapshot(snapshot StepSnapshot) StepSnapshot {
	if snapshot.DropReasons == nil {
		return snapshot
	}
	dropReasons := make(map[string]int, len(snapshot.DropReasons))
	for reason, count := range snapshot.DropReasons {
		dropReasons[reason] = count
	}
	snapshot.DropReasons = dropReasons
	return snapshot
}

// AdvanceAttempt starts a new retry attempt and returns its number.
func (l *MutationLedger) AdvanceAttempt() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempt++
	return l.attempt
}

func (l *MutationLedger) SetModelInfo(model, clientType string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.model = model
	l.clientType = clientType
}

func (l *MutationLedger) ModelInfo() (string, string) {
	if l == nil {
		return "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.model, l.clientType
}

func (l *MutationLedger) SetLoopSelectionMode(mode string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.loopSelectionMode = mode
}

func (l *MutationLedger) LoopSelectionMode() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loopSelectionMode
}

// ProviderInputHash hashes the assembled provider payload deterministically.
func ProviderInputHash(system string, messages any) string {
	return ProviderPayloadHash(system, messages, nil)
}

// ProviderPayloadHash includes tool definitions when supplied while preserving
// the legacy hash shape when tools are empty. It is a content fingerprint only;
// envelope decisions price payloads through ProviderEnvelopeTokens.
func ProviderPayloadHash(system string, messages any, tools any) string {
	tools = nilIfEmptyValue(tools)
	raw, err := json.Marshal(struct {
		System   string `json:"system"`
		Messages any    `json:"messages"`
		Tools    any    `json:"tools,omitempty"`
	}{System: system, Messages: messages, Tools: tools})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nilIfEmptyValue(value any) any {
	if value == nil {
		return nil
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		if rv.IsNil() || rv.Len() == 0 {
			return nil
		}
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
	}
	return value
}

func (l *MutationLedger) MarshalJSON() ([]byte, error) {
	model, clientType := l.ModelInfo()
	return json.Marshal(struct {
		Records           []MutationRecord   `json:"records,omitempty"`
		CacheUsage        []CacheUsageRecord `json:"cache_usage,omitempty"`
		FinalInputHash    string             `json:"final_input_hash,omitempty"`
		Steps             []StepSnapshot     `json:"steps,omitempty"`
		Model             string             `json:"model,omitempty"`
		ClientType        string             `json:"client_type,omitempty"`
		LoopSelectionMode string             `json:"loop_selection_mode,omitempty"`
	}{
		Records:           l.Records(),
		CacheUsage:        l.CacheUsageRecords(),
		FinalInputHash:    l.FinalInputHash(),
		Steps:             l.StepSnapshots(),
		Model:             model,
		ClientType:        clientType,
		LoopSelectionMode: l.LoopSelectionMode(),
	})
}

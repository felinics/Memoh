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
	MutationInjectedMessage       MutationKind = "injected_message"
	MutationContextViewFallback   MutationKind = "context_view_fallback"
	MutationContextBudgetFailure  MutationKind = "context_budget_failure"
	MutationContextBudgetDisabled MutationKind = "context_budget_disabled"
	MutationCapabilityGate        MutationKind = "capability_gate"
	MutationReadMedia             MutationKind = "read_media"
)

// MutationRecord is one ledger entry describing a post-render mutation.
type MutationRecord struct {
	Kind   MutationKind `json:"kind"`
	Detail string       `json:"detail,omitempty"`
}

// MutationLedger records post-render changes and the final provider input
// hash. It intentionally carries no provider-attempt or step state.
type MutationLedger struct {
	mu             sync.Mutex
	records        []MutationRecord
	finalInputHash string
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

// ProviderInputHash hashes the assembled provider payload deterministically.
func ProviderInputHash(system string, messages any) string {
	hash, _ := ProviderPayloadHashAndBytes(system, messages, nil)
	return hash
}

// ProviderPayloadHashAndBytes includes tool definitions when supplied while
// preserving the legacy hash shape when tools are empty.
func ProviderPayloadHashAndBytes(system string, messages any, tools any) (string, int) {
	tools = nilIfEmptyValue(tools)
	raw, err := json.Marshal(struct {
		System   string `json:"system"`
		Messages any    `json:"messages"`
		Tools    any    `json:"tools,omitempty"`
	}{System: system, Messages: messages, Tools: tools})
	if err != nil {
		return "", 0
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), len(raw)
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
	return json.Marshal(struct {
		Records        []MutationRecord `json:"records,omitempty"`
		FinalInputHash string           `json:"final_input_hash,omitempty"`
	}{
		Records:        l.Records(),
		FinalInputHash: l.FinalInputHash(),
	})
}

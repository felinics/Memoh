package contextfrag

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMutationLedgerNilSafe(t *testing.T) {
	t.Parallel()

	var ledger *MutationLedger
	ledger.Record(MutationBackgroundSummary, "bytes=10")
	ledger.SetFinalInputHash("abc")
	if ledger.Records() != nil || ledger.FinalInputHash() != "" {
		t.Fatal("nil ledger must be inert")
	}
}

func TestMutationLedgerRecordsInOrder(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.Record(MutationBackgroundSummary, "bytes=10")
	ledger.Record(MutationMidTaskPrune, "pruned=3")
	ledger.SetFinalInputHash("hash-1")

	records := ledger.Records()
	if len(records) != 2 ||
		records[0].Kind != MutationBackgroundSummary ||
		records[1].Kind != MutationMidTaskPrune {
		t.Fatalf("records = %#v", records)
	}
	if ledger.FinalInputHash() != "hash-1" {
		t.Fatalf("final hash = %q", ledger.FinalInputHash())
	}
}

func TestProviderInputHashDeterministic(t *testing.T) {
	t.Parallel()

	first := ProviderInputHash("system", []string{"a", "b"})
	second := ProviderInputHash("system", []string{"a", "b"})
	changed := ProviderInputHash("system", []string{"a", "c"})
	if first == "" || first != second {
		t.Fatal("hash must be deterministic and non-empty")
	}
	if first == changed {
		t.Fatal("hash must track payload changes")
	}
}

func TestProviderPayloadHashTracksTools(t *testing.T) {
	t.Parallel()

	withoutTools := ProviderPayloadHash("system", []string{"a"}, nil)
	withEmptyTools := ProviderPayloadHash("system", []string{"a"}, []string(nil))
	withAllocatedEmptyTools := ProviderPayloadHash("system", []string{"a"}, []string{})
	withTools := ProviderPayloadHash("system", []string{"a"}, []string{"tool"})
	if withoutTools != withEmptyTools || withoutTools != withAllocatedEmptyTools {
		t.Fatal("empty tools must preserve the legacy payload identity")
	}
	if withoutTools != ProviderInputHash("system", []string{"a"}) {
		t.Fatal("ProviderInputHash must stay the tool-less payload identity")
	}
	if withTools == withoutTools {
		t.Fatal("tool definitions must participate in provider payload identity")
	}
}

func TestMutationLedgerAdvanceAttemptReturnsNewAttemptNumber(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	if got := ledger.AdvanceAttempt(); got != 1 {
		t.Fatalf("AdvanceAttempt() = %d, want 1", got)
	}
	if got := ledger.AdvanceAttempt(); got != 2 {
		t.Fatalf("AdvanceAttempt() = %d, want 2", got)
	}
	var nilLedger *MutationLedger
	if got := nilLedger.AdvanceAttempt(); got != 0 {
		t.Fatalf("nil ledger AdvanceAttempt() = %d, want 0", got)
	}
}

func TestMutationLedgerAppendStepSnapshotStampsCurrentAttempt(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.AppendStepSnapshot(StepSnapshot{StepIndex: 0, PostPrepareInputHash: "hash-0"})
	ledger.AdvanceAttempt()
	ledger.AppendStepSnapshot(StepSnapshot{StepIndex: 0, PostPrepareInputHash: "hash-1"})

	steps := ledger.StepSnapshots()
	if len(steps) != 2 {
		t.Fatalf("steps = %#v, want 2", steps)
	}
	if steps[0].Attempt != 0 {
		t.Fatalf("steps[0].Attempt = %d, want 0", steps[0].Attempt)
	}
	if steps[1].Attempt != 1 {
		t.Fatalf("steps[1].Attempt = %d, want 1 after AdvanceAttempt", steps[1].Attempt)
	}
}

func TestMutationLedgerStepSnapshotsOwnDropReasons(t *testing.T) {
	t.Parallel()

	dropReasons := map[string]int{"budget": 1}
	ledger := NewMutationLedger()
	ledger.AppendStepSnapshot(StepSnapshot{StepIndex: 0, DropReasons: dropReasons})

	dropReasons["budget"] = 2
	first := ledger.StepSnapshots()
	if first[0].DropReasons["budget"] != 1 {
		t.Fatalf("stored drop reasons = %#v, want defensive copy of appended snapshot", first[0].DropReasons)
	}

	first[0].DropReasons["budget"] = 3
	second := ledger.StepSnapshots()
	if second[0].DropReasons["budget"] != 1 {
		t.Fatalf("stored drop reasons = %#v after returned snapshot mutation", second[0].DropReasons)
	}
}

func TestMutationLedgerAdvanceAttemptStampsCacheUsageRecords(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.RecordCacheUsage(CacheUsageRecord{StepIndex: 0})
	ledger.AdvanceAttempt()
	ledger.RecordCacheUsage(CacheUsageRecord{StepIndex: 0})

	records := ledger.CacheUsageRecords()
	if len(records) != 2 {
		t.Fatalf("records = %#v, want 2", records)
	}
	if records[0].Attempt != 0 || records[1].Attempt != 1 {
		t.Fatalf("attempts = %d, %d, want 0, 1", records[0].Attempt, records[1].Attempt)
	}
}

func TestMutationLedgerModelInfo(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.SetModelInfo("claude-x", "anthropic-messages")
	model, clientType := ledger.ModelInfo()
	if model != "claude-x" || clientType != "anthropic-messages" {
		t.Fatalf("ModelInfo() = (%q, %q), want (claude-x, anthropic-messages)", model, clientType)
	}
}

func TestMutationLedgerLoopSelectionMode(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.SetLoopSelectionMode(LoopSelectionSuffixOnly)
	if got := ledger.LoopSelectionMode(); got != LoopSelectionSuffixOnly {
		t.Fatalf("LoopSelectionMode() = %q, want %q", got, LoopSelectionSuffixOnly)
	}
}

func TestMutationLedgerLoopSelectionModeSuffixOnlyShadow(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.SetLoopSelectionMode(LoopSelectionSuffixOnlyShadow)
	if got := ledger.LoopSelectionMode(); got != LoopSelectionSuffixOnlyShadow {
		t.Fatalf("LoopSelectionMode() = %q, want %q", got, LoopSelectionSuffixOnlyShadow)
	}
	if LoopSelectionSuffixOnlyShadow != "suffix_only_shadow" {
		t.Fatalf("LoopSelectionSuffixOnlyShadow = %q, want suffix_only_shadow", LoopSelectionSuffixOnlyShadow)
	}
}

func TestMutationLedgerStepAttemptModelNilSafe(t *testing.T) {
	t.Parallel()

	var ledger *MutationLedger
	ledger.AppendStepSnapshot(StepSnapshot{StepIndex: 0})
	ledger.AdvanceAttempt()
	ledger.RecordCacheUsage(CacheUsageRecord{StepIndex: 0})
	ledger.SetModelInfo("m", "c")
	ledger.SetLoopSelectionMode(LoopSelectionLegacyPrune)
	if ledger.StepSnapshots() != nil || ledger.CacheUsageRecords() != nil {
		t.Fatal("nil ledger step/cache records must be nil")
	}
	if model, clientType := ledger.ModelInfo(); model != "" || clientType != "" {
		t.Fatalf("nil ledger ModelInfo() = (%q, %q), want empty", model, clientType)
	}
	if got := ledger.LoopSelectionMode(); got != "" {
		t.Fatalf("nil ledger LoopSelectionMode() = %q, want empty", got)
	}
}

func TestMutationLedgerMarshalJSONIncludesAttemptAudit(t *testing.T) {
	t.Parallel()

	ledger := NewMutationLedger()
	ledger.Record(MutationMidTaskPrune, "truncated=2")
	ledger.SetFinalInputHash("final-hash")
	ledger.SetModelInfo("claude-x", "anthropic-messages")
	ledger.SetLoopSelectionMode(LoopSelectionSuffixOnly)
	ledger.AppendStepSnapshot(StepSnapshot{
		StepIndex:            0,
		PostPrepareInputHash: "step-hash-0",
		ReselectionOutcome:   ReselectionOutcomeApplied,
		ReselectionApplied:   true,
		Dropped:              3,
		DropReasons:          map[string]int{"budget": 3},
	})
	ledger.RecordCacheUsage(CacheUsageRecord{StepIndex: 0, CacheReadTokens: 11})
	ledger.AdvanceAttempt()
	ledger.AppendStepSnapshot(StepSnapshot{StepIndex: 1, PostPrepareInputHash: "step-hash-1", Truncated: 2})

	raw, err := json.Marshal(ledger)
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	for _, want := range []string{
		"mid_task_prune", "truncated=2", "final-hash",
		`"model":"claude-x"`, `"client_type":"anthropic-messages"`, `"loop_selection_mode":"suffix_only"`,
		`"step_index":0`, "step-hash-0", `"reselection_outcome":"applied"`, `"reselection_applied":true`, `"dropped":3`, `"budget":3`, `"cache_read_tokens":11`,
		`"step_index":1`, "step-hash-1", `"attempt":1`, `"truncated":2`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("ledger JSON missing %q:\n%s", want, raw)
		}
	}
}

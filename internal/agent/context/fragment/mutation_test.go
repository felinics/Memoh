package contextfrag

import "testing"

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

	withoutTools, withoutToolsBytes := ProviderPayloadHashAndBytes("system", []string{"a"}, nil)
	withEmptyTools, withEmptyToolsBytes := ProviderPayloadHashAndBytes("system", []string{"a"}, []string(nil))
	withAllocatedEmptyTools, withAllocatedEmptyToolsBytes := ProviderPayloadHashAndBytes("system", []string{"a"}, []string{})
	withTools, withToolsBytes := ProviderPayloadHashAndBytes("system", []string{"a"}, []string{"tool"})
	if withoutTools != withEmptyTools || withoutToolsBytes != withEmptyToolsBytes ||
		withoutTools != withAllocatedEmptyTools || withoutToolsBytes != withAllocatedEmptyToolsBytes {
		t.Fatal("empty tools must preserve the legacy payload identity")
	}
	if withTools == withoutTools || withToolsBytes <= withoutToolsBytes {
		t.Fatal("tool definitions must participate in provider payload identity")
	}
}

package native

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestApplyBeforeModelCallAppendContextRecordsMutation(t *testing.T) {
	ledger := contextfrag.NewMutationLedger()
	cfg := RunConfig{ContextMutations: ledger}
	out := applyBeforeModelCallAppendContext(cfg, "extra guidance")
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(out.Messages))
	}
	records := ledger.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationBeforeModelCallHook {
		t.Fatalf("records = %+v, want one %s", records, contextfrag.MutationBeforeModelCallHook)
	}
}

func TestApplyBeforeModelCallAppendContextPreservesPostViewMemoryManifest(t *testing.T) {
	cfg := postViewMemoryConfig()
	out := applyBeforeModelCallAppendContext(cfg, "extra guidance")

	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want memory payload plus hook append", len(out.Messages))
	}
	assertSinglePostViewMemoryFrag(t, out)
}

func TestApplyBeforeModelCallAppendContextEmptyIsNoop(t *testing.T) {
	ledger := contextfrag.NewMutationLedger()
	cfg := RunConfig{ContextMutations: ledger}
	out := applyBeforeModelCallAppendContext(cfg, "  ")
	if len(out.Messages) != 0 || len(ledger.Records()) != 0 {
		t.Fatalf("expected noop, got messages=%d records=%d", len(out.Messages), len(ledger.Records()))
	}
}

func TestApplyStepHookAppendContextRecordsMutation(t *testing.T) {
	ledger := contextfrag.NewMutationLedger()
	p := &sdk.GenerateParams{}
	out := applyStepHookAppendContext(p, ledger, 2, "extra guidance")
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(out.Messages))
	}
	records := ledger.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationBeforeModelCallHook {
		t.Fatalf("records = %+v, want one %s", records, contextfrag.MutationBeforeModelCallHook)
	}
	if !strings.Contains(records[0].Detail, "step=2") {
		t.Fatalf("detail = %q, want to include step=2", records[0].Detail)
	}
}

func TestApplyStepHookAppendContextEmptyIsNoop(t *testing.T) {
	ledger := contextfrag.NewMutationLedger()
	p := &sdk.GenerateParams{}
	out := applyStepHookAppendContext(p, ledger, 1, "  ")
	if len(out.Messages) != 0 || len(ledger.Records()) != 0 {
		t.Fatalf("expected noop, got messages=%d records=%d", len(out.Messages), len(ledger.Records()))
	}
}

func postViewMemoryConfig() RunConfig {
	msg := sdk.UserMessage("remembered fact")
	frag := contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:         "memory.recall",
		Message:    msg,
		Kind:       contextfrag.KindMemoryRecall,
		Slot:       contextfrag.SlotAfterHistoryBeforeCurrent,
		CacheClass: contextfrag.CacheNever,
		Trust:      contextfrag.TrustExternal,
	})
	return RunConfig{
		Messages:         []sdk.Message{msg},
		ContextFrags:     []contextfrag.ContextFrag{frag},
		ContextManifest:  contextfrag.BuildManifest([]contextfrag.ContextFrag{frag}),
		ContextMutations: contextfrag.NewMutationLedger(),
	}
}

func assertSinglePostViewMemoryFrag(t *testing.T, cfg RunConfig) {
	t.Helper()
	if len(cfg.ContextFrags) != 1 || cfg.ContextFrags[0].Kind != contextfrag.KindMemoryRecall {
		t.Fatalf("context frags = %#v, want one memory recall", cfg.ContextFrags)
	}
	if cfg.ContextManifest.Counts.Fragments != 1 || cfg.ContextManifest.Counts.Messages != 1 {
		t.Fatalf("manifest counts = %#v, want unchanged post-view accounting", cfg.ContextManifest.Counts)
	}
	for _, item := range cfg.ContextManifest.Items {
		if item.Kind == contextfrag.KindConversationEvent {
			t.Fatalf("rendered memory was reclassified as conversation event: %#v", cfg.ContextManifest.Items)
		}
	}
}

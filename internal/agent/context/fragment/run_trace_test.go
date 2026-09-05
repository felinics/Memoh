package contextfrag_test

import (
	"encoding/json"
	"strings"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestLifecycleHolderSnapshotReadsRunTraceSource(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.BuildManifest(nil))
	trace := contextfrag.RunTrace{Steps: 2, ToolCalls: 1, LLMMs: 3_400, ToolMs: 900, TTFTMs: 420, DecodeMs: 2_100, DecodeOutputTokens: 210, OutputTokens: 210, InputTokens: 5_000, CachedInputTokens: 4_000}
	holder.SetRunTraceSource(func() *contextfrag.RunTrace { return &trace })

	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.RunTrace == nil || *snapshot.RunTrace != trace {
		t.Fatalf("snapshot run trace = %#v (ok=%v)", snapshot.RunTrace, ok)
	}
	holder.SetManifest(contextfrag.BuildManifest(nil))
	if snapshot, _ := holder.Snapshot(); snapshot.RunTrace == nil {
		t.Fatalf("run trace source lost across manifest replacement")
	}
	summary := snapshot.Summary()
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"run_trace":{"steps":2,"tool_calls":1,"llm_ms":3400,"tool_ms":900,"ttft_ms":420,"decode_ms":2100,"decode_output_tokens":210,"input_tokens":5000,"cached_input_tokens":4000,"output_tokens":210}`) {
		t.Fatalf("summary JSON run trace = %s", raw)
	}
}

func TestLifecycleHolderSnapshotOmitsAbsentRunTrace(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.BuildManifest(nil))
	holder.SetRunTraceSource(func() *contextfrag.RunTrace { return nil })
	snapshot, _ := holder.Snapshot()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "run_trace") {
		t.Fatalf("absent run trace serialized: %s", raw)
	}
}

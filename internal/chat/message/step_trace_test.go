package message

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStepTraceFromMetadataDecodesTypedAndJSONValues(t *testing.T) {
	t.Parallel()

	trace := StepTraceMetadata{
		Version:        StepTraceVersion,
		StepIndex:      2,
		StartedAtMS:    1_000,
		FirstTokenAtMS: 1_250,
		EndedAtMS:      3_000,
		FinishReason:   "tool-calls",
		Usage:          &StepTraceUsage{InputTokens: 120, CachedInputTokens: 80, OutputTokens: 15, ReasoningTokens: 5},
	}
	if got := StepTraceFromMetadata(map[string]any{StepTraceMetadataKey: trace}); !reflect.DeepEqual(got, &trace) {
		t.Fatalf("typed decode = %#v", got)
	}

	raw, err := json.Marshal(trace)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded map[string]any
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := StepTraceFromMetadata(map[string]any{StepTraceMetadataKey: loaded})
	if got == nil || got.StepIndex != 2 || got.FirstTokenAtMS != 1_250 || got.Usage == nil || got.Usage.CachedInputTokens != 80 {
		t.Fatalf("json decode = %#v", got)
	}
	if string(raw) != `{"version":1,"step_index":2,"started_at_ms":1000,"first_token_at_ms":1250,"ended_at_ms":3000,"finish_reason":"tool-calls","usage":{"input_tokens":120,"cached_input_tokens":80,"output_tokens":15,"reasoning_tokens":5}}` {
		t.Fatalf("wire shape = %s", raw)
	}
}

func TestStepTraceFromMetadataRejectsForeignVersions(t *testing.T) {
	t.Parallel()

	if got := StepTraceFromMetadata(map[string]any{StepTraceMetadataKey: map[string]any{"version": 99, "started_at_ms": 1}}); got != nil {
		t.Fatalf("foreign version decoded: %#v", got)
	}
	if got := StepTraceFromMetadata(nil); got != nil {
		t.Fatalf("nil metadata decoded: %#v", got)
	}
}

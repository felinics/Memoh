package capabilities

import (
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/models"
)

func ptrBool(b bool) *bool { return &b }
func ptrInt(i int) *int    { return &i }

func TestDerive_AdaptiveOpus(t *testing.T) {
	// claude-opus-4-8: adaptive + xhigh + max.
	caps := derive(litellmEntry{
		SupportsReasoning:            ptrBool(true),
		SupportsAdaptiveThinking:     ptrBool(true),
		SupportsXHighReasoningEffort: ptrBool(true),
		SupportsMaxReasoningEffort:   ptrBool(true),
		SupportsVision:               ptrBool(true),
		SupportsFunctionCalling:      ptrBool(true),
		MaxInputTokens:               ptrInt(1000000),
	})
	if caps.ThinkingMode != models.ThinkingModeAdaptive {
		t.Fatalf("thinking mode = %q, want adaptive", caps.ThinkingMode)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(caps.EffortLevels, want) {
		t.Fatalf("effort levels = %v, want %v", caps.EffortLevels, want)
	}
}

func TestDerive_ToggleGPT5Minimal(t *testing.T) {
	// gpt-5: reasoning + minimal, none/xhigh explicitly false, not adaptive.
	caps := derive(litellmEntry{
		SupportsReasoning:              ptrBool(true),
		SupportsNoneReasoningEffort:    ptrBool(false),
		SupportsMinimalReasoningEffort: ptrBool(true),
		SupportsXHighReasoningEffort:   ptrBool(false),
	})
	if caps.ThinkingMode != models.ThinkingModeToggle {
		t.Fatalf("thinking mode = %q, want toggle", caps.ThinkingMode)
	}
	want := []string{"minimal", "low", "medium", "high"}
	if !reflect.DeepEqual(caps.EffortLevels, want) {
		t.Fatalf("effort levels = %v, want %v", caps.EffortLevels, want)
	}
}

func TestDerive_LowExplicitlyUnsupported(t *testing.T) {
	// gpt-5.5-pro: reasoning + xhigh, but none/minimal/low explicitly false.
	// low must be dropped from the base so the UI/resolver never offers it.
	caps := derive(litellmEntry{
		SupportsReasoning:              ptrBool(true),
		SupportsNoneReasoningEffort:    ptrBool(false),
		SupportsMinimalReasoningEffort: ptrBool(false),
		SupportsLowReasoningEffort:     ptrBool(false),
		SupportsXHighReasoningEffort:   ptrBool(true),
	})
	want := []string{"medium", "high", "xhigh"}
	if !reflect.DeepEqual(caps.EffortLevels, want) {
		t.Fatalf("effort levels = %v, want %v", caps.EffortLevels, want)
	}
}

func TestDerive_PlainReasoning(t *testing.T) {
	// o3: reasoning only → toggle with base tiers.
	caps := derive(litellmEntry{SupportsReasoning: ptrBool(true)})
	if caps.ThinkingMode != models.ThinkingModeToggle {
		t.Fatalf("thinking mode = %q", caps.ThinkingMode)
	}
	want := []string{"low", "medium", "high"}
	if !reflect.DeepEqual(caps.EffortLevels, want) {
		t.Fatalf("effort levels = %v, want %v", caps.EffortLevels, want)
	}
}

func TestDerive_ExplicitNoReasoning(t *testing.T) {
	caps := derive(litellmEntry{SupportsReasoning: ptrBool(false)})
	if caps.ThinkingMode != models.ThinkingModeNone {
		t.Fatalf("thinking mode = %q, want none", caps.ThinkingMode)
	}
	if caps.EffortLevels != nil {
		t.Fatalf("effort levels should be nil, got %v", caps.EffortLevels)
	}
}

func TestDerive_SilentRegistryIsUnknown(t *testing.T) {
	caps := derive(litellmEntry{SupportsVision: ptrBool(true)})
	if caps.ThinkingMode != "" {
		t.Fatalf("thinking mode should be unknown (empty), got %q", caps.ThinkingMode)
	}
	if caps.Vision == nil || !*caps.Vision {
		t.Fatalf("vision should be filled")
	}
}

func TestDerive_FileInputMirrorsPDFField(t *testing.T) {
	yes, no := true, false

	caps := derive(litellmEntry{SupportsPDFInput: &yes})
	if caps.FileInput == nil || !*caps.FileInput {
		t.Fatalf("expected FileInput true, got %+v", caps.FileInput)
	}

	caps = derive(litellmEntry{SupportsPDFInput: &no})
	if caps.FileInput == nil || *caps.FileInput {
		t.Fatalf("expected FileInput false, got %+v", caps.FileInput)
	}

	// Registry silence must stay nil so callers can tell "unknown" apart
	// from an explicit false and leave hand-maintained tokens untouched.
	caps = derive(litellmEntry{})
	if caps.FileInput != nil {
		t.Fatalf("expected FileInput nil on silence, got %v", *caps.FileInput)
	}
}

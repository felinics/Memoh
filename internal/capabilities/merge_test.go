package capabilities

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/reasoning"
)

func TestMergeTakesEachSourceForWhatItKnows(t *testing.T) {
	t.Parallel()

	on := true
	base := Capabilities{
		ThinkingMode:     "toggle",
		EffortLevels:     []string{"low", "high"},
		ReasoningDialect: reasoning.DialectTier,
	}
	off := Capabilities{
		ReasoningOffSupport: reasoning.OffSupportAccepted,
		ReasoningDefaultOn:  &on,
	}

	merged, conflicts := Merge(base, off)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	// The wire shape stays with the source that models it.
	if merged.ReasoningDialect != reasoning.DialectTier || len(merged.EffortLevels) != 2 {
		t.Errorf("the base's wire shape should survive: %+v", merged)
	}
	// Off-ability and the default state come from the gateway.
	if merged.ReasoningOffSupport != reasoning.OffSupportAccepted {
		t.Errorf("off support = %q, want accepted", merged.ReasoningOffSupport)
	}
	if !reasoning.DefaultOn(merged.ReasoningDefaultOn) {
		t.Error("default-on should come from the gateway")
	}
}

// A second source earns its place by disagreeing. Resolving silently would throw
// away the only signal it exists to produce.
func TestMergeReportsDisagreementAboutOffAbility(t *testing.T) {
	t.Parallel()

	base := Capabilities{ReasoningOffSupport: reasoning.OffSupportRejected}
	off := Capabilities{ReasoningOffSupport: reasoning.OffSupportAccepted}

	merged, conflicts := Merge(base, off)
	if len(conflicts) != 1 {
		t.Fatalf("a disagreement should be reported, got %v", conflicts)
	}
	if !strings.Contains(conflicts[0], "off-ability") {
		t.Errorf("the conflict should name the field: %q", conflicts[0])
	}
	// The gateway wins the value because it has to make the request work, but the
	// conflict is still surfaced for a human to judge.
	if merged.ReasoningOffSupport != reasoning.OffSupportAccepted {
		t.Errorf("off support = %q, want the gateway's answer", merged.ReasoningOffSupport)
	}
}

func TestMergeSilenceIsNotAnOpinion(t *testing.T) {
	t.Parallel()

	base := Capabilities{ReasoningOffSupport: reasoning.OffSupportRejected}

	// An absent second source must not erase what the first knew: OpenRouter only
	// covers models it resells, and absence there says nothing about capability.
	merged, conflicts := Merge(base, Capabilities{})
	if len(conflicts) != 0 {
		t.Fatalf("silence is not a conflict: %v", conflicts)
	}
	if merged.ReasoningOffSupport != reasoning.OffSupportRejected {
		t.Errorf("off support = %q, want the base's answer preserved", merged.ReasoningOffSupport)
	}
	if merged.ReasoningDefaultOn != nil {
		t.Error("an unknown default state should stay unknown")
	}
}

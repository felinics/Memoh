package models

import (
	"testing"

	"github.com/felinics/memoh/internal/reasoning"
)

// A switch without tiers resolves to the enabled token, which no OpenAI-shaped
// endpoint accepts as a reasoning_effort. The field is left out so the
// provider's default stands; off still travels as "none" and tiers as declared.
func TestOpenAIEffortLeavesOutTheEnabledToken(t *testing.T) {
	t.Parallel()

	for _, clientType := range []ClientType{ClientTypeOpenAICompletions, ClientTypeOpenAIResponses} {
		on := SDKModelConfig{ClientType: string(clientType), ReasoningConfig: &ReasoningConfig{Active: true, Effort: reasoning.EffortEnabled, OffEffort: ReasoningEffortNone}}
		if effort, ok := ReasoningEffortParam(on); ok {
			t.Fatalf("%s: effort %q sent, want the field left out", clientType, effort)
		}
		off := SDKModelConfig{ClientType: string(clientType), ReasoningConfig: &ReasoningConfig{Disabled: true, OffEffort: ReasoningEffortNone}}
		if effort, ok := ReasoningEffortParam(off); !ok || effort != ReasoningEffortNone {
			t.Fatalf("%s: off = %q, %v, want none", clientType, effort, ok)
		}
		tier := SDKModelConfig{ClientType: string(clientType), ReasoningConfig: &ReasoningConfig{Active: true, Effort: ReasoningEffortHigh}}
		if effort, ok := ReasoningEffortParam(tier); !ok || effort != ReasoningEffortHigh {
			t.Fatalf("%s: tier = %q, %v, want high", clientType, effort, ok)
		}
	}
}

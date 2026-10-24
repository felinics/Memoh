package compaction

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

// A continuation preserves the run's prefix without requiring cache usage
// reports. Model identity and the summary's window/output limits still apply.
func TestContinuationRequestEligibility(t *testing.T) {
	base := sdk.Request{Model: "m", System: "system", Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("history ", 4000))}}
	cfg := TriggerConfig{
		ModelID: "m", ClientType: string(models.ClientTypeOpenAICompletions), SummaryWindowTokens: 64000,
		Continuation: &base,
	}
	got := continuationRequest(cfg, "[Context compaction] summarize", 500, 4000)
	if got == nil || len(got.Messages) != 2 || got.MaxTokens == nil || len(base.Messages) != 1 {
		t.Fatalf("extended request = %#v, want the run's messages plus the compaction request", got)
	}
	for name, mutate := range map[string]func(*TriggerConfig){
		"no previous request": func(c *TriggerConfig) { c.Continuation = nil },
		"different model":     func(c *TriggerConfig) { c.ModelID = "other" },
		"unsupported client":  func(c *TriggerConfig) { c.ClientType = string(models.ClientTypeAnthropicMessages) },
		"input overflow":      func(c *TriggerConfig) { c.SummaryWindowTokens = 100 },
	} {
		c := cfg
		mutate(&c)
		if got := continuationRequest(c, "[Context compaction] summarize", 500, 4000); got != nil {
			t.Fatalf("%s: extended the run's request", name)
		}
	}
	if got := continuationRequest(cfg, "[Context compaction] summarize", 500, 499); got != nil {
		t.Fatal("extended the run's request without enough output capacity")
	}
	cfg.ClientType = string(models.ClientTypeOpenAIResponses)
	if got := continuationRequest(cfg, "[Context compaction] summarize", 500, 4000); got == nil {
		t.Fatal("did not extend a supported Responses request")
	}
}

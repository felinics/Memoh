package compaction

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

// The run's request is extended only when the provider caches it and reading
// it again costs less, at half the input price, than the summarizer's own
// request; otherwise the summarizer is asked as before.
func TestContinuationRequestOnlyWhenCheaper(t *testing.T) {
	base := sdk.Request{Model: "m", System: "system", Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("history ", 4000))}}
	cfg := TriggerConfig{
		ModelID: "m", ClientType: string(models.ClientTypeOpenAICompletions), SummaryWindowTokens: 64000,
		Continuation: &Continuation{Request: base, CacheRead: true},
	}
	got := continuationRequest(cfg, "[Context compaction] summarize", 500, 4000, 20000)
	if got == nil || len(got.Messages) != 2 || got.MaxTokens == nil || len(base.Messages) != 1 {
		t.Fatalf("extended request = %#v, want the run's messages plus the compaction request", got)
	}
	for name, mutate := range map[string]func(*TriggerConfig, *int){
		"no cache read": func(c *TriggerConfig, _ *int) { c.Continuation = &Continuation{Request: base} },
		"not cheaper":   func(_ *TriggerConfig, summarizer *int) { *summarizer = 4000 },
		"breakpoints":   func(c *TriggerConfig, _ *int) { c.ClientType = string(models.ClientTypeAnthropicMessages) },
	} {
		c, summarizer := cfg, 20000
		mutate(&c, &summarizer)
		if got := continuationRequest(c, "[Context compaction] summarize", 500, 4000, summarizer); got != nil {
			t.Fatalf("%s: extended the run's request", name)
		}
	}
}

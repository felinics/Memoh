package native

import (
	"testing"

	"github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
)

func TestAggregateStepUsagePreservesReporting(t *testing.T) {
	known := sdk.Usage{
		InputTokens: 100, TotalTokens: 100, CachedInputTokens: 10, CacheReadTokensReported: true,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 90, CacheReadTokens: 10},
	}
	for _, tt := range []struct {
		name     string
		usage    []sdk.Usage
		input    int
		reported bool
	}{
		{name: "single reported step", usage: []sdk.Usage{known}, input: 100, reported: true},
		{name: "two reported steps", usage: []sdk.Usage{known, known}, input: 200, reported: true},
		{name: "missing usage last", usage: []sdk.Usage{known, {}}, input: 100},
		{name: "missing usage first", usage: []sdk.Usage{{}, known}, input: 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var records []step.Record
			for _, usage := range tt.usage {
				records = append(records, step.Record{Result: sdk.ModelResult{Usage: usage}})
			}
			got := aggregateStepUsage(records)
			if got.InputTokens != tt.input || got.CacheReadTokensReported != tt.reported {
				t.Fatalf("usage=%+v, want input=%d reported=%t", got, tt.input, tt.reported)
			}
		})
	}
}

package compaction

import "testing"

func TestRollingSummaryTargetTokensClampsToSafeOutputBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		threshold int
		ratio     int
		window    int
		want      int
	}{
		{name: "default 128k model", threshold: 100000, ratio: 80, window: 128000, want: 16384},
		{name: "million token model", threshold: 100000, ratio: 80, window: 1000000, want: 16384},
		{name: "small model window", threshold: 100000, ratio: 80, window: 32000, want: 8000},
		{name: "ratio remains meaningful below caps", threshold: 10000, ratio: 25, window: 128000, want: 2500},
		{name: "disabled threshold", threshold: 0, ratio: 80, window: 128000, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RollingSummaryTargetTokens(tt.threshold, tt.ratio, tt.window); got != tt.want {
				t.Fatalf("RollingSummaryTargetTokens(%d, %d, %d) = %d, want %d", tt.threshold, tt.ratio, tt.window, got, tt.want)
			}
		})
	}
}

func TestManualSummaryTargetTokensDoesNotDependOnAutomaticThreshold(t *testing.T) {
	t.Parallel()

	if got := ManualSummaryTargetTokens(128000); got != ConservativeSummaryOutputTokens {
		t.Fatalf("ManualSummaryTargetTokens(128000) = %d, want %d", got, ConservativeSummaryOutputTokens)
	}
	if got := ManualSummaryTargetTokens(32000); got != 8000 {
		t.Fatalf("ManualSummaryTargetTokens(32000) = %d, want 8000", got)
	}
	if got := ManualSummaryTargetTokens(0); got != ConservativeSummaryOutputTokens {
		t.Fatalf("ManualSummaryTargetTokens(0) = %d, want %d", got, ConservativeSummaryOutputTokens)
	}
}

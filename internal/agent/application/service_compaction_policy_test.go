package application

import "testing"

func TestAutoCompactionThresholdModelRelative(t *testing.T) {
	t.Parallel()

	if got := autoCompactionThreshold(0, 200000); got != 100000 {
		t.Fatalf("model-relative soft threshold = %d, want 50%% of the window", got)
	}
	if got := autoCompactionThreshold(0, 0); got != 0 {
		t.Fatalf("model-relative threshold without a window = %d, want 0 (async stays off)", got)
	}
	if got := autoCompactionThreshold(100000, 200000); got != 100000 {
		t.Fatalf("legacy threshold below the clamp = %d, want the configured value", got)
	}
	if got := autoCompactionThreshold(500000, 200000); got != 140000 {
		t.Fatalf("legacy threshold above the clamp = %d, want 70%% of the window", got)
	}
}

func TestCompactionTargetTokensForMode(t *testing.T) {
	t.Parallel()

	if got := compactionTargetTokensFor(0, 80, 200000); got != 80000 {
		t.Fatalf("model-relative target = %d, want 40%% of the window", got)
	}
	if got := compactionTargetTokensFor(0, 80, 0); got != 0 {
		t.Fatalf("model-relative target without a window = %d, want 0", got)
	}
	if got := compactionTargetTokensFor(100000, 80, 200000); got != 40000 {
		t.Fatalf("legacy target = %d, want the ratio-derived (100-ratio)%% share", got)
	}
}

func TestSyncCompactionShouldRun(t *testing.T) {
	t.Parallel()

	if syncCompactionShouldRun(0, 140000, 200000) {
		t.Fatal("model-relative backstop fired below the 75% hard threshold")
	}
	if !syncCompactionShouldRun(0, 150000, 200000) {
		t.Fatal("model-relative backstop must fire at the 75% hard threshold")
	}
	if syncCompactionShouldRun(0, 150000, 0) {
		t.Fatal("model-relative backstop without a window must stand down")
	}
	if !syncCompactionShouldRun(100000, 1, 200000) {
		t.Fatal("legacy mode must defer to the caller's shared-budget gate")
	}
}

func TestAsyncCompactionTargetKeepsLegacyRatioSelection(t *testing.T) {
	t.Parallel()

	if got := asyncCompactionTargetTokens(100000, 2000000); got != 0 {
		t.Fatalf("legacy async target = %d, want 0 so the engine keeps ratio-based selection", got)
	}
	if got := asyncCompactionTargetTokens(0, 200000); got != 80000 {
		t.Fatalf("model-relative async target = %d, want 40%% of the window", got)
	}
}

func TestAsyncCompactionPassesPerMode(t *testing.T) {
	t.Parallel()

	if got := asyncCompactionPasses(100000); got != 1 {
		t.Fatalf("legacy passes = %d, want the historical one-shot semantics", got)
	}
	if got := asyncCompactionPasses(0); got != maxAsyncCompactionPasses {
		t.Fatalf("model-relative passes = %d, want the bounded drain", got)
	}
}

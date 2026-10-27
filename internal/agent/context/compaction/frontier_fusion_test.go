package compaction

import "testing"

func TestRollupArtifactLevelUsesHighestParentLevel(t *testing.T) {
	t.Parallel()

	if got := rollupArtifactLevel([]Artifact{{Level: 0}, {Level: 2}, {Level: 1}}); got != 3 {
		t.Fatalf("rollup level = %d, want one above the highest parent", got)
	}
}

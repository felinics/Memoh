package compaction

import "errors"

func rollupArtifactLevel(parents []Artifact) int {
	level := 0
	for _, parent := range parents {
		level = max(level, parent.Level+1)
	}
	return level
}

var errIncompleteAbsorbedCoverage = errors.New("compaction: rollup parent has no persisted coverage")

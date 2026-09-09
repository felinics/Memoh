package compaction

import (
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestArtifactFrontierRejectsDerivedCoverageThatDropsParentSources(t *testing.T) {
	t.Parallel()

	a1, a2, b := testArtifact("a1"), testArtifact("a2"), testArtifact("b")
	a1.Coverage = testCoverage("row-1")
	a2.Coverage = testCoverage("row-2")
	a1.SupersededBy, a1.SupersededAt = b.ID, time.Unix(1, 0)
	a2.SupersededBy, a2.SupersededAt = b.ID, time.Unix(1, 0)
	b.ParentIDs = []string{a1.ID, a2.ID}
	b.Coverage = testCoverage("row-1")

	frontier := buildArtifactFrontier([]Artifact{a1, a2, b})

	if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueCoverageMismatch) {
		t.Fatalf("incomplete derived coverage was accepted: frontier=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
	}
}

func TestArtifactFrontierRejectsOverlappingActiveCoverage(t *testing.T) {
	t.Parallel()

	a, b := testArtifact("a"), testArtifact("b")
	a.Coverage = testCoverage("shared-row")
	b.Coverage = testCoverage("shared-row")

	frontier := buildArtifactFrontier([]Artifact{a, b})

	if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueCoverageOverlap) {
		t.Fatalf("overlapping active coverage was accepted: frontier=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
	}
}

func TestArtifactFrontierResolvesCoveredRefOnlyWhenSourceHashMatches(t *testing.T) {
	t.Parallel()

	artifact := testArtifact("a")
	covered := testCoverage("row-1")
	covered[0].Ref.HashAlgo = contextfrag.HashAlgoSHA256
	covered[0].Ref.HashScope = contextfrag.HashScopeSourcePayload
	covered[0].Ref.ContentHash = "source-hash"
	artifact.Coverage = covered
	frontier := buildArtifactFrontier([]Artifact{artifact})

	resolved, ok := frontier.ResolveCoveredRef(covered[0].Ref)
	if !ok || resolved.ID != artifact.ID {
		t.Fatalf("matching covered ref did not resolve: %#v, %v", resolved, ok)
	}
	mismatched := covered[0].Ref
	mismatched.ContentHash = "different-hash"
	if _, ok := frontier.ResolveCoveredRef(mismatched); ok {
		t.Fatal("mismatched source hash resolved through durable coverage")
	}
}

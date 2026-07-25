package compaction

import "testing"

func TestMergeArtifactFrontiersQuarantinesCrossFrontierCoverageOverlap(t *testing.T) {
	t.Parallel()

	a, b := testArtifact("a"), testArtifact("b")
	a.Coverage = testCoverage("shared-row")
	b.Coverage = testCoverage("shared-row")
	merged := MergeArtifactFrontiers(
		NewArtifactAliasFrontier("parent-a", a),
		NewArtifactAliasFrontier("parent-b", b),
	)

	if len(merged.Artifacts) != 0 || !hasLineageIssue(merged.Issues, LineageIssueCoverageOverlap) {
		t.Fatalf("cross-frontier overlap was accepted: artifacts=%#v issues=%#v", merged.Artifacts, merged.Issues)
	}
	if _, ok := merged.ResolveCoveredRef(a.Coverage[0].Ref); ok {
		t.Fatal("overlapping covered ref remained resolvable")
	}
}

func TestMergeArtifactFrontiersPreservesDistinctAliases(t *testing.T) {
	t.Parallel()

	a, b := testArtifact("a"), testArtifact("b")
	a.Coverage = testCoverage("row-a")
	b.Coverage = testCoverage("row-b")
	merged := MergeArtifactFrontiers(
		NewArtifactAliasFrontier("parent-a", a),
		NewArtifactAliasFrontier("parent-b", b),
	)

	for alias, want := range map[string]string{"parent-a": a.ID, "parent-b": b.ID} {
		artifact, ok := merged.Resolve(alias)
		if !ok || artifact.ID != want {
			t.Fatalf("Resolve(%q) = %#v, %v; want %q", alias, artifact, ok, want)
		}
	}
}

func TestArtifactCatalogKeepsOwnerResolutionPartitioned(t *testing.T) {
	t.Parallel()

	owner1 := ArtifactOwner{BotID: "bot", SessionID: "session-1", SessionIDKnown: true}
	owner2 := ArtifactOwner{BotID: "bot", SessionID: "session-2", SessionIDKnown: true}
	a, b := testArtifact("a"), testArtifact("b")
	a.Coverage = testCoverage("shared-id")
	b.Coverage = testCoverage("shared-id")
	catalog := NewArtifactCatalog()
	catalog.Add(owner1, NewArtifactAliasFrontier("same-alias", a))
	catalog.Add(owner2, NewArtifactAliasFrontier("same-alias", b))

	for owner, want := range map[ArtifactOwner]string{owner1: a.ID, owner2: b.ID} {
		artifact, ok := catalog.Resolve(owner, "same-alias")
		if !ok || artifact.ID != want {
			t.Fatalf("Resolve(%#v) = %#v, %v; want %q", owner, artifact, ok, want)
		}
	}
}

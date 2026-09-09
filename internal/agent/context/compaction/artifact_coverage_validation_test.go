package compaction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestDecodeArtifactCoverageAcceptsStrictAndLegacyEmptyCoverage(t *testing.T) {
	t.Parallel()

	strict := strictTestCoveredSource("row-1", 1)
	decoded, err := DecodeArtifactCoverage(mustMarshalCoverage(t, strict))
	if err != nil {
		t.Fatalf("DecodeArtifactCoverage(strict) error = %v", err)
	}
	if len(decoded) != 1 || decoded[0].Ref.ContentHash != strict.Ref.ContentHash {
		t.Fatalf("DecodeArtifactCoverage(strict) = %#v", decoded)
	}

	for _, raw := range [][]byte{nil, []byte(" "), []byte("null"), []byte("[]")} {
		decoded, err := DecodeArtifactCoverage(raw)
		if err != nil || len(decoded) != 0 {
			t.Fatalf("DecodeArtifactCoverage(%q) = %#v, %v; want empty coverage", raw, decoded, err)
		}
	}
}

func TestDecodeArtifactCoverageRejectsInvalidPersistedCoverage(t *testing.T) {
	t.Parallel()

	missingHashIdentity := strictTestCoveredSource("missing-hash-identity", 1)
	missingHashIdentity.Ref.HashAlgo = ""
	missingHashIdentity.Ref.HashScope = ""
	missingHashIdentity.Ref.ContentHash = ""

	emptyContentHash := strictTestCoveredSource("empty-content-hash", 1)
	emptyContentHash.Ref.ContentHash = " "

	canonicalScope := strictTestCoveredSource("canonical-scope", 1)
	canonicalScope.Ref.HashScope = contextfrag.HashScopeCanonicalFragment

	duplicate := strictTestCoveredSource("duplicate", 1)
	newer := strictTestCoveredSource("newer", 2)
	older := strictTestCoveredSource("older", 1)

	tests := []struct {
		name    string
		covered []CoveredSource
		wantErr string
	}{
		{name: "missing hash identity", covered: []CoveredSource{missingHashIdentity}, wantErr: "sha256"},
		{name: "empty content hash", covered: []CoveredSource{emptyContentHash}, wantErr: "content hash"},
		{name: "canonical hash scope", covered: []CoveredSource{canonicalScope}, wantErr: "source_payload"},
		{name: "duplicate stable key", covered: []CoveredSource{duplicate, duplicate}, wantErr: "duplicate"},
		{name: "decreasing creation time", covered: []CoveredSource{newer, older}, wantErr: "created_at_ms"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeArtifactCoverage(mustMarshalCoverage(t, tt.covered...))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("DecodeArtifactCoverage() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCoverageIncludesRequiresExactPersistedHash(t *testing.T) {
	t.Parallel()

	strict := strictTestCoveredSource("row-1", 1)
	hashless := strict
	hashless.Ref.HashAlgo = ""
	hashless.Ref.HashScope = ""
	hashless.Ref.ContentHash = ""

	if !coverageIncludes([]CoveredSource{strict}, []CoveredSource{strict}) {
		t.Fatal("identical strict coverage did not match")
	}
	if coverageIncludes([]CoveredSource{strict}, []CoveredSource{hashless}) {
		t.Fatal("hashless required coverage acted as a wildcard")
	}
	if coverageIncludes([]CoveredSource{hashless}, []CoveredSource{hashless}) {
		t.Fatal("hashless persisted coverage matched itself")
	}
}

func TestRollupCoverageBuildsValidMultiParentLineage(t *testing.T) {
	t.Parallel()

	parentA := testArtifact("parent-a")
	parentA.Level = 1
	parentA.Coverage = []CoveredSource{
		strictTestCoveredSource("row-a1", 1000),
		strictTestCoveredSource("row-a2", 2000),
	}
	parentB := testArtifact("parent-b")
	parentB.Level = 3
	parentB.Coverage = []CoveredSource{
		strictTestCoveredSource("row-b1", 3000),
		strictTestCoveredSource("row-b2", 4000),
	}
	currentCoverage := []CoveredSource{
		strictTestCoveredSource("row-new-1", 5000),
		strictTestCoveredSource("row-new-2", 6000),
	}
	current := artifactMetadata{
		Coverage:      mustMarshalCoverage(t, currentCoverage...),
		AnchorStartMs: 5000,
		AnchorEndMs:   6000,
	}

	merged, err := rollupArtifactMetadata([]Artifact{parentA, parentB}, current)
	if err != nil {
		t.Fatalf("rollupArtifactMetadata: %v", err)
	}
	coverage, err := DecodeArtifactCoverage(merged.Coverage)
	if err != nil {
		t.Fatalf("DecodeArtifactCoverage: %v", err)
	}
	if len(coverage) != 6 || merged.AnchorStartMs != 1000 || merged.AnchorEndMs != 6000 {
		t.Fatalf("merged coverage = %#v anchor=%d..%d, want six chronological entries at 1000..6000", coverage, merged.AnchorStartMs, merged.AnchorEndMs)
	}

	rollup := testArtifact("rollup")
	rollup.Level = rollupArtifactLevel([]Artifact{parentA, parentB})
	rollup.ParentIDs = []string{parentA.ID, parentB.ID}
	rollup.Coverage = coverage
	rollup.AnchorStartMs = merged.AnchorStartMs
	rollup.AnchorEndMs = merged.AnchorEndMs
	parentA.SupersededBy, parentA.SupersededAt = rollup.ID, time.Unix(1, 0)
	parentB.SupersededBy, parentB.SupersededAt = rollup.ID, time.Unix(1, 0)

	frontier := buildArtifactFrontier([]Artifact{parentA, parentB, rollup})
	if len(frontier.Issues) != 0 {
		t.Fatalf("valid rollup lineage issues = %#v", frontier.Issues)
	}
	if len(frontier.Artifacts) != 1 || frontier.Artifacts[0].ID != rollup.ID {
		t.Fatalf("frontier = %#v, want only rollup %q", frontier.Artifacts, rollup.ID)
	}
	for _, id := range []string{parentA.ID, parentB.ID, rollup.ID} {
		resolved, ok := frontier.Resolve(id)
		if !ok || resolved.ID != rollup.ID {
			t.Fatalf("Resolve(%q) = %#v, %t; want %q", id, resolved, ok, rollup.ID)
		}
	}
}

func TestArtifactFrontierAcceptsPartialRollupStraddlingKeptArtifacts(t *testing.T) {
	t.Parallel()

	absorbedOldest := testArtifact("absorbed-oldest")
	absorbedOldest.Coverage = []CoveredSource{strictTestCoveredSource("row-oldest", 1000)}
	absorbedOldest.AnchorStartMs = 1000
	absorbedOldest.AnchorEndMs = 1000

	absorbedNext := testArtifact("absorbed-next")
	absorbedNext.Level = 1
	absorbedNext.Coverage = []CoveredSource{strictTestCoveredSource("row-next", 1500)}
	absorbedNext.AnchorStartMs = 1500
	absorbedNext.AnchorEndMs = 1500

	keptMiddle := testArtifact("kept-middle")
	keptMiddle.Coverage = []CoveredSource{strictTestCoveredSource("row-middle", 2000)}
	keptMiddle.AnchorStartMs = 2000
	keptMiddle.AnchorEndMs = 2000

	keptNewest := testArtifact("kept-newest")
	keptNewest.Coverage = []CoveredSource{strictTestCoveredSource("row-newest", 3000)}
	keptNewest.AnchorStartMs = 3000
	keptNewest.AnchorEndMs = 3000

	fused := testArtifact("partial-rollup")
	absorbed := []Artifact{absorbedOldest, absorbedNext}
	fused.Level = rollupArtifactLevel(absorbed)
	fused.ParentIDs = []string{absorbedOldest.ID, absorbedNext.ID}
	fused.Coverage = []CoveredSource{
		absorbedOldest.Coverage[0],
		absorbedNext.Coverage[0],
		strictTestCoveredSource("row-current-pass", 4000),
	}
	fused.AnchorStartMs = 1000
	fused.AnchorEndMs = 4000
	absorbedOldest.SupersededBy = fused.ID
	absorbedOldest.SupersededAt = time.Unix(1, 0)
	absorbedNext.SupersededBy = fused.ID
	absorbedNext.SupersededAt = time.Unix(1, 0)

	frontier := buildArtifactFrontier([]Artifact{absorbedOldest, absorbedNext, keptMiddle, keptNewest, fused})
	if len(frontier.Issues) != 0 {
		t.Fatalf("partial rollup lineage issues = %#v", frontier.Issues)
	}
	if len(frontier.Artifacts) != 3 {
		t.Fatalf("active frontier = %#v, want fused checkpoint plus two kept artifacts", frontier.Artifacts)
	}
	for _, id := range []string{absorbedOldest.ID, absorbedNext.ID, fused.ID} {
		resolved, ok := frontier.Resolve(id)
		if !ok || resolved.ID != fused.ID {
			t.Fatalf("Resolve(%q) = %#v, %t; want fused checkpoint %q", id, resolved, ok, fused.ID)
		}
	}
	for _, parent := range absorbed {
		resolved, ok := frontier.ResolveCoveredRef(parent.Coverage[0].Ref)
		if !ok || resolved.ID != fused.ID {
			t.Fatalf("ResolveCoveredRef(%q) = %#v, %t; want fused checkpoint %q", parent.ID, resolved, ok, fused.ID)
		}
	}
	for _, kept := range []Artifact{keptMiddle, keptNewest} {
		resolved, ok := frontier.Resolve(kept.ID)
		if !ok || resolved.ID != kept.ID {
			t.Fatalf("Resolve(%q) = %#v, %t; want kept artifact to resolve to itself", kept.ID, resolved, ok)
		}
		resolved, ok = frontier.ResolveCoveredRef(kept.Coverage[0].Ref)
		if !ok || resolved.ID != kept.ID {
			t.Fatalf("ResolveCoveredRef(%q) = %#v, %t; want kept artifact to resolve to itself", kept.ID, resolved, ok)
		}
	}
}

func TestArtifactFrontierRejectsDerivedCoverageThatReordersParentSources(t *testing.T) {
	t.Parallel()

	parent := testArtifact("ordered-parent")
	child := testArtifact("reordered-child")
	parent.SupersededBy = child.ID
	parent.SupersededAt = time.Unix(1, 0)
	parent.Coverage = testCoverage("row-1", "row-2")
	child.ParentIDs = []string{parent.ID}
	child.Coverage = testCoverage("row-2", "row-1")
	for i := range parent.Coverage {
		parent.Coverage[i].CreatedAtMs = 1
		child.Coverage[i].CreatedAtMs = 1
	}

	frontier := buildArtifactFrontier([]Artifact{parent, child})
	if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueCoverageMismatch) {
		t.Fatalf("reordered derived coverage remained active: artifacts=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
	}
}

func TestArtifactFrontierRejectsDerivedCoverageThatReordersParentSegments(t *testing.T) {
	t.Parallel()

	parentA := testArtifact("parent-a")
	parentB := testArtifact("parent-b")
	child := testArtifact("reordered-child")
	parentA.SupersededBy = child.ID
	parentA.SupersededAt = time.Unix(1, 0)
	parentA.Coverage = testCoverage("row-a")
	parentB.SupersededBy = child.ID
	parentB.SupersededAt = time.Unix(1, 0)
	parentB.Coverage = testCoverage("row-b")
	child.ParentIDs = []string{parentA.ID, parentB.ID}
	child.Coverage = testCoverage("row-b", "row-a")

	frontier := buildArtifactFrontier([]Artifact{parentA, parentB, child})
	if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueCoverageMismatch) {
		t.Fatalf("reordered parent segments remained active: artifacts=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
	}
}

func TestArtifactFrontierRejectsDerivedCoverageThatRewritesParentMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*CoveredSource)
	}{
		{name: "created at", mutate: func(source *CoveredSource) { source.CreatedAtMs++ }},
		{name: "external message id", mutate: func(source *CoveredSource) { source.ExternalMessageID = "changed" }},
		{name: "reply target", mutate: func(source *CoveredSource) { source.SourceReplyToMessageID = "changed" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent := testArtifact("metadata-parent")
			child := testArtifact("metadata-child")
			parent.SupersededBy = child.ID
			parent.SupersededAt = time.Unix(1, 0)
			parent.Coverage = testCoverage("row-1")
			parent.Coverage[0].ExternalMessageID = "external-1"
			parent.Coverage[0].SourceReplyToMessageID = "reply-1"
			child.ParentIDs = []string{parent.ID}
			child.Coverage = append([]CoveredSource(nil), parent.Coverage...)
			tt.mutate(&child.Coverage[0])

			frontier := buildArtifactFrontier([]Artifact{parent, child})
			if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueCoverageMismatch) {
				t.Fatalf("rewritten parent metadata remained active: artifacts=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
			}
		})
	}
}

func TestPersistedMalformedOrHashlessLineageCoverageIsQuarantined(t *testing.T) {
	t.Parallel()

	hashless := strictTestCoveredSource("hashless-row", 1)
	hashless.Ref.HashAlgo = ""
	hashless.Ref.HashScope = ""
	hashless.Ref.ContentHash = ""

	tests := []struct {
		name     string
		coverage []byte
	}{
		{name: "malformed", coverage: []byte("{")},
		{name: "hashless", coverage: mustMarshalCoverage(t, hashless)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := projectionRow(t, "00000000-0000-0000-0000-00000000da01")
			row.ParentIds = []pgtype.UUID{mustProjectionUUID(t, "00000000-0000-0000-0000-00000000da02")}
			row.Coverage = tt.coverage
			artifact, err := artifactFromDBRow(row)
			if err != nil {
				t.Fatalf("artifactFromDBRow() error = %v", err)
			}
			if !artifact.CoverageMalformed {
				t.Fatal("invalid persisted coverage was not marked malformed")
			}

			frontier := buildArtifactFrontier([]Artifact{artifact})
			if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueMalformedCoverage) {
				t.Fatalf("invalid lineage coverage was not quarantined: frontier=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
			}
		})
	}
}

func TestArtifactProjectionQuarantinesCoverageAnchorMismatch(t *testing.T) {
	t.Parallel()

	row := projectionRow(t, "00000000-0000-0000-0000-00000000da03")
	row.Coverage = testCoverageJSON(t, "anchor-row")
	row.AnchorStartMs = 0
	row.AnchorEndMs = 2
	artifact, err := artifactFromDBRow(row)
	if err != nil {
		t.Fatalf("artifactFromDBRow() error = %v", err)
	}
	if !artifact.CoverageMalformed {
		t.Fatal("coverage/anchor mismatch was not marked malformed")
	}

	frontier := buildArtifactFrontier([]Artifact{artifact})
	if len(frontier.Artifacts) != 0 || !hasLineageIssue(frontier.Issues, LineageIssueMalformedCoverage) {
		t.Fatalf("coverage/anchor mismatch remained active: frontier=%#v issues=%#v", frontier.Artifacts, frontier.Issues)
	}
}

func strictTestCoveredSource(id string, createdAtMs int64) CoveredSource {
	return CoveredSource{
		Ref: contextfrag.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    contextfrag.HashAlgoSHA256,
			HashScope:   contextfrag.HashScopeSourcePayload,
			ContentHash: "hash-" + id,
			Schema:      contextfrag.SchemaContextRef,
			Durability:  contextfrag.RefDurable,
		},
		CreatedAtMs: createdAtMs,
	}
}

func mustMarshalCoverage(t *testing.T, covered ...CoveredSource) []byte {
	t.Helper()
	raw, err := json.Marshal(covered)
	if err != nil {
		t.Fatalf("marshal coverage: %v", err)
	}
	return raw
}

package compaction

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
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
	canonicalScope.Ref.HashScope = fragment.HashScopeCanonicalFragment

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
			row.ParentIDs = []string{mustProjectionUUID(t, "00000000-0000-0000-0000-00000000da02")}
			row.Coverage = tt.coverage
			artifact, err := artifactFromRecord(row)
			if err != nil {
				t.Fatalf("artifactFromRecord() error = %v", err)
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
	artifact, err := artifactFromRecord(row)
	if err != nil {
		t.Fatalf("artifactFromRecord() error = %v", err)
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
		Ref: fragment.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    fragment.HashAlgoSHA256,
			HashScope:   fragment.HashScopeSourcePayload,
			ContentHash: "hash-" + id,
			Schema:      fragment.SchemaContextRef,
			Durability:  fragment.RefDurable,
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

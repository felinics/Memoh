package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
)

func TestArtifactFrontierResolvesMultiLevelLineageToOneTerminal(t *testing.T) {
	t.Parallel()

	a := testArtifact("a")
	b := testArtifact("b")
	c := testArtifact("c")
	a.SupersededBy = b.ID
	a.SupersededAt = time.Unix(1, 0)
	a.Coverage = testCoverage("row-a")
	b.ParentIDs = []string{a.ID}
	b.Coverage = testCoverage("row-a")
	b.SupersededBy = c.ID
	b.SupersededAt = time.Unix(2, 0)
	c.ParentIDs = []string{b.ID}
	c.Coverage = testCoverage("row-a")

	frontier := buildArtifactFrontier([]Artifact{a, b, c})

	if len(frontier.Issues) != 0 {
		t.Fatalf("valid lineage issues = %#v", frontier.Issues)
	}
	if len(frontier.Artifacts) != 1 || frontier.Artifacts[0].ID != c.ID {
		t.Fatalf("frontier = %#v, want only terminal %q", frontier.Artifacts, c.ID)
	}
	for _, id := range []string{a.ID, b.ID, c.ID} {
		resolved, ok := frontier.Resolve(id)
		if !ok || resolved.ID != c.ID {
			t.Fatalf("Resolve(%q) = %#v, %v; want %q", id, resolved, ok, c.ID)
		}
	}
}

func TestArtifactFrontierQuarantinesBrokenLineageWithoutDroppingValidLeaf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		artifacts []Artifact
		startID   string
		issue     LineageIssueKind
	}{
		{
			name: "cycle",
			artifacts: func() []Artifact {
				a, b := testArtifact("cycle-a"), testArtifact("cycle-b")
				a.SupersededBy, a.SupersededAt, a.ParentIDs = b.ID, time.Unix(1, 0), []string{b.ID}
				b.SupersededBy, b.SupersededAt, b.ParentIDs = a.ID, time.Unix(2, 0), []string{a.ID}
				a.Coverage, b.Coverage = testCoverage("cycle-row"), testCoverage("cycle-row")
				return []Artifact{a, b}
			}(),
			startID: "cycle-a",
			issue:   LineageIssueCycle,
		},
		{
			name: "missing successor",
			artifacts: func() []Artifact {
				a := testArtifact("missing-a")
				a.SupersededBy, a.SupersededAt = "missing-b", time.Unix(1, 0)
				a.Coverage = testCoverage("missing-row")
				return []Artifact{a}
			}(),
			startID: "missing-a",
			issue:   LineageIssueMissingSuccessor,
		},
		{
			name: "inactive successor",
			artifacts: func() []Artifact {
				a, b := testArtifact("inactive-a"), testArtifact("inactive-b")
				a.SupersededBy, a.SupersededAt = b.ID, time.Unix(1, 0)
				a.Coverage = testCoverage("inactive-row")
				b.Status, b.ParentIDs = "pending", []string{a.ID}
				return []Artifact{a, b}
			}(),
			startID: "inactive-a",
			issue:   LineageIssueInactiveSuccessor,
		},
		{
			name: "inconsistent marker",
			artifacts: func() []Artifact {
				a, b := testArtifact("marker-a"), testArtifact("marker-b")
				a.SupersededBy = b.ID
				b.ParentIDs, b.Coverage = []string{a.ID}, testCoverage("marker-row")
				return []Artifact{a, b}
			}(),
			startID: "marker-a",
			issue:   LineageIssueInconsistentMarker,
		},
		{
			name: "orphaned marker after successor delete",
			artifacts: func() []Artifact {
				a := testArtifact("orphan-marker-a")
				a.SupersededAt = time.Unix(1, 0)
				a.Coverage = testCoverage("orphan-marker-row")
				return []Artifact{a}
			}(),
			startID: "orphan-marker-a",
			issue:   LineageIssueInconsistentMarker,
		},
		{
			name: "parent mismatch",
			artifacts: func() []Artifact {
				a, b := testArtifact("parent-a"), testArtifact("parent-b")
				a.SupersededBy, a.SupersededAt = b.ID, time.Unix(1, 0)
				a.Coverage = testCoverage("parent-row")
				return []Artifact{a, b}
			}(),
			startID: "parent-a",
			issue:   LineageIssueParentMismatch,
		},
		{
			name: "scope mismatch",
			artifacts: func() []Artifact {
				a, b := testArtifact("scope-a"), testArtifact("scope-b")
				a.SupersededBy, a.SupersededAt = b.ID, time.Unix(1, 0)
				a.Coverage = testCoverage("scope-row")
				b.SessionID, b.ParentIDs, b.Coverage = "other-session", []string{a.ID}, testCoverage("scope-row")
				return []Artifact{a, b}
			}(),
			startID: "scope-a",
			issue:   LineageIssueScopeMismatch,
		},
		{
			name: "derived coverage missing",
			artifacts: func() []Artifact {
				a, b := testArtifact("coverage-a"), testArtifact("coverage-b")
				a.SupersededBy, a.SupersededAt = b.ID, time.Unix(1, 0)
				a.Coverage = testCoverage("coverage-row")
				b.ParentIDs = []string{a.ID}
				return []Artifact{a, b}
			}(),
			startID: "coverage-a",
			issue:   LineageIssueMissingDerivedCoverage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			valid := testArtifact("valid-" + tt.name)
			valid.AnchorStartMs = 1
			frontier := buildArtifactFrontier(append(tt.artifacts, valid))

			if len(frontier.Artifacts) != 1 || frontier.Artifacts[0].ID != valid.ID {
				t.Fatalf("frontier = %#v, want only unrelated valid leaf %q", frontier.Artifacts, valid.ID)
			}
			if _, ok := frontier.Resolve(tt.startID); ok {
				t.Fatalf("broken lineage %q unexpectedly resolved", tt.startID)
			}
			if !hasLineageIssue(frontier.Issues, tt.issue) {
				t.Fatalf("issues = %#v, want kind %q", frontier.Issues, tt.issue)
			}
		})
	}
}

func TestArtifactProjectionLoadActiveByIDReturnsTypedLineageErrors(t *testing.T) {
	t.Parallel()

	aID := "00000000-0000-0000-0000-00000000ca01"
	bID := "00000000-0000-0000-0000-00000000ca02"
	a := projectionRow(t, aID)
	b := projectionRow(t, bID)
	a.SupersededBy, a.SupersededAt, a.ParentIDs = b.ID, time.Unix(1, 0), []string{b.ID}
	b.SupersededBy, b.SupersededAt, b.ParentIDs = a.ID, time.Unix(2, 0), []string{a.ID}
	a.Coverage, b.Coverage = testCoverageJSON(t, "cycle-db-row"), testCoverageJSON(t, "cycle-db-row")

	tests := []struct {
		name  string
		rows  map[string]ArtifactRecord
		issue LineageIssueKind
	}{
		{
			name:  "cycle",
			rows:  map[string]ArtifactRecord{a.ID: a, b.ID: b},
			issue: LineageIssueCycle,
		},
		{
			name: "missing successor",
			rows: map[string]ArtifactRecord{
				a.ID: func() ArtifactRecord {
					row := projectionRow(t, aID)
					row.SupersededBy = b.ID
					row.SupersededAt = time.Unix(1, 0)
					row.Coverage = testCoverageJSON(t, "missing-successor-row")
					return row
				}(),
			},
			issue: LineageIssueMissingSuccessor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			queries := &projectionQueries{rows: tt.rows}
			_, err := NewArtifactProjection(queries).LoadActiveByID(context.Background(), aID, ArtifactOwner{})
			var lineageErr *LineageError
			if !errors.As(err, &lineageErr) || lineageErr.Issue.Kind != tt.issue {
				t.Fatalf("LoadActiveByID error = %v, want lineage issue %q", err, tt.issue)
			}
		})
	}
}

func testArtifact(id string) Artifact {
	return Artifact{
		ID:        id,
		BotID:     "bot",
		SessionID: "session",
		Status:    "ok",
		Summary:   "summary " + id,
	}
}

func testCoverage(ids ...string) []CoveredSource {
	covered := make([]CoveredSource, 0, len(ids))
	for i, id := range ids {
		covered = append(covered, CoveredSource{
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
			CreatedAtMs: int64(i + 1),
		})
	}
	return covered
}

func testCoverageJSON(t *testing.T, ids ...string) []byte {
	t.Helper()
	coverage := testCoverage(ids...)
	for i := range coverage {
		coverage[i].CreatedAtMs = 0
	}
	raw, err := json.Marshal(coverage)
	if err != nil {
		t.Fatalf("marshal coverage: %v", err)
	}
	return raw
}

func hasLineageIssue(issues []LineageIssue, kind LineageIssueKind) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}

type projectionQueries struct {
	rows       map[string]ArtifactRecord
	getErrors  map[string]error
	parentsErr error
}

func (q *projectionQueries) GetArtifact(_ context.Context, id string) (ArtifactRecord, error) {
	if err := q.getErrors[id]; err != nil {
		return ArtifactRecord{}, err
	}
	row, ok := q.rows[id]
	if !ok {
		return ArtifactRecord{}, ErrArtifactNotFound
	}
	return row, nil
}

func (q *projectionQueries) ListArtifactsBySession(context.Context, string) ([]ArtifactRecord, error) {
	rows := make([]ArtifactRecord, 0, len(q.rows))
	for _, row := range q.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (q *projectionQueries) ListParentIDs(_ context.Context, arg ArtifactParentsInput) ([]string, error) {
	if q.parentsErr != nil {
		return nil, q.parentsErr
	}
	var ids []string
	for _, row := range q.rows {
		if row.Status == "ok" && row.SupersededBy == arg.SuccessorID && row.BotID == arg.BotID && row.SessionID == arg.SessionID {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}

func projectionRow(t *testing.T, id string) ArtifactRecord {
	t.Helper()
	parsed, err := parseUUID(id)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", id, err)
	}
	return ArtifactRecord{ID: parsed, Status: "ok", Summary: "summary " + id}
}

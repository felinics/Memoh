package compaction

import (
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestTimelineArtifactsProjectsFrontier(t *testing.T) {
	started := time.UnixMilli(4000).UTC()
	artifacts := []Artifact{{
		ID:            "a1",
		Summary:       "window summary",
		AnchorStartMs: 1000,
		StartedAt:     started,
		Coverage: []CoveredSource{
			{Ref: contextfrag.ContextRef{ID: "h1"}, ExternalMessageID: "m1", CreatedAtMs: 1000},
			{Ref: contextfrag.ContextRef{ID: "h2"}, CreatedAtMs: 2000},
		},
	}}

	projected := TimelineArtifacts(artifacts)
	if len(projected) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(projected))
	}
	artifact := projected[0]
	if artifact.ID != "a1" || artifact.Summary != "window summary" || artifact.AnchorStartMs != 1000 {
		t.Fatalf("unexpected artifact projection %+v", artifact)
	}
	if artifact.CoverageAsOfMs != 4000 {
		t.Fatalf("expected coverage as-of from StartedAt, got %d", artifact.CoverageAsOfMs)
	}
	if len(artifact.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %+v", artifact.Sources)
	}
	if artifact.Sources[0].HistoryMessageID != "h1" || artifact.Sources[0].ExternalMessageID != "m1" || artifact.Sources[0].CreatedAtMs != 1000 {
		t.Fatalf("unexpected first source %+v", artifact.Sources[0])
	}
	if artifact.Sources[1].HistoryMessageID != "h2" || artifact.Sources[1].ExternalMessageID != "" {
		t.Fatalf("unexpected second source %+v", artifact.Sources[1])
	}
}

func TestTimelineArtifactsSkipsUnusableArtifacts(t *testing.T) {
	artifacts := []Artifact{
		{ID: "blank", Summary: "   ", Coverage: []CoveredSource{{ExternalMessageID: "m0", CreatedAtMs: 1}}},
		{ID: "malformed", Summary: "usable text", CoverageMalformed: true, Coverage: []CoveredSource{{ExternalMessageID: "m0", CreatedAtMs: 1}}},
		{ID: "legacy-empty-coverage", Summary: "cannot replace anything"},
		{ID: "ok", Summary: "kept", Coverage: []CoveredSource{{ExternalMessageID: "m1", CreatedAtMs: 1000}}},
	}

	projected := TimelineArtifacts(artifacts)
	if len(projected) != 1 || projected[0].ID != "ok" {
		t.Fatalf("expected only usable artifact, got %+v", projected)
	}
	if projected[0].CoverageAsOfMs != 0 {
		t.Fatalf("expected zero coverage as-of without StartedAt, got %d", projected[0].CoverageAsOfMs)
	}
}

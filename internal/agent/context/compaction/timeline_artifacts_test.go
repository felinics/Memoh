package compaction

import (
	"fmt"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestTimelineArtifactsProjectsFrontier(t *testing.T) {
	started := time.UnixMilli(4000).UTC()
	artifacts := []Artifact{{
		ID:          "a1",
		Summary:     "window summary",
		AnchorEndMs: 2000,
		StartedAt:   started,
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
	if artifact.ID != "a1" || artifact.Summary != "window summary" || artifact.AnchorEndMs != 2000 {
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

func TestTimelineRollupKeepsAnEditItsParentNeverCovered(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(fmt.Sprintf("event_cursor=%v", versioned), func(t *testing.T) {
			coverage := testCoverage("old-row")
			coverage[0].ExternalMessageID = "rc-1"
			coverage[0].CreatedAtMs = 1000
			parent := Artifact{ID: "parent", Summary: "original content only", StartedAt: time.UnixMilli(4000), AnchorEndMs: 1000, Coverage: coverage}
			rc := timeline.RenderedContext{{MessageID: "rc-1", ReceivedAtMs: 1000, EditedAtMs: 5000, Content: []timeline.RenderedContentPiece{{Type: "text", Text: "EDIT_UNCOVERED_BY_PARENT"}}}}
			if versioned {
				parent.Coverage[0].EventCursor = 10
				rc[0].LastEventCursor, rc[0].EditedAtMs = 11, 3000
			}
			hasEdit := func(result *timeline.ComposeContextResult) bool {
				for _, message := range result.Messages {
					if strings.Contains(message.Content, "EDIT_UNCOVERED_BY_PARENT") {
						return true
					}
				}
				return false
			}
			before := timeline.ComposeContextWithArtifacts(rc, nil, TimelineArtifacts([]Artifact{parent}))
			if !hasEdit(before) {
				t.Fatal("invalid fixture: parent already hid newer edit")
			}
			meta, err := rollupArtifactMetadata([]Artifact{parent}, artifactMetadata{Coverage: []byte("[]")})
			if err != nil {
				t.Fatal(err)
			}
			merged, err := DecodeArtifactCoverage(meta.Coverage)
			if err != nil {
				t.Fatal(err)
			}
			child := Artifact{ID: "child", Summary: "refolded original content only", StartedAt: time.UnixMilli(6000), AnchorEndMs: meta.AnchorEndMs, Coverage: merged, ParentIDs: []string{"parent"}}
			after := timeline.ComposeContextWithArtifacts(rc, nil, TimelineArtifacts([]Artifact{child}))
			if !hasEdit(after) {
				t.Fatalf("merely refolding a summary hid its uncovered edit on next-turn pipeline replay; parent as-of=%d child as-of=%d edit=%d", TimelineArtifacts([]Artifact{parent})[0].CoverageAsOfMs, TimelineArtifacts([]Artifact{child})[0].CoverageAsOfMs, rc[0].EditedAtMs)
			}
		})
	}
}

package compaction

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type ArtifactCatalog struct {
	frontiers map[ArtifactOwner]ArtifactFrontier
}

func NewArtifactCatalog() *ArtifactCatalog {
	return &ArtifactCatalog{frontiers: make(map[ArtifactOwner]ArtifactFrontier)}
}

func (c *ArtifactCatalog) Add(owner ArtifactOwner, frontier ArtifactFrontier) ArtifactFrontier {
	owner = normalizeArtifactOwner(owner)
	merged := MergeArtifactFrontiers(c.frontiers[owner], frontier)
	c.frontiers[owner] = merged
	return merged
}

func (c *ArtifactCatalog) Resolve(owner ArtifactOwner, lineageID string) (Artifact, bool) {
	frontier, ok := c.frontiers[normalizeArtifactOwner(owner)]
	if !ok {
		return Artifact{}, false
	}
	return frontier.Resolve(lineageID)
}

func (c *ArtifactCatalog) ResolveCoveredRef(owner ArtifactOwner, ref contextfrag.ContextRef) (Artifact, bool) {
	frontier, ok := c.frontiers[normalizeArtifactOwner(owner)]
	if !ok {
		return Artifact{}, false
	}
	return frontier.ResolveCoveredRef(ref)
}

func (c *ArtifactCatalog) ResolveCoverageIdentity(owner ArtifactOwner, ref contextfrag.ContextRef) (Artifact, bool) {
	frontier, ok := c.frontiers[normalizeArtifactOwner(owner)]
	if !ok {
		return Artifact{}, false
	}
	return frontier.ResolveCoverageIdentity(ref)
}

func normalizeArtifactOwner(owner ArtifactOwner) ArtifactOwner {
	owner.BotID = strings.TrimSpace(owner.BotID)
	owner.SessionID = strings.TrimSpace(owner.SessionID)
	owner.SessionIDKnown = owner.SessionIDKnown || owner.SessionID != ""
	return owner
}

func NewArtifactAliasFrontier(lineageID string, artifact Artifact) ArtifactFrontier {
	aliases := map[string]string{artifact.ID: artifact.ID}
	if id := strings.TrimSpace(lineageID); id != "" {
		aliases[id] = artifact.ID
	}
	coverage := make(map[string]artifactCoverageAlias, len(artifact.Coverage))
	for _, source := range artifact.Coverage {
		coverage[source.Ref.StableKey()] = artifactCoverageAlias{artifactID: artifact.ID, ref: source.Ref}
	}
	return ArtifactFrontier{
		Artifacts: []Artifact{artifact},
		aliases:   aliases,
		byID:      map[string]Artifact{artifact.ID: artifact},
		coverage:  coverage,
	}
}

func MergeArtifactFrontiers(frontiers ...ArtifactFrontier) ArtifactFrontier {
	merged := ArtifactFrontier{
		aliases:  make(map[string]string),
		byID:     make(map[string]Artifact),
		coverage: make(map[string]artifactCoverageAlias),
	}
	conflicted := make(map[string]struct{})
	seenIssues := make(map[string]struct{})
	addIssue := func(kind LineageIssueKind, left, right string) {
		if right < left {
			left, right = right, left
		}
		key := fmt.Sprintf("%s:%s:%s", kind, left, right)
		if _, seen := seenIssues[key]; seen {
			return
		}
		seenIssues[key] = struct{}{}
		merged.Issues = append(merged.Issues, LineageIssue{Kind: kind, ArtifactID: left, RelatedID: right})
	}
	for _, frontier := range frontiers {
		for _, issue := range frontier.Issues {
			key := fmt.Sprintf("%s:%s:%s", issue.Kind, issue.ArtifactID, issue.RelatedID)
			if _, seen := seenIssues[key]; seen {
				continue
			}
			seenIssues[key] = struct{}{}
			merged.Issues = append(merged.Issues, issue)
		}
		for id, artifact := range frontier.byID {
			if existing, ok := merged.byID[id]; ok && !reflect.DeepEqual(existing, artifact) {
				conflicted[id] = struct{}{}
				addIssue(LineageIssueAliasConflict, id, id)
				continue
			}
			merged.byID[id] = artifact
		}
		for alias, target := range frontier.aliases {
			if existing, ok := merged.aliases[alias]; ok && existing != target {
				conflicted[existing] = struct{}{}
				conflicted[target] = struct{}{}
				addIssue(LineageIssueAliasConflict, existing, target)
				continue
			}
			merged.aliases[alias] = target
		}
		for key, covered := range frontier.coverage {
			if existing, ok := merged.coverage[key]; ok &&
				(existing.artifactID != covered.artifactID || !compatibleCoverageRef(existing.ref, covered.ref) || !compatibleCoverageRef(covered.ref, existing.ref)) {
				conflicted[existing.artifactID] = struct{}{}
				conflicted[covered.artifactID] = struct{}{}
				addIssue(LineageIssueCoverageOverlap, existing.artifactID, covered.artifactID)
				continue
			}
			merged.coverage[key] = covered
		}
	}
	for id := range conflicted {
		delete(merged.byID, id)
	}
	for alias, target := range merged.aliases {
		if _, invalid := conflicted[target]; invalid {
			delete(merged.aliases, alias)
		}
	}
	for key, covered := range merged.coverage {
		if _, invalid := conflicted[covered.artifactID]; invalid {
			delete(merged.coverage, key)
		}
	}
	merged.Artifacts = make([]Artifact, 0, len(merged.byID))
	for _, artifact := range merged.byID {
		merged.Artifacts = append(merged.Artifacts, artifact)
	}
	sort.Slice(merged.Artifacts, func(i, j int) bool {
		left, right := merged.Artifacts[i], merged.Artifacts[j]
		if left.AnchorStartMs != right.AnchorStartMs {
			return left.AnchorStartMs < right.AnchorStartMs
		}
		if !left.StartedAt.Equal(right.StartedAt) {
			return left.StartedAt.Before(right.StartedAt)
		}
		return left.ID < right.ID
	})
	sort.Slice(merged.Issues, func(i, j int) bool {
		left, right := merged.Issues[i], merged.Issues[j]
		if left.ArtifactID != right.ArtifactID {
			return left.ArtifactID < right.ArtifactID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.RelatedID < right.RelatedID
	})
	return merged
}

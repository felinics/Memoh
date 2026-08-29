package compaction

import (
	"fmt"
	"sort"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type LineageIssueKind string

const (
	LineageIssueCycle                  LineageIssueKind = "cycle"
	LineageIssueMissingSuccessor       LineageIssueKind = "missing_successor"
	LineageIssueInactiveSuccessor      LineageIssueKind = "inactive_successor"
	LineageIssueInconsistentMarker     LineageIssueKind = "inconsistent_supersession_marker"
	LineageIssueParentMismatch         LineageIssueKind = "parent_mismatch"
	LineageIssueScopeMismatch          LineageIssueKind = "scope_mismatch"
	LineageIssueMissingDerivedCoverage LineageIssueKind = "missing_derived_coverage"
	LineageIssueCoverageMismatch       LineageIssueKind = "coverage_mismatch"
	LineageIssueCoverageOverlap        LineageIssueKind = "coverage_overlap"
	LineageIssueAliasConflict          LineageIssueKind = "alias_conflict"
	LineageIssueMalformedCoverage      LineageIssueKind = "malformed_coverage"
)

type LineageIssue struct {
	Kind       LineageIssueKind
	ArtifactID string
	RelatedID  string
}

func (i LineageIssue) Error() string {
	if i.RelatedID == "" {
		return fmt.Sprintf("compaction artifact %s: lineage %s", i.ArtifactID, i.Kind)
	}
	return fmt.Sprintf("compaction artifact %s: lineage %s (%s)", i.ArtifactID, i.Kind, i.RelatedID)
}

type LineageError struct {
	Issue LineageIssue
}

func (e *LineageError) Error() string {
	return e.Issue.Error()
}

type ArtifactFrontier struct {
	Artifacts []Artifact
	Issues    []LineageIssue
	aliases   map[string]string
	byID      map[string]Artifact
	coverage  map[string]artifactCoverageAlias
}

type ArtifactOwner struct {
	BotID          string
	SessionID      string
	SessionIDKnown bool
}

func (f ArtifactFrontier) Resolve(lineageID string) (Artifact, bool) {
	activeID, ok := f.aliases[strings.TrimSpace(lineageID)]
	if !ok {
		return Artifact{}, false
	}
	artifact, ok := f.byID[activeID]
	return artifact, ok
}

func (f ArtifactFrontier) ResolveCoveredRef(ref contextfrag.ContextRef) (Artifact, bool) {
	covered, ok := f.coverage[ref.StableKey()]
	if !ok || !compatibleCoverageRef(ref, covered.ref) {
		return Artifact{}, false
	}
	artifact, ok := f.byID[covered.artifactID]
	return artifact, ok
}

func (f ArtifactFrontier) ResolveCoverageIdentity(ref contextfrag.ContextRef) (Artifact, bool) {
	covered, ok := f.coverage[ref.StableKey()]
	if !ok {
		return Artifact{}, false
	}
	artifact, ok := f.byID[covered.artifactID]
	return artifact, ok
}

type artifactCoverageAlias struct {
	artifactID string
	ref        contextfrag.ContextRef
}

func buildArtifactFrontier(artifacts []Artifact) ArtifactFrontier {
	return buildArtifactFrontierForOwner(artifacts, ArtifactOwner{})
}

func buildArtifactFrontierForOwner(artifacts []Artifact, owner ArtifactOwner) ArtifactFrontier {
	nodes := make(map[string]Artifact, len(artifacts))
	for _, artifact := range artifacts {
		nodes[artifact.ID] = artifact
	}
	adjacent := lineageAdjacency(nodes)
	aliases := make(map[string]string)
	active := make(map[string]Artifact)
	issues := make([]LineageIssue, 0)
	seenIssues := make(map[string]struct{})
	invalid := make(map[string]struct{})
	resolver := loadedLineageResolver{
		nodes: nodes,
		memo:  make(map[string]loadedLineageResolution, len(nodes)),
		state: make(map[string]lineageVisitState, len(nodes)),
	}
	for _, artifact := range artifacts {
		if !artifactUsable(artifact) {
			continue
		}
		if artifactCoverageMalformed(artifact) {
			lineageIssue := LineageIssue{Kind: LineageIssueMalformedCoverage, ArtifactID: artifact.ID}
			key := fmt.Sprintf("%s:%s:%s", lineageIssue.Kind, lineageIssue.ArtifactID, lineageIssue.RelatedID)
			if _, seen := seenIssues[key]; !seen {
				seenIssues[key] = struct{}{}
				issues = append(issues, lineageIssue)
			}
			markConnectedLineage(artifact.ID, adjacent, invalid)
			continue
		}
		if !artifactMatchesOwner(artifact, owner) {
			ownerID := strings.TrimSpace(owner.BotID) + "/" + strings.TrimSpace(owner.SessionID)
			lineageIssue := LineageIssue{Kind: LineageIssueScopeMismatch, ArtifactID: artifact.ID, RelatedID: ownerID}
			key := fmt.Sprintf("%s:%s:%s", lineageIssue.Kind, lineageIssue.ArtifactID, lineageIssue.RelatedID)
			if _, seen := seenIssues[key]; !seen {
				seenIssues[key] = struct{}{}
				issues = append(issues, lineageIssue)
			}
			markConnectedLineage(artifact.ID, adjacent, invalid)
			continue
		}
		resolved := resolver.resolve(artifact.ID)
		if resolved.issue != nil {
			key := fmt.Sprintf("%s:%s:%s", resolved.issue.Kind, resolved.issue.ArtifactID, resolved.issue.RelatedID)
			if _, seen := seenIssues[key]; !seen {
				seenIssues[key] = struct{}{}
				issues = append(issues, *resolved.issue)
			}
			markConnectedLineage(artifact.ID, adjacent, invalid)
			continue
		}
		active[resolved.terminal.ID] = resolved.terminal
		aliases[artifact.ID] = resolved.terminal.ID
	}
	for id := range invalid {
		delete(active, id)
		delete(aliases, id)
	}
	coverageOwners := make(map[string]artifactCoverageAlias)
	for artifactID, artifact := range active {
		for _, source := range artifact.Coverage {
			key := source.Ref.StableKey()
			previous, exists := coverageOwners[key]
			if !exists || previous.artifactID == artifactID {
				coverageOwners[key] = artifactCoverageAlias{artifactID: artifactID, ref: source.Ref}
				continue
			}
			lineageIssue := LineageIssue{Kind: LineageIssueCoverageOverlap, ArtifactID: artifactID, RelatedID: previous.artifactID}
			issueKey := fmt.Sprintf("%s:%s:%s", lineageIssue.Kind, lineageIssue.ArtifactID, lineageIssue.RelatedID)
			if _, seen := seenIssues[issueKey]; !seen {
				seenIssues[issueKey] = struct{}{}
				issues = append(issues, lineageIssue)
			}
			markConnectedLineage(artifactID, adjacent, invalid)
			markConnectedLineage(previous.artifactID, adjacent, invalid)
		}
	}
	for id := range invalid {
		delete(active, id)
		delete(aliases, id)
	}
	coverageOwners = make(map[string]artifactCoverageAlias)
	for artifactID, artifact := range active {
		for _, source := range artifact.Coverage {
			coverageOwners[source.Ref.StableKey()] = artifactCoverageAlias{artifactID: artifactID, ref: source.Ref}
		}
	}

	frontier := ArtifactFrontier{
		Artifacts: make([]Artifact, 0, len(active)),
		Issues:    issues,
		aliases:   aliases,
		byID:      active,
		coverage:  coverageOwners,
	}
	for _, artifact := range active {
		frontier.Artifacts = append(frontier.Artifacts, artifact)
	}
	sort.Slice(frontier.Artifacts, func(i, j int) bool {
		left, right := frontier.Artifacts[i], frontier.Artifacts[j]
		if left.AnchorStartMs != right.AnchorStartMs {
			return left.AnchorStartMs < right.AnchorStartMs
		}
		if !left.StartedAt.Equal(right.StartedAt) {
			return left.StartedAt.Before(right.StartedAt)
		}
		return left.ID < right.ID
	})
	sort.Slice(frontier.Issues, func(i, j int) bool {
		left, right := frontier.Issues[i], frontier.Issues[j]
		if left.ArtifactID != right.ArtifactID {
			return left.ArtifactID < right.ArtifactID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.RelatedID < right.RelatedID
	})
	return frontier
}

func lineageAdjacency(nodes map[string]Artifact) map[string][]string {
	adjacent := make(map[string][]string, len(nodes))
	connect := func(left, right string) {
		if _, ok := nodes[left]; !ok {
			return
		}
		if _, ok := nodes[right]; !ok {
			return
		}
		adjacent[left] = append(adjacent[left], right)
		adjacent[right] = append(adjacent[right], left)
	}
	for _, artifact := range nodes {
		if artifact.SupersededBy != "" {
			connect(artifact.ID, artifact.SupersededBy)
		}
		for _, parentID := range artifact.ParentIDs {
			connect(artifact.ID, parentID)
		}
	}
	return adjacent
}

func markConnectedLineage(startID string, adjacent map[string][]string, marked map[string]struct{}) {
	queue := []string{startID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := marked[id]; seen {
			continue
		}
		marked[id] = struct{}{}
		queue = append(queue, adjacent[id]...)
	}
}

type lineageVisitState uint8

const (
	lineageVisiting lineageVisitState = iota + 1
	lineageVisited
)

type loadedLineageResolution struct {
	terminal Artifact
	issue    *LineageIssue
}

type loadedLineageResolver struct {
	nodes map[string]Artifact
	memo  map[string]loadedLineageResolution
	state map[string]lineageVisitState
}

func (r *loadedLineageResolver) resolve(id string) loadedLineageResolution {
	if resolved, ok := r.memo[id]; ok {
		return resolved
	}
	if r.state[id] == lineageVisiting {
		return loadedLineageResolution{issue: issue(LineageIssueCycle, id, id)}
	}
	r.state[id] = lineageVisiting
	current, ok := r.nodes[id]
	if !ok {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMissingSuccessor, id, id)})
	}
	if !artifactUsable(current) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueInactiveSuccessor, id, id)})
	}
	if artifactCoverageMalformed(current) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMalformedCoverage, current.ID, "")})
	}
	if markerIssue, invalid := validateSupersessionMarker(current); invalid {
		return r.finish(id, loadedLineageResolution{issue: &markerIssue})
	}
	if lineageCoverageMissing(current) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMissingDerivedCoverage, current.ID, "")})
	}
	parentCoverage := make([]CoveredSource, 0)
	for _, parentID := range current.ParentIDs {
		parent, exists := r.nodes[parentID]
		if !exists || !artifactUsable(parent) || parent.SupersededBy != current.ID {
			return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueParentMismatch, current.ID, parentID)})
		}
		if artifactCoverageMalformed(parent) {
			return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMalformedCoverage, parent.ID, "")})
		}
		if markerIssue, invalid := validateSupersessionMarker(parent); invalid {
			return r.finish(id, loadedLineageResolution{issue: &markerIssue})
		}
		if !sameArtifactScope(parent, current.BotID, current.SessionID) {
			return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueScopeMismatch, current.ID, parentID)})
		}
		if lineageCoverageMissing(parent) {
			return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMissingDerivedCoverage, parent.ID, "")})
		}
		parentCoverage = append(parentCoverage, parent.Coverage...)
	}
	if !coverageIncludes(current.Coverage, parentCoverage) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueCoverageMismatch, current.ID, strings.Join(current.ParentIDs, ","))})
	}
	if current.SupersededBy == "" {
		return r.finish(id, loadedLineageResolution{terminal: current})
	}
	next, exists := r.nodes[current.SupersededBy]
	if !exists {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueMissingSuccessor, current.ID, current.SupersededBy)})
	}
	if !artifactUsable(next) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueInactiveSuccessor, current.ID, next.ID)})
	}
	if !sameArtifactScope(next, current.BotID, current.SessionID) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueScopeMismatch, current.ID, next.ID)})
	}
	if !containsID(next.ParentIDs, current.ID) {
		return r.finish(id, loadedLineageResolution{issue: issue(LineageIssueParentMismatch, current.ID, next.ID)})
	}
	return r.finish(id, r.resolve(next.ID))
}

func (r *loadedLineageResolver) finish(id string, resolved loadedLineageResolution) loadedLineageResolution {
	r.state[id] = lineageVisited
	r.memo[id] = resolved
	return resolved
}

func artifactUsable(artifact Artifact) bool {
	return artifact.Status == "ok" && strings.TrimSpace(artifact.Summary) != ""
}

func validateSupersessionMarker(artifact Artifact) (LineageIssue, bool) {
	if (artifact.SupersededBy == "") != artifact.SupersededAt.IsZero() {
		return LineageIssue{Kind: LineageIssueInconsistentMarker, ArtifactID: artifact.ID, RelatedID: artifact.SupersededBy}, true
	}
	return LineageIssue{}, false
}

func lineageCoverageMissing(artifact Artifact) bool {
	participatesInLineage := len(artifact.ParentIDs) > 0 || artifact.SupersededBy != ""
	return participatesInLineage && len(artifact.Coverage) == 0
}

func artifactCoverageMalformed(artifact Artifact) bool {
	return artifact.CoverageMalformed ||
		(len(artifact.Coverage) > 0 && validatePersistedArtifactCoverage(artifact.Coverage) != nil)
}

func sameArtifactScope(artifact Artifact, botID, sessionID string) bool {
	return artifact.BotID == botID && artifact.SessionID == sessionID
}

func artifactMatchesOwner(artifact Artifact, owner ArtifactOwner) bool {
	botID := strings.TrimSpace(owner.BotID)
	if botID != "" && artifact.BotID != botID {
		return false
	}
	sessionID := strings.TrimSpace(owner.SessionID)
	return (!owner.SessionIDKnown && sessionID == "") || artifact.SessionID == sessionID
}

func coverageIncludes(coverage []CoveredSource, required []CoveredSource) bool {
	next := 0
	for _, expected := range required {
		found := false
		for next < len(coverage) {
			candidate := coverage[next]
			next++
			if compatibleCoveredSource(candidate, expected) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func compatibleCoveredSource(candidate, expected CoveredSource) bool {
	return compatibleCoverageRef(candidate.Ref, expected.Ref) &&
		candidate.ExternalMessageID == expected.ExternalMessageID &&
		candidate.SourceReplyToMessageID == expected.SourceReplyToMessageID &&
		candidate.CreatedAtMs == expected.CreatedAtMs
}

func compatibleCoverageRef(candidate contextfrag.ContextRef, expected contextfrag.ContextRef) bool {
	if !candidate.EqualIdentity(expected) {
		return false
	}
	if validatePersistedCoverageRef(candidate) != nil || validatePersistedCoverageRef(expected) != nil {
		return false
	}
	return candidate.HashAlgo == expected.HashAlgo &&
		candidate.HashScope == expected.HashScope &&
		candidate.ContentHash == expected.ContentHash
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func issue(kind LineageIssueKind, artifactID, relatedID string) *LineageIssue {
	return &LineageIssue{Kind: kind, ArtifactID: artifactID, RelatedID: relatedID}
}

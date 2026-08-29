package compaction

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type absorbedSegmentSource string

const (
	absorbedSourceRawTranscript  absorbedSegmentSource = "raw_transcript"
	absorbedSourceEarlierSummary absorbedSegmentSource = "earlier_summary"
)

const frontierRetainOverheadTokens = 1024

type absorbedSegment struct {
	Source  absorbedSegmentSource
	Content string
}

type absorbedRowsLoader func(context.Context, Artifact) ([]sqlc.ListUncompactedMessagesBySessionRow, error)

func shouldFuseFrontier(cfg TriggerConfig, artifacts []Artifact, maxCompactTokens int) bool {
	if !cfg.AllowFrontierFusion || len(artifacts) < 2 {
		return false
	}
	return frontierSummaryTokens(artifacts) > frontierRetainBudget(cfg, maxCompactTokens)
}

func frontierRetainBudget(cfg TriggerConfig, maxCompactTokens int) int {
	if cfg.ContextWindowTokens > 0 {
		return max(512, cfg.ContextWindowTokens*60/100-frontierRetainOverheadTokens)
	}
	return maxCompactTokens / 4
}

func frontierHasPersistedCoverage(artifacts []Artifact) bool {
	for _, artifact := range artifacts {
		if len(artifact.Coverage) == 0 {
			return false
		}
	}
	return true
}

func frontierSummaryTokens(artifacts []Artifact) int {
	summaries := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		summaries = append(summaries, artifact.Summary)
	}
	return priorContextTokens(summaries)
}

func rollupArtifactLevel(parents []Artifact) int {
	level := 0
	for _, parent := range parents {
		level = max(level, parent.Level+1)
	}
	return level
}

func allocateAbsorbBudgets(parents []Artifact, maxTokens int) ([]int, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	floor := absorbedSegmentOverheadTokens(absorbedSourceEarlierSummary) + estimateBytesAsTokens(truncationMarker)
	if maxTokens < floor*len(parents) {
		return nil, fmt.Errorf("%w: absorbed_segments=%d absorb_tokens=%d", errCompactionInputOverflow, len(parents), maxTokens)
	}
	weights := make([]int, len(parents))
	totalWeight := 0
	for i, parent := range parents {
		weights[i] = max(1, estimateBytesAsTokens(strings.TrimSpace(parent.Summary)))
		totalWeight += weights[i]
	}

	budgets := make([]int, len(parents))
	active := make([]int, len(parents))
	for i := range active {
		active[i] = i
	}
	remaining := maxTokens
	for len(active) > 0 {
		totalWeight = 0
		for _, i := range active {
			totalWeight += weights[i]
		}
		roundRemaining := remaining
		next := make([]int, 0, len(active))
		clamped := make([]int, 0, len(active))
		for _, i := range active {
			if int64(roundRemaining)*int64(weights[i]) < int64(floor)*int64(totalWeight) {
				clamped = append(clamped, i)
				continue
			}
			next = append(next, i)
		}
		if len(clamped) > 0 {
			for _, i := range clamped {
				budgets[i] = floor
			}
			remaining -= floor * len(clamped)
			active = next
			continue
		}

		type shareRemainder struct {
			index int
			value int64
		}
		remainders := make([]shareRemainder, 0, len(active))
		distributed := 0
		for _, i := range active {
			numerator := int64(remaining) * int64(weights[i])
			share := int(numerator / int64(totalWeight))
			budgets[i] = share
			distributed += share
			remainders = append(remainders, shareRemainder{index: i, value: numerator % int64(totalWeight)})
		}
		sort.SliceStable(remainders, func(i, j int) bool {
			return remainders[i].value > remainders[j].value
		})
		for i := 0; i < remaining-distributed; i++ {
			budgets[remainders[i].index]++
		}
		break
	}
	return budgets, nil
}

func buildAbsorbedContext(
	ctx context.Context,
	parents []Artifact,
	maxTokens int,
	load absorbedRowsLoader,
) ([]absorbedSegment, int, error) {
	budgets, err := allocateAbsorbBudgets(parents, maxTokens)
	if err != nil {
		return nil, 0, err
	}
	segments := make([]absorbedSegment, 0, len(parents))
	total := 0
	for i, parent := range parents {
		segment, rawOK := absorbedRawSegment(ctx, parent, budgets[i], load)
		if !rawOK {
			segment = absorbedSegment{
				Source:  absorbedSourceEarlierSummary,
				Content: truncateAbsorbedText(parent.Summary, budgets[i]),
			}
		}
		segments = append(segments, segment)
		total += absorbedSegmentTokens(segment)
	}
	return segments, total, nil
}

func absorbedRawSegment(ctx context.Context, parent Artifact, budget int, load absorbedRowsLoader) (absorbedSegment, bool) {
	if load == nil {
		return absorbedSegment{}, false
	}
	rows, err := load(ctx, parent)
	if err != nil || len(rows) == 0 {
		return absorbedSegment{}, false
	}
	items, _ := itemsFromRows(rows)
	if len(items) != len(rows) || !rawRowsCoverArtifact(items, parent) {
		return absorbedSegment{}, false
	}

	var raw strings.Builder
	for _, item := range items {
		content := strings.TrimSpace(renderCandidateEntry(item.Record))
		role := strings.TrimSpace(item.Record.ModelMessage.Role)
		if content == "" || role == "" {
			return absorbedSegment{}, false
		}
		fmt.Fprintf(&raw, "%s: %s\n", role, content)
	}
	segment := absorbedSegment{Source: absorbedSourceRawTranscript, Content: strings.TrimSpace(raw.String())}
	if absorbedSegmentTokens(segment) > budget {
		return absorbedSegment{}, false
	}
	return segment, true
}

func rawRowsCoverArtifact(items []CompactionCandidate, artifact Artifact) bool {
	if len(artifact.Coverage) == 0 {
		return true
	}
	if len(items) != len(artifact.Coverage) {
		return false
	}
	loaded := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := item.Record.Ref.StableKey()
		if key == "" {
			return false
		}
		loaded[key] = struct{}{}
	}
	if len(loaded) != len(artifact.Coverage) {
		return false
	}
	for _, source := range artifact.Coverage {
		if _, ok := loaded[source.Ref.StableKey()]; !ok {
			return false
		}
	}
	return true
}

func absorbedSegmentTokens(segment absorbedSegment) int {
	return absorbedSegmentOverheadTokens(segment.Source) + estimateBytesAsTokens(strings.TrimSpace(segment.Content))
}

func absorbedSegmentOverheadTokens(source absorbedSegmentSource) int {
	return estimateBytesAsTokens(absorbedSegmentHeader(source)+"\n") + priorSeparatorTokens
}

func truncateAbsorbedText(value string, maxTokens int) string {
	value = strings.TrimSpace(value)
	if absorbedSegmentTokens(absorbedSegment{Content: value}) <= maxTokens {
		return value
	}
	bodyTokens := maxTokens - absorbedSegmentOverheadTokens(absorbedSourceEarlierSummary)
	markerTokens := estimateBytesAsTokens(truncationMarker)
	if bodyTokens <= markerTokens {
		return truncationMarker
	}
	maxBytes := bodyTokens*4 - len(truncationMarker)
	for maxBytes > 0 {
		truncated := truncateBytes(value, maxBytes)
		if absorbedSegmentTokens(absorbedSegment{Content: truncated}) <= maxTokens {
			return truncated
		}
		maxBytes--
	}
	return truncationMarker
}

func summaryReplacementTokens(entries []messageEntry, parents []Artifact) int {
	total := entriesPromptCost(entries)
	for _, parent := range parents {
		total += estimateSummaryReplayTokens(parent.Summary)
	}
	return total
}

var errIncompleteAbsorbedCoverage = errors.New("compaction: rollup parent has no persisted coverage")

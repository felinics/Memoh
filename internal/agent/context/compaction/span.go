package compaction

import "strings"

// minCompactionSpanTokens is the smallest span worth a summarizer call. The
// prompt asks the model to keep facts, decisions, names and tool outcomes,
// and models spend well over a hundred tokens even on one short message
// (MEMOH-100: 71–280-token summaries for a 27-token span), so a smaller span
// cannot pass the net-reduction check. It stays raw in place instead.
const minCompactionSpanTokens = 256

// failureReasonIneffectiveSummary marks a claim whose summary was not shorter
// than its rows. Those rows stay raw within the compaction epoch and later
// passes select past them.
const failureReasonIneffectiveSummary = "ineffective_summary"

type groupKind int

const (
	groupMarkable groupKind = iota
	groupMustKeep
	groupOrphanResult
	groupUnrendered
	groupIneffective
)

// classifyGroup reports whether one tool-exchange group can be summarized and
// claimed. renders reports whether an item renders non-empty summarizer text.
// A result whose call is not in its group answers a call outside this claim,
// so claiming it alone would split the exchange; a group whose every row was
// already claimed by an ineffective summary would only produce that failure
// again.
func classifyGroup(items []CompactionCandidate, group []int, renders func(int) bool) groupKind {
	ineffective := true
	for _, idx := range group {
		if items[idx].HasPolicy(CompactPolicyMustKeep) {
			return groupMustKeep
		}
		ineffective = ineffective && items[idx].IneffectiveClaim
	}
	if isToolResultItem(items[group[0]]) {
		return groupOrphanResult
	}
	for _, idx := range group {
		if !renders(idx) {
			return groupUnrendered
		}
	}
	if ineffective {
		return groupIneffective
	}
	return groupMarkable
}

// groupCost classifies a group and, when it is markable, returns its
// summarizer-prompt cost (see markableGroupCost).
func groupCost(items []CompactionCandidate, group []int) (int, groupKind) {
	cost := 0
	kind := classifyGroup(items, group, func(idx int) bool {
		rendered := strings.TrimSpace(renderCandidateEntry(items[idx].Record))
		if rendered == "" {
			return false
		}
		cost += estimateBytesAsTokens(rendered) + estimateBytesAsTokens(items[idx].Record.ModelMessage.Role) + 1
		return true
	})
	return cost, kind
}

// spanStats records what one window held back from a claim, for diagnostics.
type spanStats struct {
	MustKeepGroups     int
	OrphanResultGroups int
	UnrenderedGroups   int
	IneffectiveGroups  int
	OpenGroups         int
	Gaps               int
	SmallSpans         int
	SpanTokens         int
}

func (s *spanStats) add(other spanStats) {
	s.MustKeepGroups += other.MustKeepGroups
	s.OrphanResultGroups += other.OrphanResultGroups
	s.UnrenderedGroups += other.UnrenderedGroups
	s.IneffectiveGroups += other.IneffectiveGroups
	s.OpenGroups += other.OpenGroups
	s.Gaps += other.Gaps
	s.SmallSpans += other.SmallSpans
	s.SpanTokens = other.SpanTokens
}

// spanChoice is where one pass claims rows: items[start:end] is the oldest
// span worth summarizing — consecutive markable groups with no gap between
// them. With no such span, start == end and resume is where a truncated read
// continues.
type spanChoice struct {
	start, end int
	resume     int
	stats      spanStats
}

// chooseSpan picks the oldest span worth one summarizer call. A span below
// minTokens is left raw in place. Barriers and gaps bound every span, so the
// rows a pass leaves behind never border a later claim.
//
// With truncated set, items are one read window with more candidates behind
// it: the last tool exchange may still have results past the edge, and the
// last span may still grow. A small last span is deferred — resume points at
// its start so the next window reads it whole — unless it fills the window,
// where deferring could not advance and its prefix is claimed instead.
func chooseSpan(items []CompactionCandidate, minTokens int, truncated bool) spanChoice {
	groups := toolExchangeGroups(items)
	costs := make([]int, len(groups))
	var stats spanStats
	for g, group := range groups {
		cost, kind := groupCost(items, group)
		switch kind {
		case groupMarkable:
			costs[g] = cost
		case groupMustKeep:
			stats.MustKeepGroups++
		case groupOrphanResult:
			stats.OrphanResultGroups++
		case groupUnrendered:
			stats.UnrenderedGroups++
		case groupIneffective:
			stats.IneffectiveGroups++
		}
		if g > 0 && items[group[0]].GapBefore {
			stats.Gaps++
		}
	}
	last := len(groups) - 1
	open := truncated && last >= 0 && costs[last] > 0 && items[groups[last][0]].HasPolicy(CompactPolicyPreserveToolClosure)
	if open {
		costs[last] = 0
		stats.OpenGroups++
	}
	reachesEdge := func(end int) bool {
		return end == len(groups) || (open && end == last && !items[groups[last][0]].GapBefore)
	}

	choice := spanChoice{resume: len(items)}
	if open {
		choice.resume = groups[last][0]
	}
	for g := 0; g < len(groups); {
		if costs[g] == 0 {
			g++
			continue
		}
		first, cost := g, costs[g]
		for g++; g < len(groups) && costs[g] > 0 && !items[groups[g][0]].GapBefore; g++ {
			cost += costs[g]
		}
		growing := truncated && reachesEdge(g)
		if cost < minTokens && (!growing || first > 0) {
			if growing {
				choice.resume = groups[first][0]
				break
			}
			stats.SmallSpans++
			continue
		}
		stats.SpanTokens = cost
		choice.start = groups[first][0]
		choice.end = groups[g-1][len(groups[g-1])-1] + 1
		break
	}
	choice.stats = stats
	return choice
}

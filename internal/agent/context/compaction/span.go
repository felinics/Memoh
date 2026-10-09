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
)

// classifyGroup reports whether one tool-exchange group can be summarized and
// claimed. renders reports whether an item renders non-empty summarizer text.
// A result whose call is not in its group answers a call outside this claim,
// so claiming it alone would split the exchange.
func classifyGroup(items []CompactionCandidate, group []int, renders func(int) bool) groupKind {
	for _, idx := range group {
		if items[idx].HasPolicy(CompactPolicyMustKeep) {
			return groupMustKeep
		}
	}
	if isToolResultItem(items[group[0]]) {
		return groupOrphanResult
	}
	for _, idx := range group {
		if !renders(idx) {
			return groupUnrendered
		}
	}
	return groupMarkable
}

// provedIneffective reports that every row of a group was already claimed by
// a summary that did not shrink them. Sent again on their own they would only
// fail again; next to new rows they get another chance.
func provedIneffective(items []CompactionCandidate, group []int) bool {
	for _, idx := range group {
		if !items[idx].IneffectiveClaim {
			return false
		}
	}
	return true
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
	OpenGroups         int
	Gaps               int
	SmallSpans         int
	IneffectiveSpans   int
	SpanTokens         int
}

func (s *spanStats) add(other spanStats) {
	s.MustKeepGroups += other.MustKeepGroups
	s.OrphanResultGroups += other.OrphanResultGroups
	s.UnrenderedGroups += other.UnrenderedGroups
	s.OpenGroups += other.OpenGroups
	s.Gaps += other.Gaps
	s.SmallSpans += other.SmallSpans
	s.IneffectiveSpans += other.IneffectiveSpans
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

// chooseSpan picks the oldest span worth one summarizer call: at least
// minTokens of rows not already proved ineffective. Proved rows ride along
// with new ones but never count toward the floor. Other spans stay raw in
// place. Barriers and gaps bound every span, and the read query
// marks the row after a claim with a gap, so rows left behind never join a
// later claim across it.
//
// With truncated set, items are one read window with more candidates behind
// it: the last tool exchange may still have results past the edge, and the
// last span may still grow. Such a span is deferred and resume points at its
// start, so the next window reads it whole — unless it starts the window,
// where re-reading could not advance and the window is passed instead.
func chooseSpan(items []CompactionCandidate, minTokens int, truncated bool) spanChoice {
	groups := toolExchangeGroups(items)
	costs := make([]int, len(groups))
	fresh := make([]bool, len(groups))
	var stats spanStats
	for g, group := range groups {
		cost, kind := groupCost(items, group)
		switch kind {
		case groupMarkable:
			costs[g] = cost
			fresh[g] = !provedIneffective(items, group)
		case groupMustKeep:
			stats.MustKeepGroups++
		case groupOrphanResult:
			stats.OrphanResultGroups++
		case groupUnrendered:
			stats.UnrenderedGroups++
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
	if open && groups[last][0] > 0 {
		choice.resume = groups[last][0]
	}
	for g := 0; g < len(groups); {
		if costs[g] == 0 {
			g++
			continue
		}
		first, cost, freshCost := g, 0, 0
		for ; g < len(groups) && costs[g] > 0 && (g == first || !items[groups[g][0]].GapBefore); g++ {
			cost += costs[g]
			if fresh[g] {
				freshCost += costs[g]
			}
		}
		if freshCost > 0 && freshCost >= minTokens {
			stats.SpanTokens = cost
			choice.start = groups[first][0]
			choice.end = groups[g-1][len(groups[g-1])-1] + 1
			break
		}
		if truncated && reachesEdge(g) {
			if groups[first][0] > 0 {
				choice.resume = groups[first][0]
			}
			break
		}
		if freshCost > 0 {
			stats.SmallSpans++
		} else {
			stats.IneffectiveSpans++
		}
	}
	choice.stats = stats
	return choice
}

// trimSpan caps a chosen span to the entries budget, oldest groups first.
// When the budget leaves less than minTokens of rows not yet proved
// ineffective, the claim starts at the span's first such group instead, so a
// pass never resends what failed before without enough new rows to change
// the outcome.
func trimSpan(span []CompactionCandidate, budget, minTokens int) []CompactionCandidate {
	trimmed := trimCompactMessages(span, budget)
	if freshCost(trimmed) >= max(1, minTokens) {
		return trimmed
	}
	for _, group := range toolExchangeGroups(span) {
		if !provedIneffective(span, group) {
			return trimCompactMessages(span[group[0]:], budget)
		}
	}
	return nil
}

func freshCost(items []CompactionCandidate) int {
	total := 0
	for _, group := range toolExchangeGroups(items) {
		if !provedIneffective(items, group) {
			total += markableGroupCost(items, group)
		}
	}
	return total
}

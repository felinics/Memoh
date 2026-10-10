package compaction

import (
	"strings"
	"time"
)

// minCompactionSpanTokens is the smallest span worth a summarizer call. The
// prompt asks the model to keep facts, decisions, names and tool outcomes,
// and models spend well over a hundred tokens even on one short message
// (MEMOH-100: 71–280-token summaries for a 27-token span), so a smaller span
// cannot pass the net-reduction check. It stays raw in place instead.
const minCompactionSpanTokens = 256

// failureReasonIneffectiveSummary marks a claim whose summary was not
// shorter than its rows. Those rows stay raw within the compaction epoch and
// later passes select past them.
const failureReasonIneffectiveSummary = "ineffective_summary"

// failureReasonUnusableSummary marks a claim the model returned no usable
// summary for: empty, cut off, or refused. That may pass, so its rows are
// held back only for a while, as later passes select past them, and are then
// tried again: for unusableSummaryHold, four times as long after each
// consecutive such attempt on them, up to maxUnusableSummaryHold.
const failureReasonUnusableSummary = "unusable_summary"

const (
	unusableSummaryHold    = 15 * time.Minute
	maxUnusableSummaryHold = 6 * time.Hour
)

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
// continues. Every row in items[:settled] is permanently unclaimable in this
// epoch: a barrier, or a span closed by one that stays below the floor or is
// made only of rows proved ineffective.
type spanChoice struct {
	start, end int
	resume     int
	settled    int
	// grow reports a window filled from its first row by one span or tool
	// exchange that may continue past the edge: only a larger window can
	// read it whole without cutting it.
	grow  bool
	stats spanStats
}

// chooseSpan picks the oldest span worth one summarizer call: at least
// minTokens of rows not already proved ineffective, which trimSpan can still
// claim within budget. Proved rows ride along with new ones but never count
// toward the floor. Other spans stay raw in place, and so does a row marked
// PreserveRecent, which also stops the settled prefix: the current task and
// the rows held with it are protected only for now. Barriers and gaps bound
// every span, and the read query marks the row after a claim with a gap, so
// rows left behind never join a later claim across it.
//
// With truncated set, items are one read window with more candidates behind
// it: the last tool exchange may still have results past the edge, and the
// last span may still grow. Such a span is deferred and resume points at its
// start, so the next window reads it whole. When it starts the window, grow
// asks for a larger window instead; at the largest one the window is passed
// over and settled, since every pass cuts it the same.
func chooseSpan(items []CompactionCandidate, minTokens, budget int, truncated bool) spanChoice {
	groups := toolExchangeGroups(items)
	costs := make([]int, len(groups))
	fresh := make([]bool, len(groups))
	recent := len(groups)
	var stats spanStats
	for g, group := range groups {
		cost, kind := groupCost(items, group)
		switch {
		case kind == groupMarkable && items[group[0]].HasPolicy(CompactPolicyPreserveRecent):
			// The current task or a row held with it: held back for now,
			// not for good.
			recent = min(recent, g)
		case kind == groupMarkable:
			costs[g] = cost
			fresh[g] = !provedIneffective(items, group)
		case kind == groupMustKeep:
			stats.MustKeepGroups++
		case kind == groupOrphanResult:
			stats.OrphanResultGroups++
		case kind == groupUnrendered:
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

	choice := spanChoice{resume: len(items), settled: len(items), grow: open && last == 0}
	if open && groups[last][0] > 0 {
		choice.resume = groups[last][0]
		choice.settled = groups[last][0]
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
		if trimSpan(items[groups[first][0]:groups[g-1][len(groups[g-1])-1]+1], budget, minTokens) != nil {
			stats.SpanTokens = cost
			choice.start = groups[first][0]
			choice.end = groups[g-1][len(groups[g-1])-1] + 1
			choice.settled = choice.start
			break
		}
		if reachesEdge(g) {
			// The last span may still grow: in the next window, or when the
			// kept recent tail moves past it. A window it fills alone is
			// passed over instead, since re-reading it could not advance.
			if !truncated || groups[first][0] > 0 {
				choice.settled = groups[first][0]
				if truncated {
					choice.resume = groups[first][0]
				}
			} else {
				choice.grow = true
			}
			break
		}
		if freshCost > 0 && freshCost < max(1, minTokens) {
			stats.SmallSpans++
		} else {
			stats.IneffectiveSpans++
		}
	}
	if recent < len(groups) {
		// Rows right in front of the current task may join it once it is no
		// longer current, so they are not settled either.
		before := recent
		for before > 0 && costs[before-1] > 0 && !items[groups[before][0]].GapBefore {
			before--
		}
		choice.settled = min(choice.settled, groups[before][0])
	}
	choice.stats = stats
	return choice
}

// trimSpan caps a chosen span to the entries budget, oldest groups first,
// keeping in the claim at least minTokens of rows not yet proved ineffective,
// and at least as many of them as of proved rows: when the budget cut leaves
// less, the claim starts at a later group of the span. Proved rows thus get
// another chance only next to as many new ones. The claim is the longest such
// one from its start, so a larger budget never claims less. Rows passed over
// stay raw in place, and the row after the claim reads as a gap, so they never
// join a later claim across it.
func trimSpan(span []CompactionCandidate, budget, minTokens int) []CompactionCandidate {
	groups := toolExchangeGroups(span)
	costs := make([]int, len(groups))
	proved := make([]int, len(groups))
	for g, group := range groups {
		costs[g] = markableGroupCost(span, group)
		if provedIneffective(span, group) {
			proved[g] = costs[g]
		}
	}
	need := max(1, minTokens)
	for start := range groups {
		total, provedTotal, last := 0, 0, -1
		for end := start; end < len(groups) && (end == start || total+costs[end] <= budget); end++ {
			total += costs[end]
			provedTotal += proved[end]
			if fresh := total - provedTotal; fresh >= need && provedTotal <= fresh {
				last = end
			}
		}
		if last >= 0 {
			return span[groups[start][0] : groups[last][len(groups[last])-1]+1]
		}
	}
	return nil
}

// retrySpan halves a claim that starts with rows whose summary came back
// unusable more than once: it claims the first half of them when both halves
// still clear floor. A row the model keeps refusing thus ends up retried
// alone within a few attempts, instead of holding back every row once
// claimed with it.
func retrySpan(span []CompactionCandidate, floor int) []CompactionCandidate {
	if len(span) == 0 || span[0].UnusableAttempts < 2 {
		return span
	}
	groups := toolExchangeGroups(span)
	var costs []int
	total := 0
	for _, group := range groups {
		if span[group[0]].UnusableAttempts == 0 {
			break
		}
		costs = append(costs, markableGroupCost(span, group))
		total += costs[len(costs)-1]
	}
	for g, have := 0, 0; g < len(costs)-1; g++ {
		if have += costs[g]; have >= floor && 2*have >= total && total-have >= floor {
			last := groups[g]
			return span[:last[len(last)-1]+1]
		}
	}
	return span
}

// closeRun extends the claimable prefix items[:n] — what the recent tail
// leaves — over the rest of its run when a barrier or a gap closes that rest
// below floor: left raw, it would stay alone between this claim and the
// barrier for good once the tail moves past it. A rest that reaches the end
// of the window may still grow and stays; rows held with the current task are
// never claimed either way.
func closeRun(items []CompactionCandidate, n, floor int) int {
	if n == 0 || n >= len(items) || items[n].GapBefore {
		return n
	}
	rest := items[n:]
	cost := 0
	for _, group := range toolExchangeGroups(rest) {
		if group[0] > 0 && rest[group[0]].GapBefore {
			return n + group[0]
		}
		c, kind := groupCost(rest, group)
		if kind != groupMarkable {
			return n + group[0]
		}
		if cost += c; cost >= floor {
			return n
		}
	}
	return n
}

// holdJoint returns items[from:to], the rows that stay raw with the current
// task at items[task] while it is current: the steps right after it until
// they and the task are worth a call together. When a barrier, a gap or a
// step whose replay dwarfs its summarizer entry ends those first, or while a
// running turn's steps do not reach that yet, the rows right in front of the
// task are held too, as far as needed. Once the turn is over, they form one
// claimable span instead of the task staying behind alone between two
// summaries. open reports steps that ran out before the target, which more
// steps may still extend.
func holdJoint(items []CompactionCandidate, task, target int) (from, to int, open bool) {
	groups := toolExchangeGroups(items)
	t := 0
	for groups[t][0] != task {
		t++
	}
	have := estimateBytesAsTokens(strings.TrimSpace(renderCandidateEntry(items[task].Record)))
	join := func(g int) bool {
		cost, replay := markableGroupCost(items, groups[g]), 0
		for _, idx := range groups[g] {
			replay += (len(items[idx].RawContent) + 3) / 4
		}
		// Most of such a step never reaches the summarizer: held raw, it
		// costs far more context than the joint is worth.
		if cost == 0 || replay > 4*cost {
			return false
		}
		have += cost
		return true
	}
	g := t + 1
	for ; g < len(groups) && have < target; g++ {
		if items[groups[g][0]].GapBefore || !join(g) {
			break
		}
	}
	to = len(items)
	if g < len(groups) {
		to = groups[g][0]
	}
	open = g == len(groups) && have < target
	from = task
	if open && (g == t+1 || !items[groups[g-1][0]].HasPolicy(CompactPolicyPreserveToolClosure)) {
		// No step yet, or the turn already answered: the run in front of the
		// task simply continues into the next one. A turn whose latest step
		// is a tool exchange is still running, and its next step may end the
		// joint.
		return from, to, open
	}
	for b := t; b > 0 && have < target && !items[groups[b][0]].GapBefore && join(b-1); b-- {
		from = groups[b-1][0]
	}
	return from, to, open
}

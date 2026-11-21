package compaction

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

const (
	// roomShare is the share of a request's room (fixedRoom) a fold keeps
	// verbatim within its budget, and the share the instruction asks its
	// summary to stay within, so a fold that gets such a summary leaves the
	// steps after it room instead of refilling the request it just emptied.
	roomShare = 4
	// verbatimCapTokens is the most a fold keeps verbatim of what it may
	// fold: anchors newest first, then the newest complete call groups. It
	// holds three results at the tool output bound, or two beside a long
	// input; a small window keeps at most a quarter of its room (fixedRoom).
	verbatimCapTokens = 32000
	// minForkOutputTokens is the least output a fork request asks for of a
	// summarizer whose output is not fixed. A pass reads less of the request
	// rather than ask for less.
	minForkOutputTokens = 1024
	// instructionNumberSlack covers the instruction growing by the digits of
	// the numbers it states.
	instructionNumberSlack = 8
	// forkInstructionReserve is what a fork keeps for its instruction when
	// it bounds the messages it reads.
	forkInstructionReserve = 1024
	// minSummaryTokens is the least room a summary must have to be worth
	// writing: below it a pass fails before calling the summarizer.
	minSummaryTokens = 32
)

// AnchorPolicy is how a fold keeps a request's anchors.
type AnchorPolicy int

const (
	// AnchorsCapped keeps the anchors after the newest call group, the input
	// and steers the next call answers, whatever they take, and the older
	// ones newest first within the verbatim budget.
	AnchorsCapped AnchorPolicy = iota
	// AnchorsFirst keeps the inputs a run answers, a Discuss batch, newest
	// first while they leave a tenth of the room for the summary; call groups
	// take what they leave of the verbatim budget.
	AnchorsFirst
)

// verbatimBudget is the most ev keeps verbatim of what it may fold: its
// share of the room, within verbatimCapTokens.
func verbatimBudget(ev Event) int {
	return min(verbatimCapTokens, fixedRoom(ev)/roomShare)
}

// summaryBudget is the length the instruction asks a summary of ev to stay
// within: its share of the room. It is a target; what a pass requires of the
// summary is foldPass's.
func summaryBudget(ev Event) int {
	return fixedRoom(ev) / roomShare
}

// fixedRoom is what ev's allowance leaves beside what every request carries:
// the system prompt, the tools and the runtime context.
func fixedRoom(ev Event) int {
	var runtime []sdk.Message
	for i, message := range ev.Request.Messages {
		if len(ev.Sources[i].Sources) == 0 {
			runtime = append(runtime, message)
		}
	}
	return ev.Allowance - forkTokens(sdk.Request{System: ev.Request.System, Messages: runtime, Tools: ev.Request.Tools})
}

// retention is what an event keeps verbatim of what it may fold, decided
// before its first pass: the anchors it keeps where they are, and cut, the
// first of the newest messages it keeps after the summary.
type retention struct {
	cut     int
	anchors map[turn.ContextMessageSource]bool
	// keptGroups and latestGroupTokens describe the call groups it keeps,
	// for the event's log.
	keptGroups        int
	latestGroupTokens int
}

// keepTokens is what keeping message verbatim takes: what a request prices it
// at, or what a history load admits it by, its encoded bytes, whichever is
// more. A request prices an inline image flat, but the image fills a load
// bounded in bytes; kept, it would leave the history that load cannot hold.
func keepTokens(message sdk.Message) int {
	tokens := forkTokens(sdk.Request{Messages: []sdk.Message{message}})
	if encoded, err := json.Marshal(message); err == nil {
		tokens = max(tokens, contextfrag.TokensFromBytes(len(encoded)))
	}
	return tokens
}

func (r retention) keeps(source Source) bool {
	return source.Anchor && slices.ContainsFunc(source.Sources, func(s turn.ContextMessageSource) bool {
		return r.anchors[turn.ContextMessageSource{Kind: s.Kind, ID: s.ID}]
	})
}

// retain decides what ev keeps verbatim. Runtime context stays and takes
// nothing; everything else ev keeps takes from one budget. The messages no
// context row holds stay whatever they take, and so, under AnchorsCapped, do
// the anchors after the newest call group; then anchors newest first, then
// the newest complete call groups, a call with all its results, up to the
// first that does not fit. Everything else, summaries included, folds. What
// a message takes is keepTokens.
func retain(ev Event, unfoldable map[int]bool) retention {
	messages, sources := ev.Request.Messages, ev.Sources
	cost := func(from, to int) int {
		tokens := 0
		for i := from; i < to; i++ {
			if len(sources[i].Sources) > 0 && !unfoldable[i] {
				tokens += keepTokens(messages[i])
			}
		}
		return tokens
	}
	fixed := func(i int) bool { return len(sources[i].Sources) == 0 || unfoldable[i] }
	r := retention{cut: len(messages), anchors: map[turn.ContextMessageSource]bool{}}
	budget := verbatimBudget(ev)
	for i := range messages {
		if len(sources[i].Sources) > 0 && unfoldable[i] {
			budget -= keepTokens(messages[i])
		}
	}
	if ev.AnchorPolicy == AnchorsCapped {
		for r.cut > 0 && (fixed(r.cut-1) || sources[r.cut-1].Anchor) {
			r.cut--
		}
		budget -= cost(r.cut, len(messages))
	}
	anchorBudget := budget
	if ev.AnchorPolicy == AnchorsFirst {
		room := fixedRoom(ev)
		anchorBudget = room - room/10
	}
	used := 0
	for i := r.cut - 1; i >= 0; i-- {
		if !sources[i].Anchor || fixed(i) {
			continue
		}
		tokens := cost(i, i+1)
		if used+tokens > anchorBudget {
			break
		}
		used += tokens
		for _, s := range sources[i].Sources {
			r.anchors[turn.ContextMessageSource{Kind: s.Kind, ID: s.ID}] = true
		}
	}
	budget -= used
	latest := true
	for at := r.cut; at > 0; {
		start := at - 1
		for start > 0 && !foldBoundary(messages, start) {
			start--
		}
		group, stop := false, false
		for i := start; i < at; i++ {
			switch {
			case fixed(i) || r.keeps(sources[i]):
			case isSummarySource(sources[i]) || sources[i].Anchor:
				stop = true
			default:
				group = true
			}
		}
		if stop {
			break
		}
		if group {
			tokens := cost(start, at)
			if latest {
				r.latestGroupTokens, latest = tokens, false
			}
			if tokens > budget {
				break
			}
			budget -= tokens
			r.keptGroups++
			r.cut = start
		}
		at = start
	}
	return r
}

// foldPlan is one pass worked out before the summarizer is asked: what it
// reads and folds, and how long its summary may be.
type foldPlan struct {
	// end is how much of the request the pass reads; cut is the first of the
	// newest messages the event keeps. A pass that reads less than the cut
	// leaves the rest to a later pass.
	end int
	cut int
	// folded are the sources the summary stands for, read every source of
	// the messages the pass reads, absorbed the summaries it replaces.
	folded   []turn.ContextMessageSource
	read     []turn.ContextMessageSource
	absorbed []string
	// replaced is what the folded messages and absorbed summaries take in a
	// request, room what the next request has left for the summary.
	replaced int
	room     int
	// maxTokens is the fork's output cap and stated the summary length the
	// instruction asks for, below it.
	maxTokens   int
	stated      int
	instruction string
	// keep marks the messages before end the pass leaves verbatim: the
	// anchors it keeps and the messages no context row holds.
	keep map[int]bool
	// rewrite marks a request with nothing left to fold but its summaries:
	// a pass only rewrites them to fit the room the next request has.
	rewrite bool
	// fork is what the pass sends its summarizer of the request before the
	// instruction (forkRead).
	fork []sdk.Message
}

func (p foldPlan) folds() bool { return len(p.folded) > 0 || len(p.absorbed) > 0 }

// order is ev's request after the pass, as Pass.Order names it, and where the
// newest messages the event keeps start in it. The pass replaces every
// message it reads that renders a source and that it does not keep: what it
// folds and the summaries it absorbs. Everything after it and runtime
// context stay.
func (p foldPlan) order(ev Event) ([]int, int) {
	order := make([]int, len(ev.Request.Messages))
	for i := range order {
		order[i] = i
	}
	order, _ = turn.PlaceSummary(order, -1, func(i int) bool {
		return i >= 0 && i < p.end && len(ev.Sources[i].Sources) > 0 && !p.keep[i]
	})
	cut := slices.Index(order, ev.cut)
	if cut < 0 {
		cut = len(order)
	}
	return order, cut
}

// final reports whether the pass reads up to the newest messages the event
// keeps, so its summary is the one the caller's next request carries.
func (p foldPlan) final() bool { return p.end == p.cut }

// answers reports whether the fork leaves the summarizer room to answer: all
// of its output when that is fixed, else, within its cap, at least half of
// the summary what the pass reads may need and never less than
// minForkOutputTokens.
func (p foldPlan) answers(ev Event) bool {
	need := ev.MaxOutputTokens
	if !ev.FixedOutput {
		need = min(need, max(minForkOutputTokens, min(summaryBudget(ev), p.replaced)/2))
	}
	return p.maxTokens >= need
}

// leastOutput is the least output a fork of ev asks for: all of the
// summarizer's when it is fixed, else minForkOutputTokens within its cap.
func leastOutput(ev Event) int {
	if ev.FixedOutput {
		return ev.MaxOutputTokens
	}
	return min(ev.MaxOutputTokens, minForkOutputTokens)
}

// bounded reports whether the pass's summary must fit the room the next
// request has for it: the final pass's, and a rewrite's, which makes
// progress only by leaving a summary that fits.
func (p foldPlan) bounded() bool { return p.final() || p.rewrite }

// planFold decides one pass over ev. Every message before the cut folds
// except runtime context, which has no sources, the messages no context row
// holds, and the anchors ev keeps; summaries are absorbed. When
// nothing but summaries is left, a pass rewrites them only if they do not fit
// the room the next request has. The pass reads the longest prefix of the
// request, ending at a tool-group boundary, that leaves the fork room to
// answer (answers). The longest read decides first, as what it keeps may
// leave no room for any summary; past it, a shorter read takes less of the
// window and needs less output, so the reads that answer are the shortest
// ones, and the pass halves its way to the longest of them rather than
// pricing every read.
func planFold(ev Event, unfoldable map[int]bool) (foldPlan, error) {
	messages, sources := ev.Request.Messages, ev.Sources
	kept := map[int]bool{}
	for i := range ev.cut {
		kept[i] = unfoldable[i] || ev.keeps(sources[i])
	}
	// A pass ends its read at a tool-group boundary after the first message
	// it folds or absorbs and, as a checkpoint absorbs every summary of its
	// parent, after the last summary the request renders.
	first, summariesEnd := ev.cut, 0
	for i, source := range sources[:ev.cut] {
		summary := isSummarySource(source)
		if summary {
			summariesEnd = i + 1
		}
		if first == ev.cut && (summary || len(source.Sources) > 0 && !kept[i]) {
			first = i
		}
	}
	var ends []int
	for end := max(first+1, summariesEnd); end <= ev.cut; end++ {
		if foldBoundary(messages, end) {
			ends = append(ends, end)
		}
	}
	if len(ends) == 0 {
		return foldPlan{}, nil
	}
	rewrite := len(spanPlan(ev, kept, ev.cut).folded) == 0
	read, err := forkRead(ev)
	if err != nil {
		return foldPlan{}, err
	}
	plan := func(end int) (foldPlan, error) {
		p := spanPlan(ev, kept, end)
		p.rewrite, p.fork = rewrite, read[:end]
		return p, p.size(ev)
	}
	pass, err := plan(ends[len(ends)-1])
	switch {
	case ReasonOf(err) == ReasonProtectedOverflow:
		return foldPlan{}, err
	case rewrite && pass.replaced <= pass.room:
		// Only summaries are left, and they fit: folding them again would
		// only rewrite them.
		return foldPlan{}, nil
	case err == nil && !pass.answers(ev):
		answering := sort.Search(len(ends)-1, func(i int) bool {
			p, _ := plan(ends[i])
			return !p.answers(ev)
		})
		pass, err = plan(ends[max(answering-1, 0)])
		if err == nil && answering == 0 {
			return foldPlan{}, failed(ReasonInputOverflow, fmt.Errorf("%w: no prefix of the request leaves the summarizer room to answer in a %d-token window", errCompactionInputOverflow, ev.Window))
		}
	}
	if err != nil {
		// What is left is too little for any summary to replace; once the
		// request fits, the event is done.
		if forkTokens(ev.Request) <= ev.Allowance {
			return foldPlan{}, nil
		}
		return foldPlan{}, err
	}
	return pass, nil
}

// foldBoundary reports whether a pass may end its read before message end: a
// tool result never starts what a pass leaves, so a call and its results
// stay on one side.
func foldBoundary(messages []sdk.Message, end int) bool {
	if end <= 0 || end > len(messages) {
		return false
	}
	if end == len(messages) {
		return true
	}
	for _, part := range messages[end].Content {
		if _, ok := part.(sdk.ToolResultPart); ok {
			return false
		}
	}
	return true
}

// spanPlan classifies the messages a pass reading up to end folds.
func spanPlan(ev Event, kept map[int]bool, end int) foldPlan {
	plan := foldPlan{end: end, cut: ev.cut, keep: map[int]bool{}}
	read, folded := map[turn.ContextMessageSource]bool{}, map[turn.ContextMessageSource]bool{}
	for i := range end {
		source := ev.Sources[i]
		switch {
		case len(source.Sources) == 0:
			continue
		case isSummarySource(source):
			plan.absorbed = append(plan.absorbed, source.Sources[0].ID)
			continue
		}
		for _, s := range source.Sources {
			key := turn.ContextMessageSource{Kind: s.Kind, ID: s.ID}
			if !read[key] {
				read[key] = true
				plan.read = append(plan.read, key)
			}
		}
		if kept[i] {
			plan.keep[i] = true
			continue
		}
		for _, s := range source.Sources {
			key := turn.ContextMessageSource{Kind: s.Kind, ID: s.ID}
			if !folded[key] {
				folded[key] = true
				plan.folded = append(plan.folded, key)
			}
		}
	}
	return plan
}

// size prices the pass: what it replaces, the room the next request leaves
// its summary, the fork's output cap and the summary length to ask for.
func (p *foldPlan) size(ev Event) error {
	capability := ev.MaxOutputTokens
	messages := ev.Request.Messages
	var replaced, keptMessages []sdk.Message
	for i, message := range messages {
		if i < p.end && len(ev.Sources[i].Sources) > 0 && !p.keep[i] {
			replaced = append(replaced, message)
		} else {
			keptMessages = append(keptMessages, message)
		}
	}
	p.replaced = forkTokens(sdk.Request{Messages: replaced})
	p.room = ev.Allowance - forkTokens(sdk.Request{System: ev.Request.System, Messages: keptMessages, Tools: ev.Request.Tools})
	prefix := forkTokens(sdk.Request{System: ev.Request.System, Messages: p.fork, Tools: ev.Request.Tools})
	p.instruction = forkInstruction(ev, p)
	// The instruction is priced as a second request carries it, with the
	// reminder after a tool call.
	p.maxTokens = min(capability, ev.Window-prefix-instructionTokens(p.instruction+noToolsReminder)-instructionNumberSlack)
	p.stated = min(p.maxTokens*3/4, summaryBudget(ev), summaryOutputLimit(p.replaced-1, capability))
	if p.bounded() {
		p.stated = min(p.stated, summaryOutputLimit(p.room, capability))
	}
	if p.bounded() && summaryOutputLimit(p.room, capability) < minSummaryTokens {
		return failed(ReasonProtectedOverflow, fmt.Errorf("%w: allowance=%d room=%d", errNoSummaryRoom, ev.Allowance, p.room))
	}
	if p.stated < minSummaryTokens && p.maxTokens >= minForkOutputTokens {
		return failed(ReasonIneffective, fmt.Errorf("%w: replaced_tokens=%d", errIneffectiveSummary, p.replaced))
	}
	p.instruction = forkInstruction(ev, p)
	return nil
}

func isSummarySource(source Source) bool {
	return len(source.Sources) > 0 && source.Sources[0].Kind == "summary"
}

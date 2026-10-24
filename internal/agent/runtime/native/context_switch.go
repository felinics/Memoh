package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

// MessageProvenance is what one message a run sends renders: the stored
// messages behind it, and whether the run keeps it where it is (its input, a
// steer) when it compacts.
type MessageProvenance struct {
	Message sdk.Message
	Sources []turn.ContextMessageSource
	Anchor  bool
}

// ContextSwitch is the compaction event an armed run asks for when the
// request before a step does not fit its allowance. View is every source the
// request renders, Anchors the ones it keeps in place, and Tail those of the
// latest committed step, which stays with everything after it.
// HistoryBudgetTokens is the room the request leaves for the summary and the
// history before the tail. Fold applies a pass the event committed to the
// request and reports the tokens it then takes.
type ContextSwitch struct {
	View                []turn.ContextMessageSource
	Anchors             []turn.ContextMessageSource
	Tail                []turn.ContextMessageSource
	HistoryBudgetTokens int
	UsageTokens         int
	AllowanceTokens     int
	Fold                func(ContextFold) int
	// Previous is the request the run sent last: this one without the
	// latest step. PreviousCacheRead reports that the provider served part
	// of it from its prompt cache, so it likely still holds all of it.
	Previous          *sdk.Request
	PreviousCacheRead bool
}

// ContextFold is one pass an event committed: the summary that replaces the
// messages whose sources it covers, and the earlier summaries it absorbs.
type ContextFold struct {
	Artifact string
	Summary  sdk.Message
	Covered  []turn.ContextMessageSource
	Absorbed []string
}

// contextSwitchState follows an armed run's request across its dispatches:
// the provenance of what it renders, the sources of the latest committed
// step, and, from a splice until the re-dispatch that follows it, where each
// message of the spliced request was before (-1 for a summary).
type contextSwitchState struct {
	known []MessageProvenance
	tail  []turn.ContextMessageSource
	moved []int
}

func newContextSwitchState(cfg RunConfig) *contextSwitchState {
	if cfg.SwitchContext == nil {
		return nil
	}
	s := &contextSwitchState{}
	for _, provenance := range cfg.ContextProvenance {
		s.know(provenance)
	}
	return s
}

func (s *contextSwitchState) know(provenance MessageProvenance) {
	messages := cloneProviderMessages([]sdk.Message{provenance.Message})
	clearProviderCacheControls(messages)
	provenance.Message = messages[0]
	s.known = append(s.known, provenance)
}

// committed records what a step persisted: messages are the ones it added to
// the request, in the order its commit persisted them with provenance.
func (s *contextSwitchState) committed(messages []sdk.Message, provenance []MessageProvenance) {
	if s == nil || len(provenance) == 0 || len(provenance) != len(messages) {
		return
	}
	var tail []turn.ContextMessageSource
	for i, p := range provenance {
		p.Message = messages[i]
		s.know(p)
		if !p.Anchor {
			tail = append(tail, p.Sources...)
		}
	}
	if len(tail) > 0 {
		s.tail = tail
	}
}

// align matches each message of a request, in order, to the provenance the
// run knows for it; a message the run knows nothing about matches nil.
func (s *contextSwitchState) align(messages []sdk.Message) []*MessageProvenance {
	cleared := cloneProviderMessages(messages)
	clearProviderCacheControls(cleared)
	out := make([]*MessageProvenance, len(messages))
	next := 0
	for i := range cleared {
		for j := next; j < len(s.known); j++ {
			if reflect.DeepEqual(s.known[j].Message, cleared[i]) {
				out[i], next = &s.known[j], j+1
				break
			}
		}
	}
	return out
}

// fold runs one compaction event when params does not fit allowance and the
// run has committed a step, and splices each pass it commits into params.
// Every message no pass covers stays as it was. It reports whether params
// changed; an event that leaves the request over its allowance fails as an
// unsatisfied budget, or a protected overflow when the event says so.
func (s *contextSwitchState) fold(ctx context.Context, cfg RunConfig, params *sdk.Request, allowance int) (bool, error) {
	if s == nil || len(s.tail) == 0 || allowance <= 0 {
		return false, nil
	}
	usage := contextfrag.ProviderEnvelopeTokens(params.System, params.Messages, params.Tools)
	if usage <= allowance {
		return false, nil
	}
	provenance := s.align(params.Messages)
	tailStart := slices.IndexFunc(provenance, func(p *MessageProvenance) bool {
		return p != nil && !p.Anchor && sharesSource(p.Sources, s.tail)
	})
	if tailStart < 0 {
		return false, nil
	}
	previous := *params
	previous.Messages = slices.Clone(params.Messages[:tailStart])
	sw := ContextSwitch{Tail: slices.Clone(s.tail), UsageTokens: usage, AllowanceTokens: allowance, Previous: &previous}
	if records := cfg.ContextMutations.CacheUsageRecords(); len(records) > 0 {
		sw.PreviousCacheRead = records[len(records)-1].CacheReadTokens > 0
	}
	var kept []sdk.Message
	for i, p := range provenance {
		if p != nil {
			sw.View = append(sw.View, p.Sources...)
			if p.Anchor {
				sw.Anchors = append(sw.Anchors, p.Sources...)
			}
		}
		if p == nil || p.Anchor || i >= tailStart {
			kept = append(kept, params.Messages[i])
		}
	}
	sw.HistoryBudgetTokens = allowance - contextfrag.ProviderEnvelopeTokens(params.System, kept, params.Tools)
	splice := requestSplice{messages: params.Messages, provenance: provenance, from: make([]int, len(params.Messages)), tail: sw.Tail}
	for i := range splice.from {
		splice.from[i] = i
	}
	tokens := usage
	sw.Fold = func(fold ContextFold) int {
		splice.apply(fold)
		tokens = contextfrag.ProviderEnvelopeTokens(params.System, splice.messages, params.Tools)
		return tokens
	}
	detail, err := cfg.SwitchContext(ctx, sw)
	if err == nil {
		switch call := unansweredToolCall(splice.messages); {
		case call != "":
			err, detail = fmt.Errorf("tool call %s lost its result", call), "trigger=in_run reason=postcondition"
		case tokens > allowance:
			err = fmt.Errorf("request_tokens=%d allowance=%d", tokens, allowance)
			detail = fmt.Sprintf("trigger=in_run reason=still_over request_tokens=%d allowance=%d", tokens, allowance)
		}
	}
	if detail != "" {
		cfg.ContextMutations.Record(contextfrag.MutationContextSwitch, detail)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, contextfrag.ErrProtectedContextOverflow) || errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
			return false, err
		}
		return false, fmt.Errorf("%w: context switch: %w", contextfrag.ErrBudgetUnsatisfied, err)
	}
	params.Messages = splice.messages
	s.known = nil
	for _, p := range splice.provenance {
		if p != nil {
			s.know(*p)
		}
	}
	s.moved = splice.from
	return true, nil
}

// requestSplice is a request an event rewrites pass by pass: its messages,
// what each renders, and where each was in the request the event started
// from.
type requestSplice struct {
	messages   []sdk.Message
	provenance []*MessageProvenance
	from       []int
	tail       []turn.ContextMessageSource
}

// apply removes the messages whose every source the pass covers and places
// its summary by turn.PlaceSummary. Anchors and the tail are never removed.
func (sp *requestSplice) apply(fold ContextFold) {
	covered := make(map[turn.ContextMessageSource]bool, len(fold.Covered)+len(fold.Absorbed))
	for _, source := range fold.Covered {
		covered[turn.ContextMessageSource{Kind: source.Kind, ID: source.ID}] = true
	}
	for _, id := range fold.Absorbed {
		covered[turn.ContextMessageSource{Kind: "summary", ID: id}] = true
	}
	removed := func(i int) bool {
		p := sp.provenance[i]
		if p == nil || p.Anchor || len(p.Sources) == 0 || sharesSource(p.Sources, sp.tail) {
			return false
		}
		for _, source := range p.Sources {
			if !covered[turn.ContextMessageSource{Kind: source.Kind, ID: source.ID}] {
				return false
			}
		}
		return true
	}
	order := make([]int, len(sp.messages))
	for i := range order {
		order[i] = i
	}
	order, placed := turn.PlaceSummary(order, -1, removed)
	if !placed {
		return
	}
	summary := &MessageProvenance{Message: fold.Summary, Sources: []turn.ContextMessageSource{{Kind: "summary", ID: fold.Artifact}}}
	next := requestSplice{tail: sp.tail}
	for _, i := range order {
		if i < 0 {
			next.messages, next.provenance, next.from = append(next.messages, fold.Summary), append(next.provenance, summary), append(next.from, -1)
			continue
		}
		next.messages, next.provenance, next.from = append(next.messages, sp.messages[i]), append(next.provenance, sp.provenance[i]), append(next.from, sp.from[i])
	}
	*sp = next
}

func sharesSource(sources, set []turn.ContextMessageSource) bool {
	return slices.ContainsFunc(sources, func(source turn.ContextMessageSource) bool {
		return slices.ContainsFunc(set, func(other turn.ContextMessageSource) bool {
			return source.Kind == other.Kind && source.ID == other.ID
		})
	})
}

// remap moves dynamic refs of the request before the last splice to the
// request it left.
func (s *contextSwitchState) remap(refs []dynamicSourceRef) []dynamicSourceRef {
	at := make(map[int]int, len(s.moved))
	for i, from := range s.moved {
		if from >= 0 {
			at[from] = i
		}
	}
	out := make([]dynamicSourceRef, 0, len(refs))
	for _, ref := range refs {
		if index, ok := at[ref.index]; ok {
			ref.index = index
			out = append(out, ref)
		}
	}
	return out
}

// redispatch is the configuration that dispatches params again after a
// splice, the way a mid-stream retry dispatches a failed call again: from the
// request as it now is, with the boundary's inputs admitted, and the prefix,
// cache breakpoints and stable prefix estimate moved with what the splice
// removed. It reports false when no splice happened since the last dispatch.
//
//nolint:gocritic // hugeParam: the run configuration and request are copied into the next dispatch.
func (s *contextSwitchState) redispatch(cfg RunConfig, dispatch generateDispatch, params sdk.Request) (RunConfig, bool) {
	if s == nil || s.moved == nil {
		return cfg, false
	}
	offset := 0
	if dispatch.systemPrepended {
		offset = 1
	}
	messages := cloneProviderMessages(params.Messages[offset:])
	clearProviderCacheControls(messages)
	plan := &cfg.ContextCachePlan
	stableBefore := messagesTokens(cfg.Messages[:min(plan.StableMessageCount, len(cfg.Messages))])
	cfg.Messages = messages
	cfg.retryDynamicRefs = shiftDynamicSourceRefs(dispatch.handoff.admitted(), -offset)
	cfg.initialProviderPrefixSet = true
	cfg.initialProviderMessageCount = s.movedCount(dispatch.initialMessageCount) - offset
	for _, count := range []*int{&plan.StableMessageCount, &plan.MidStableMessageCount} {
		if *count > 0 {
			*count = s.movedCount(*count+offset) - offset
		}
	}
	// The stable prefix is no longer the one the view compiled: its estimate
	// follows the messages the splice changed, and it has no compiled hash.
	if plan.StablePrefixTokenEstimate > 0 {
		plan.StablePrefixTokenEstimate += messagesTokens(messages[:min(plan.StableMessageCount, len(messages))]) - stableBefore
	}
	plan.StablePrefixHash = ""
	s.moved = nil
	return cfg, true
}

func messagesTokens(messages []sdk.Message) int {
	tokens := 0
	for _, message := range messages {
		tokens += contextfrag.EstimateSDKMessageTokens(message)
	}
	return tokens
}

// movedCount maps a message count of the request before the last splice,
// such as a prefix or a cache breakpoint, to the request it left: a summary
// counts as before the first message kept at or after count.
func (s *contextSwitchState) movedCount(count int) int {
	for i, from := range s.moved {
		if from >= count {
			return i
		}
	}
	return len(s.moved)
}

// unansweredToolCall names a tool call in messages that no tool result
// answers.
func unansweredToolCall(messages []sdk.Message) string {
	answered := map[string]bool{}
	for _, message := range messages {
		for _, part := range message.Content {
			if result, ok := part.(sdk.ToolResultPart); ok {
				answered[result.ToolCallID] = true
			}
		}
	}
	for _, message := range messages {
		for _, part := range message.Content {
			if call, ok := part.(sdk.ToolCallPart); ok && !answered[call.ToolCallID] {
				return call.ToolCallID
			}
		}
	}
	return ""
}

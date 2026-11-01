package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

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
// request before a step does not fit its allowance. Request is that request
// and Provenance what each of its messages renders (nil for runtime context
// the run rebuilds); the event decides what of it stays verbatim. Summarize
// answers a fork request through the run's own model, with the output
// settings the request carries, so a fork reads only what WindowTokens holds
// beside OutputTokens, the run's output allowance, as every request of the
// run does. Fold applies a pass the event committed to the request and
// reports the request as it then is. UsageTokens, AllowanceTokens and
// WindowTokens are in the units the run's estimate prices: the provider
// counts Density tokens for each of them.
type ContextSwitch struct {
	Request         sdk.Request
	Provenance      []*MessageProvenance
	UsageTokens     int
	AllowanceTokens int
	WindowTokens    int
	OutputTokens    int
	Density         float64
	Summarize       func(context.Context, sdk.Request) (sdk.ModelResult, error)
	Fold            func(ContextFold) ContextSplice
}

// ContextSplice is a request after the passes an event applied to it so far.
type ContextSplice struct {
	Request    sdk.Request
	Provenance []*MessageProvenance
	Tokens     int
}

// ContextFold is one pass an event committed: its summary, and the request
// it leaves by the messages of the request it read, the index of each that
// stays or -1 for the summary.
type ContextFold struct {
	SummaryID string
	Summary   sdk.Message
	Order     []int
}

// contextSwitchState follows an armed run's request across its dispatches:
// the provenance of what it renders and, from a splice until the re-dispatch
// that follows it, where each message of the spliced request was before (-1
// for a summary). It also calibrates the run's estimate against the provider:
// density is how many tokens the provider counts for each token the estimate
// prices, from the latest step that reported its input (dispatched is what
// the estimate priced the request a step sent at), and starts from the
// session's last such step (RunConfig.ContextDensity).
type contextSwitchState struct {
	known      []MessageProvenance
	moved      []int
	density    float64
	dispatched int
}

// maxContextDensity bounds a calibration: a provider count far beyond any
// tokenizer's (a cumulative or misreported usage) must not fold every request
// to nothing.
const maxContextDensity = 4.0

func newContextSwitchState(cfg RunConfig) *contextSwitchState {
	if cfg.SwitchContext == nil {
		return nil
	}
	s := &contextSwitchState{density: boundedContextDensity(cfg.ContextDensity)}
	for _, provenance := range cfg.ContextProvenance {
		s.know(provenance)
	}
	return s
}

// boundedContextDensity is a density a calibration may apply: never below 1,
// where the estimate already holds, nor above maxContextDensity.
func boundedContextDensity(density float64) float64 {
	if !(density > 1) {
		return 1
	}
	return min(density, maxContextDensity)
}

// calibrated is a provider token count in the units the estimate prices: a
// request the estimate prices at calibrated(n) is one the provider counts at
// about n.
func (s *contextSwitchState) calibrated(tokens int) int {
	if s == nil || s.density <= 1 || tokens <= 0 {
		return tokens
	}
	return max(int(float64(tokens)/s.density), 1)
}

// staged records what the estimate prices the request a step sends at.
func (s *contextSwitchState) staged(params *sdk.Request) {
	if s == nil || params == nil {
		return
	}
	s.dispatched = contextfrag.ProviderEnvelopeTokens(params.System, params.Messages, params.Tools)
}

// observe calibrates the estimate by the input tokens the provider counted
// for the request the step sent, and reports what the estimate priced that
// request at; zero when either is unknown.
func (s *contextSwitchState) observe(usage sdk.Usage) int {
	if s == nil || s.dispatched <= 0 || usage.InputTokens <= 0 {
		return 0
	}
	s.density = boundedContextDensity(float64(usage.InputTokens) / float64(s.dispatched))
	return s.dispatched
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
	for i, p := range provenance {
		p.Message = messages[i]
		s.know(p)
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

// fold runs one compaction event when params does not fit allowance, which
// is calibrated, and splices each pass it commits into params; the event
// decides what stays verbatim. Every message no pass covers stays as it was.
// It reports whether params changed; an event that leaves the request over
// its allowance fails as the event says. The event works in the estimate's
// units, so the window a fork reads within is calibrated too.
func (s *contextSwitchState) fold(ctx context.Context, cfg RunConfig, params *sdk.Request, allowance int) (bool, error) {
	if s == nil || allowance <= 0 {
		return false, nil
	}
	usage := contextfrag.ProviderEnvelopeTokens(params.System, params.Messages, params.Tools)
	if usage <= allowance {
		return false, nil
	}
	provenance := s.align(params.Messages)
	window, reserve := providerWindow(cfg)
	window = s.calibrated(window)
	splice := requestSplice{messages: params.Messages, provenance: provenance, from: make([]int, len(params.Messages))}
	for i := range splice.from {
		splice.from[i] = i
	}
	tokens := usage
	sw := ContextSwitch{
		Request:         *params,
		Provenance:      provenance,
		UsageTokens:     usage,
		AllowanceTokens: allowance,
		WindowTokens:    window,
		OutputTokens:    reserve,
		Density:         s.density,
		Summarize:       forkSummarizer(cfg.Model, cfg.ModelIdleWindow),
		Fold: func(fold ContextFold) ContextSplice {
			splice.apply(fold)
			tokens = contextfrag.ProviderEnvelopeTokens(params.System, splice.messages, params.Tools)
			request := *params
			request.Messages = splice.messages
			return ContextSplice{Request: request, Provenance: splice.provenance, Tokens: tokens}
		},
	}
	detail, err := cfg.SwitchContext(ctx, sw)
	if err == nil {
		switch call, result := unpairedToolParts(splice.messages); {
		case call != "":
			err, detail = fmt.Errorf("tool call %s lost its result", call), "trigger=in_run reason=postcondition"
		case result != "":
			err, detail = fmt.Errorf("tool result %s lost its call", result), "trigger=in_run reason=postcondition"
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
	recordFoldedInputImages(cfg.ContextMutations, params.Messages, provenance, splice.from)
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
}

// apply rewrites the request as the pass left it: the messages fold.Order
// keeps, in its order, with the summary in its place.
func (sp *requestSplice) apply(fold ContextFold) {
	summary := &MessageProvenance{Message: fold.Summary, Sources: []turn.ContextMessageSource{{Kind: "summary", ID: fold.SummaryID}}}
	var next requestSplice
	for _, i := range fold.Order {
		if i < 0 {
			next.messages, next.provenance, next.from = append(next.messages, fold.Summary), append(next.provenance, summary), append(next.from, -1)
			continue
		}
		next.messages, next.provenance, next.from = append(next.messages, sp.messages[i]), append(next.provenance, sp.provenance[i]), append(next.from, sp.from[i])
	}
	*sp = next
}

// recordFoldedInputImages records the inputs a fold summarized with their
// images: a summary carries no image, so they are omitted from then on. from
// is where each message of the spliced request was in messages.
func recordFoldedInputImages(ledger *contextfrag.MutationLedger, messages []sdk.Message, provenance []*MessageProvenance, from []int) {
	kept := make(map[int]bool, len(from))
	for _, i := range from {
		kept[i] = true
	}
	var omitted []string
	for i, message := range messages {
		p := provenance[i]
		if kept[i] || p == nil || !p.Anchor || !slices.ContainsFunc(message.Content, func(part sdk.MessagePart) bool {
			_, image := part.(sdk.ImagePart)
			return image
		}) {
			continue
		}
		for _, source := range p.Sources {
			if source.ID != "" {
				omitted = append(omitted, source.ID)
			}
		}
	}
	if len(omitted) > 0 && ledger != nil {
		ledger.Record(contextfrag.MutationCurrentInputImagesOmitted, "sources="+strings.Join(omitted, ","))
	}
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

// unpairedToolParts names a tool call in messages that no tool result
// answers, and a tool result whose call no message makes.
func unpairedToolParts(messages []sdk.Message) (call, result string) {
	answered, called := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		for _, part := range message.Content {
			switch part := part.(type) {
			case sdk.ToolResultPart:
				answered[part.ToolCallID] = true
			case sdk.ToolCallPart:
				called[part.ToolCallID] = true
			}
		}
	}
	for _, message := range messages {
		for _, part := range message.Content {
			switch part := part.(type) {
			case sdk.ToolCallPart:
				if call == "" && !answered[part.ToolCallID] {
					call = part.ToolCallID
				}
			case sdk.ToolResultPart:
				if result == "" && !called[part.ToolCallID] {
					result = part.ToolCallID
				}
			}
		}
	}
	return call, result
}

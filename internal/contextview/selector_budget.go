package contextview

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// HistoryTrimNotice tells the model that budget pressure removed messages.
// Tiered drops can leave holes mid-history, not only at the head, so the
// wording stays honest about that. The text is model-visible; change it only
// deliberately.
const HistoryTrimNotice = "[System Notice] Some earlier and intervening messages were trimmed to fit the context window. " +
	"If you need information from the trimmed messages, use the available tools " +
	"(such as memory_read or web search) to retrieve it."

// Budget drop reasons name the attention band a fragment fell out of, so the
// lifecycle drop_reasons histogram explains what budget pressure removed.
const (
	budgetDropReasonPassive        = "budget:passive"
	budgetDropReasonUntiered       = "budget:untiered"
	budgetDropReasonDirected       = "budget:directed"
	budgetDropReasonRecentWindow   = "budget:recent_window"
	budgetDropReasonOrphanExchange = "budget:orphan_tool_exchange"
)

// Attention tiers order budget drops: passive group traffic goes first,
// fragments without attention data stay time-neutral in the middle, and
// directed traffic (mention/reply/direct/command/schedule) goes
// last.
const (
	attentionTierPassive = iota
	attentionTierUntiered
	attentionTierDirected
)

func fragAttentionTier(frag contextfrag.ContextFrag) int {
	reasons := frag.Scope.Attention
	if len(reasons) == 0 {
		return attentionTierUntiered
	}
	for _, reason := range reasons {
		if reason != contextfrag.AttentionPassive {
			return attentionTierDirected
		}
	}
	return attentionTierPassive
}

func budgetDropReasonForTier(tier int) string {
	switch tier {
	case attentionTierPassive:
		return budgetDropReasonPassive
	case attentionTierDirected:
		return budgetDropReasonDirected
	default:
		return budgetDropReasonUntiered
	}
}

// budgetUnit is the atomic drop unit: a lone fragment, or an assistant
// tool-call fragment grouped with its tool-result fragments so budget drops
// never break a tool closure. Units span the whole tagged set: when any
// member is protected the entire unit is (mixed droppability would orphan
// half a closure), and the tier comes from attention-bearing members only so
// data-less tool results never promote a passive exchange.
type budgetUnit struct {
	indexes       []int
	tokens        int
	attentionTier int
	hasAttention  bool
	droppable     bool
	hasCall       bool
	hasResult     bool
}

func (u *budgetUnit) tier() int {
	if u.hasAttention {
		return u.attentionTier
	}
	return attentionTierUntiered
}

// orphanToolExchange reports a unit that carries only one side of a tool
// exchange: a call nothing ever answers, or a result answering a call that
// is not (or no longer) in the set. Either shape is a guaranteed provider
// 400 if it reaches the wire, so both drop unconditionally.
func (u *budgetUnit) orphanToolExchange() bool {
	return u.hasCall != u.hasResult
}

// buildBudgetUnits pairs tool calls with their results in a single forward
// pass keyed by tool_call_id and scoped to appearance order, not id identity
// alone: a call opens its id, the next result seen for that id closes it,
// and the id is then free for a later call to reopen. Backends that emit
// deterministic per-turn ids (llama.cpp/vLLM style "call_0") reuse the same
// id across unrelated exchanges; scoping by the currently-open call keeps
// each exchange its own drop unit instead of collapsing every reuse of an id
// into one atomic (and possibly permanently undroppable) unit.
//
// A call whose id is already open when a new call for that id arrives does
// not close the earlier one: the earlier call stays open — and therefore
// orphaned, since nothing will ever close it — while the new call starts its
// own unit. A result whose id has no open call is likewise an orphan. This
// symmetric treatment also covers a result observed before its matching
// call: the result finds no open call (orphan), and the call that follows
// then opens a unit nothing ever closes (orphan too) — both drop, instead of
// the old union-find behavior that paired them regardless of order.
func buildBudgetUnits(tagged []TaggedFrag) []budgetUnit {
	units := make([]budgetUnit, 0, len(tagged))
	open := make(map[string]int)

	for i, taggedFrag := range tagged {
		frag := taggedFrag.Frag
		callIDs := fragToolCallIDs(frag)
		resultIDs := fragToolResultCallIDs(frag)

		home := -1
		for _, id := range resultIDs {
			idx, ok := open[id]
			if !ok {
				continue
			}
			delete(open, id)
			if home == -1 {
				home = idx
				continue
			}
			if home != idx {
				mergeBudgetUnits(units, home, idx, open)
			}
		}
		if home == -1 {
			units = append(units, budgetUnit{droppable: true})
			home = len(units) - 1
		}

		addFragToBudgetUnit(&units[home], i, frag, taggedFrag, len(callIDs) > 0, len(resultIDs) > 0)

		for _, id := range callIDs {
			open[id] = home
		}
	}
	return units
}

// mergeBudgetUnits folds src into dest when a single fragment's result parts
// close calls that opened two different (still-open) units — a batched
// multi-result tool message answering more than one pending call. It also
// redirects any other ids still pointing at src, in case src had further
// calls open elsewhere.
func mergeBudgetUnits(units []budgetUnit, dest, src int, open map[string]int) {
	from := units[src]
	to := &units[dest]
	to.indexes = append(to.indexes, from.indexes...)
	to.tokens += from.tokens
	if from.hasAttention && (!to.hasAttention || from.attentionTier > to.attentionTier) {
		to.attentionTier = from.attentionTier
	}
	to.hasAttention = to.hasAttention || from.hasAttention
	to.droppable = to.droppable && from.droppable
	to.hasCall = to.hasCall || from.hasCall
	to.hasResult = to.hasResult || from.hasResult
	units[src] = budgetUnit{}
	for id, idx := range open {
		if idx == src {
			open[id] = dest
		}
	}
}

func addFragToBudgetUnit(unit *budgetUnit, index int, frag contextfrag.ContextFrag, taggedFrag TaggedFrag, hasCall, hasResult bool) {
	unit.indexes = append(unit.indexes, index)
	unit.tokens += taggedFrag.Tokens
	if !taggedFrag.HasTag(TagCanDrop) {
		unit.droppable = false
	}
	if len(frag.Scope.Attention) > 0 {
		if tier := fragAttentionTier(frag); !unit.hasAttention || tier > unit.attentionTier {
			unit.attentionTier = tier
		}
		unit.hasAttention = true
	}
	if hasCall {
		unit.hasCall = true
	}
	if hasResult {
		unit.hasResult = true
	}
}

func fragToolCallIDs(frag contextfrag.ContextFrag) []string {
	var ids []string
	for _, part := range frag.Parts {
		msg := sdkMessagePart(part)
		if msg == nil {
			continue
		}
		for _, mp := range msg.Content {
			if call, ok := mp.(sdk.ToolCallPart); ok {
				if id := strings.TrimSpace(call.ToolCallID); id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

func fragToolResultCallIDs(frag contextfrag.ContextFrag) []string {
	var ids []string
	for _, part := range frag.Parts {
		msg := sdkMessagePart(part)
		if msg == nil {
			continue
		}
		for _, mp := range msg.Content {
			if result, ok := mp.(sdk.ToolResultPart); ok {
				if id := strings.TrimSpace(result.ToolCallID); id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// budgetTrimDrops decides which droppable fragments leave the context under
// budget pressure. The drop order is a fixed total order that depends only on
// the fragments and the recent-protect window, never on the budget:
//
//  1. Droppable orphan tool-call/tool-result units (the other half of the
//     exchange is missing anywhere in the set) drop unconditionally: left in
//     place they are a guaranteed provider 400.
//  2. The remaining units drop tier by tier — passive, then untiered, then
//     directed — so a directed unit never drops before a passive or
//     untiered one, regardless of where either sits.
//  3. Within a tier, units outside the recent-protect window drop before
//     units inside it (reported as budget:recent_window), oldest first in
//     both halves. The window is a tie-break within a shared tier, not a
//     competing band: it can never buy a passive unit survival over a
//     directed one.
//
// Each spatial drop happens only while the droppable total still exceeds its
// allowance — the fit check is the sole budget-dependent point. A larger
// allowance stops earlier along the same sequence, so it always keeps a superset of
// what a smaller budget keeps, and dropping the whole sequence always reaches
// the budget because only droppable tokens are counted.
//
// Priority never enters retention: it only orders rendering. The budget
// passed here is the allowance left for droppable tokens. The legacy path
// passes its whole history budget, preserving trimMessagesByTokens accounting.
// An active unified plan first deducts protected history and the required trim
// notice. When the droppable total fits, nothing drops and the output is
// byte-identical to the unbudgeted path.
func budgetTrimDrops(tagged []TaggedFrag, maxTokens, recentProtectTokens int) (map[int]bool, map[int]string) {
	if maxTokens <= 0 {
		return nil, nil
	}
	return budgetTrimDropsEnabled(tagged, maxTokens, recentProtectTokens)
}

// budgetTrimDropsEnabled applies an active budget even when no tokens remain
// for droppable history. The legacy zero value still disables budgeting via
// budgetTrimDrops.
func budgetTrimDropsEnabled(tagged []TaggedFrag, maxTokens, recentProtectTokens int) (map[int]bool, map[int]string) {
	if maxTokens < 0 {
		maxTokens = 0
	}
	units := buildBudgetUnits(tagged)

	drops := make(map[int]bool)
	reasons := make(map[int]string)
	dropUnit := func(unit *budgetUnit, reason string) {
		for _, idx := range unit.indexes {
			drops[idx] = true
			reasons[idx] = reason
		}
	}

	pool := make([]int, 0, len(units))
	total := 0
	for i := range units {
		unit := &units[i]
		if !unit.droppable {
			continue
		}
		if unit.orphanToolExchange() {
			dropUnit(unit, budgetDropReasonOrphanExchange)
			continue
		}
		pool = append(pool, i)
		total += unit.tokens
	}
	if total <= maxTokens {
		if len(drops) == 0 {
			return nil, nil
		}
		return drops, reasons
	}

	protectedStart := len(pool)
	acc := 0
	for protectedStart > 0 && acc+units[pool[protectedStart-1]].tokens <= recentProtectTokens {
		acc += units[pool[protectedStart-1]].tokens
		protectedStart--
	}

	outside, inside := pool[:protectedStart], pool[protectedStart:]
	dropTier := func(band []int, tier int, reason string) {
		for _, unitIdx := range band {
			if total <= maxTokens {
				return
			}
			unit := &units[unitIdx]
			if unit.tier() != tier {
				continue
			}
			dropUnit(unit, reason)
			total -= unit.tokens
		}
	}
	for tier := attentionTierPassive; tier <= attentionTierDirected && total > maxTokens; tier++ {
		dropTier(outside, tier, budgetDropReasonForTier(tier))
		dropTier(inside, tier, budgetDropReasonRecentWindow)
	}
	return drops, reasons
}

// protectedHistoryTokenCost charges every protected non-system/non-current
// fragment to the unified history budget. Current requests may intentionally
// retain the history slot to preserve composed order, so kind is authoritative
// alongside slot. Unit protection is atomic: when a protected tool-call/result
// member makes the whole closure non-droppable, every history member of that
// closure is charged.
func protectedHistoryTokenCost(tagged []TaggedFrag) int {
	units := buildBudgetUnits(tagged)
	total := 0
	for i := range units {
		unit := &units[i]
		if unit.droppable {
			continue
		}
		for _, idx := range unit.indexes {
			frag := tagged[idx].Frag
			if frag.Slot == contextfrag.SlotSystem ||
				frag.Slot == contextfrag.SlotCurrentUser ||
				frag.Kind == contextfrag.KindCurrentUserMessage {
				continue
			}
			total += tagged[idx].Tokens
		}
	}
	return total
}

// hasSpatialBudgetDrop reports whether any drop reason came from budget
// pressure rather than the unconditional orphan cut, so the trim notice
// (which promises the model earlier or intervening messages were trimmed to
// fit) is never raised over an orphan-only drop that would have fit as-is.
func hasSpatialBudgetDrop(reasons map[int]string) bool {
	for _, reason := range reasons {
		if reason != budgetDropReasonOrphanExchange {
			return true
		}
	}
	return false
}

// TrimNoticeFrag is the synthetic fragment the builder splices in when budget
// trimming dropped history, mirroring the legacy resolver notice message.
func TrimNoticeFrag(scope contextfrag.Scope) contextfrag.ContextFrag {
	msg := sdk.Message{
		Role:    sdk.MessageRoleSystem,
		Content: []sdk.MessagePart{sdk.TextPart{Text: HistoryTrimNotice}},
	}
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID:         "history.trim_notice",
		Message:    msg,
		Kind:       contextfrag.KindSystemPolicy,
		Slot:       contextfrag.SlotHistory,
		Priority:   30,
		CacheClass: contextfrag.CacheNever,
		Trust:      contextfrag.TrustSystem,
		Scope:      scope,
		Source:     "context_select",
		SourceID:   "history.trim_notice",
		Collector:  "budget_trim",
	})
}

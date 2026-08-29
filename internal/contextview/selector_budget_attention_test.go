package contextview

import (
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func attentionMessageFrag(id string, msg sdk.Message, tokens int, reasons ...contextfrag.AttentionReason) contextfrag.ContextFrag {
	frag := messageFrag(id, msg)
	frag.TokenEstimate = tokens
	frag.Scope.Attention = reasons
	return frag
}

func selectProviderFrags(frags []contextfrag.ContextFrag, budget BudgetEnvelope) SelectionResult {
	selector := &FragmentSelector{}
	return selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), budget)
}

func TestBudgetAttention_NoPressureKeepsEverything(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("passive-old", sdk.UserMessage("group chatter"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("directed-old", sdk.UserMessage("@bot do it"), 100, contextfrag.AttentionMention),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 1000, RecentProtectTokens: 50})

	assertSelectedIDs(t, result, []string{"passive-old", "directed-old", "latest"})
	if result.TrimNotice {
		t.Fatal("no budget pressure must not raise a trim notice")
	}
}

func TestBudgetAttention_PassiveDropsBeforeDirected(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-1", sdk.UserMessage("@bot summarize"), 100, contextfrag.AttentionMention),
		attentionMessageFrag("passive-1", sdk.UserMessage("group chatter one"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("directed-2", sdk.UserMessage("replying to bot"), 100, contextfrag.AttentionReply),
		attentionMessageFrag("passive-2", sdk.UserMessage("group chatter two"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 200})

	assertSelectedIDs(t, result, []string{"directed-1", "directed-2", "latest"})
	assertDroppedReason(t, result, "passive-1", budgetDropReasonPassive)
	assertDroppedReason(t, result, "passive-2", budgetDropReasonPassive)
}

func TestBudgetAttention_UntieredDropsAfterPassiveBeforeDirected(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-1", sdk.UserMessage("@bot look"), 100, contextfrag.AttentionMention),
		attentionMessageFrag("untiered-1", sdk.UserMessage("plain chat history"), 100),
		attentionMessageFrag("passive-1", sdk.UserMessage("group chatter"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 100})

	assertSelectedIDs(t, result, []string{"directed-1", "latest"})
	assertDroppedReason(t, result, "passive-1", budgetDropReasonPassive)
	assertDroppedReason(t, result, "untiered-1", budgetDropReasonUntiered)
}

func TestBudgetAttention_UntieredOnlyKeepsOldestFirstOrder(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("untiered-1", sdk.UserMessage("first"), 100),
		attentionMessageFrag("untiered-2", sdk.UserMessage("second"), 100),
		attentionMessageFrag("untiered-3", sdk.UserMessage("third"), 100),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 200})

	assertSelectedIDs(t, result, []string{"untiered-2", "untiered-3", "latest"})
	assertDroppedReason(t, result, "untiered-1", budgetDropReasonUntiered)
}

// The recent-protect window scopes protection to units sharing a tier, not
// across tiers: attention is the primary key and window membership only
// breaks ties within a shared tier. A directed unit sitting outside the
// window must never drop before a passive unit sitting inside it — that
// cross-tier case is exactly where a fixed window-as-primary-band ordering
// used to drop a directed message 21K tokens back to protect in-window
// passive chatter.
func TestBudgetAttention_DirectedOutsideWindowNeverDropsBeforePassiveInsideWindow(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-old", sdk.UserMessage("@bot old ask"), 150, contextfrag.AttentionMention),
		attentionMessageFrag("passive-recent", sdk.UserMessage("recent group chatter"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 200, RecentProtectTokens: 100})

	assertSelectedIDs(t, result, []string{"directed-old", "latest"})
	assertDroppedReason(t, result, "passive-recent", budgetDropReasonRecentWindow)
}

// When the budget is too small to hold the recent-protect window, the window
// yields in the same tier order (passive first) instead of shielding recent
// passives at the expense of older directed traffic, and the drops honestly
// read budget:recent_window.
func TestBudgetAttention_WindowYieldsTierOrderedUnderSmallBudget(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-old", sdk.UserMessage("@bot old ask"), 100, contextfrag.AttentionMention),
		attentionMessageFrag("passive-new", sdk.UserMessage("recent group chatter"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 150, RecentProtectTokens: 1000})

	assertSelectedIDs(t, result, []string{"directed-old", "latest"})
	assertDroppedReason(t, result, "passive-new", budgetDropReasonRecentWindow)
}

// The whole span sits inside the window here, so drops walk the in-window
// band tier by tier (passive, then directed, oldest first) under the
// budget:recent_window reason; raising the budget stops earlier along the
// same order and monotonically keeps more.
func TestBudgetAttention_MoreBudgetNeverDropsMore(t *testing.T) {
	t.Parallel()

	buildFrags := func() []contextfrag.ContextFrag {
		return []contextfrag.ContextFrag{
			attentionMessageFrag("window-1", sdk.UserMessage("one"), 100, contextfrag.AttentionMention),
			attentionMessageFrag("window-2", sdk.UserMessage("two"), 100, contextfrag.AttentionPassive),
			attentionMessageFrag("window-3", sdk.UserMessage("three"), 100, contextfrag.AttentionMention),
			attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
		}
	}

	tight := selectProviderFrags(buildFrags(), BudgetEnvelope{MaxTokens: 150, RecentProtectTokens: 1000})
	assertSelectedIDs(t, tight, []string{"window-3", "latest"})
	assertDroppedReason(t, tight, "window-2", budgetDropReasonRecentWindow)
	assertDroppedReason(t, tight, "window-1", budgetDropReasonRecentWindow)

	mid := selectProviderFrags(buildFrags(), BudgetEnvelope{MaxTokens: 250, RecentProtectTokens: 1000})
	assertSelectedIDs(t, mid, []string{"window-1", "window-3", "latest"})
	assertDroppedReason(t, mid, "window-2", budgetDropReasonRecentWindow)

	loose := selectProviderFrags(buildFrags(), BudgetEnvelope{MaxTokens: 400, RecentProtectTokens: 1000})
	assertSelectedIDs(t, loose, []string{"window-1", "window-2", "window-3", "latest"})
}

func keptIDSet(result SelectionResult) map[string]bool {
	kept := make(map[string]bool, len(result.Selected))
	for _, frag := range result.Selected {
		kept[frag.ID] = true
	}
	return kept
}

// windowShiftFrags is the review scenario where a budget-scaled window broke
// cross-budget monotonicity: the larger budget widened the window, protected
// more recent passives out of the passive band, and pushed pressure into the
// older untiered band — dropping the 95-token message the smaller budget kept.
func windowShiftFrags() []contextfrag.ContextFrag {
	return []contextfrag.ContextFrag{
		attentionMessageFrag("untiered-old", sdk.UserMessage("plain history"), 95),
		attentionMessageFrag("passive-a", sdk.UserMessage("chatter a"), 20, contextfrag.AttentionPassive),
		attentionMessageFrag("passive-b", sdk.UserMessage("chatter b"), 30, contextfrag.AttentionPassive),
		attentionMessageFrag("passive-c", sdk.UserMessage("chatter c"), 60, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 50, contextfrag.AttentionDirect),
	}
}

// Kept sets nest monotonically across any increasing budget ladder: shared
// banding (fixed recent-protect window, tier order, oldest-first) makes every
// larger budget stop earlier along the same drop sequence.
func TestBudgetAttention_KeptSetsNestAcrossBudgets(t *testing.T) {
	t.Parallel()

	budgets := []int{60, 100, 140, 180, 205}
	prev := selectProviderFrags(windowShiftFrags(), BudgetEnvelope{MaxTokens: budgets[0], RecentProtectTokens: 100})
	for _, budget := range budgets[1:] {
		next := selectProviderFrags(windowShiftFrags(), BudgetEnvelope{MaxTokens: budget, RecentProtectTokens: 100})
		nextKept := keptIDSet(next)
		for _, frag := range prev.Selected {
			if !nextKept[frag.ID] {
				t.Fatalf("budget %d dropped %q kept at a smaller budget", budget, frag.ID)
			}
		}
		prev = next
	}
}

// The trim notice may never split a kept tool closure. When the natural
// insertion point (after the last dropped fragment) falls between a kept
// call and its result, it slides to the closure's end.
func TestBudgetAttention_NoticeNeverSplitsKeptClosure(t *testing.T) {
	t.Parallel()

	call := toolCallFrag("directed-call", "search", "call-1")
	call.TokenEstimate = 50
	call.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionMention}
	callResult := toolResultFrag("directed-result", "search", "call-1", "found")
	callResult.TokenEstimate = 50

	frags := []contextfrag.ContextFrag{
		call,
		attentionMessageFrag("passive-mid", sdk.UserMessage("group chatter"), 100, contextfrag.AttentionPassive),
		callResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 150})

	assertSelectedIDs(t, result, []string{"directed-call", "directed-result", "latest"})
	assertDroppedReason(t, result, "passive-mid", budgetDropReasonPassive)
	if !result.TrimNotice {
		t.Fatal("budget drop must raise a trim notice")
	}
	if result.TrimNoticeIndex != 2 {
		t.Fatalf("TrimNoticeIndex = %d, want 2 (after the kept closure)", result.TrimNoticeIndex)
	}
}

// The notice wording is honest about scattered trimming — holes can be
// mid-history, not only at the head. Model-visible change locked here.
func TestHistoryTrimNoticeWordingHonestAboutHoles(t *testing.T) {
	t.Parallel()

	want := "[System Notice] Some earlier and intervening messages were trimmed to fit the context window. " +
		"If you need information from the trimmed messages, use the available tools " +
		"(such as memory_read or web search) to retrieve it."
	if HistoryTrimNotice != want {
		t.Fatalf("HistoryTrimNotice = %q, want %q", HistoryTrimNotice, want)
	}
}

func TestBudgetAttention_ToolClosureDropsAtomically(t *testing.T) {
	t.Parallel()

	call := toolCallFrag("passive-call", "search", "call-1")
	call.TokenEstimate = 50
	call.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}
	callResult := toolResultFrag("passive-result", "search", "call-1", "found")
	callResult.TokenEstimate = 100
	callResult.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-1", sdk.UserMessage("@bot keep this"), 100, contextfrag.AttentionMention),
		call,
		callResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 100})

	assertSelectedIDs(t, result, []string{"directed-1", "latest"})
	assertDroppedReason(t, result, "passive-call", budgetDropReasonPassive)
	assertDroppedReason(t, result, "passive-result", budgetDropReasonPassive)
}

// A tool closure with mixed droppability (one member pinned, the other
// droppable) must be kept whole; dropping only the droppable half leaves an
// orphan tool_use or tool_result the provider rejects with a 400.
func TestBudgetAttention_MixedDroppabilityClosureKeptWhole(t *testing.T) {
	t.Parallel()

	pinnedCall := toolCallFrag("pinned-call", "search", "call-1")
	pinnedCall.TokenEstimate = 50
	pinnedCall.Budget.Overflow = contextfrag.OverflowKeep
	droppableResult := toolResultFrag("droppable-result", "search", "call-1", "found")
	droppableResult.TokenEstimate = 200

	droppableCall := toolCallFrag("droppable-call", "search", "call-2")
	droppableCall.TokenEstimate = 200
	pinnedResult := toolResultFrag("pinned-result", "search", "call-2", "found")
	pinnedResult.TokenEstimate = 50
	pinnedResult.Budget.Overflow = contextfrag.OverflowKeep

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("filler-old", sdk.UserMessage("old filler"), 100),
		pinnedCall,
		droppableResult,
		droppableCall,
		pinnedResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 50})

	assertSelectedIDs(t, result, []string{"pinned-call", "droppable-result", "droppable-call", "pinned-result", "latest"})
	assertDroppedReason(t, result, "filler-old", budgetDropReasonUntiered)
}

// A closure's tier comes from its attention-bearing members only. A passive
// call whose tool result carries no attention data stays in the passive band
// instead of being promoted to untiered.
func TestBudgetAttention_ClosureTierIgnoresAttentionlessMembers(t *testing.T) {
	t.Parallel()

	call := toolCallFrag("passive-call", "search", "call-1")
	call.TokenEstimate = 50
	call.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}
	callResult := toolResultFrag("plain-result", "search", "call-1", "found")
	callResult.TokenEstimate = 50

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("untiered-old", sdk.UserMessage("plain history"), 100),
		call,
		callResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 150})

	assertSelectedIDs(t, result, []string{"untiered-old", "latest"})
	assertDroppedReason(t, result, "passive-call", budgetDropReasonPassive)
	assertDroppedReason(t, result, "plain-result", budgetDropReasonPassive)
}

// A droppable tool result whose call is absent from the set is a guaranteed
// provider 400; with a budget in force it drops unconditionally, restoring
// the legacy leading-orphan cut even when everything fits.
//
// An orphan drop is not a space trim: with nothing else under pressure, it
// must not raise the trim notice either.
func TestBudgetAttention_OrphanToolResultDroppedEvenUnderBudget(t *testing.T) {
	t.Parallel()

	orphan := toolResultFrag("orphan-result", "search", "call-gone", "stale")
	orphan.TokenEstimate = 10

	frags := []contextfrag.ContextFrag{
		orphan,
		attentionMessageFrag("plain-old", sdk.UserMessage("plain history"), 100),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 1000})

	assertSelectedIDs(t, result, []string{"plain-old", "latest"})
	assertDroppedReason(t, result, "orphan-result", budgetDropReasonOrphanExchange)
	if result.TrimNotice {
		t.Fatal("an orphan-only drop is not a space trim and must not raise a trim notice")
	}
}

// A genuine space trim alongside an orphan drop must still raise the notice:
// only the orphan-only case is silent.
func TestBudgetAttention_OrphanPlusSpatialDropRaisesTrimNotice(t *testing.T) {
	t.Parallel()

	orphan := toolResultFrag("orphan-result", "search", "call-gone", "stale")
	orphan.TokenEstimate = 10

	frags := []contextfrag.ContextFrag{
		orphan,
		attentionMessageFrag("passive-old", sdk.UserMessage("chatter"), 100, contextfrag.AttentionPassive),
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 50})

	assertSelectedIDs(t, result, []string{"latest"})
	assertDroppedReason(t, result, "orphan-result", budgetDropReasonOrphanExchange)
	assertDroppedReason(t, result, "passive-old", budgetDropReasonPassive)
	if !result.TrimNotice {
		t.Fatal("a genuine space trim alongside an orphan drop must still raise the trim notice")
	}
}

// buildBudgetUnits scopes tool-call pairing to appearance order: a call
// opens its tool_call_id, the next result seen for that id closes it, and
// the id is then free for a later call to reopen. Backends that emit
// deterministic per-turn ids (llama.cpp/vLLM style "call_0") reuse the same
// id across unrelated exchanges; without appearance-order scoping, reusing
// an id collapses two independent exchanges into one atomic drop unit. Here
// the budget only needs the older exchange gone — the newer, smaller reuse
// of the same id must survive on its own instead of being dragged along.
func TestBudgetAttention_ReusedToolCallIDKeepsExchangesIndependent(t *testing.T) {
	t.Parallel()

	oldCall := toolCallFrag("old-call", "search", "call_0")
	oldCall.TokenEstimate = 50
	oldCall.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}
	oldResult := toolResultFrag("old-result", "search", "call_0", "found")
	oldResult.TokenEstimate = 50

	newCall := toolCallFrag("new-call", "search", "call_0")
	newCall.TokenEstimate = 25
	newCall.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}
	newResult := toolResultFrag("new-result", "search", "call_0", "found")
	newResult.TokenEstimate = 25

	frags := []contextfrag.ContextFrag{
		oldCall,
		oldResult,
		newCall,
		newResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 50})

	assertSelectedIDs(t, result, []string{"new-call", "new-result", "latest"})
	assertDroppedReason(t, result, "old-call", budgetDropReasonPassive)
	assertDroppedReason(t, result, "old-result", budgetDropReasonPassive)
}

// A must-keep marker on a newer reused-id exchange must not contaminate an
// unrelated older exchange sharing the same tool_call_id: id-keyed merging
// (rather than appearance-order scoping) would fold both into one unit,
// have the must-keep tag make the whole thing undroppable, and blow through
// the budget with the older exchange still attached — a guaranteed 400 that
// nothing in the older exchange itself justifies.
func TestBudgetAttention_ReusedToolCallIDOldExchangeDropsDespiteNewMustKeep(t *testing.T) {
	t.Parallel()

	oldCall := toolCallFrag("old-call", "search", "call_0")
	oldCall.TokenEstimate = 5000
	oldCall.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}
	oldResult := toolResultFrag("old-result", "search", "call_0", "found")
	oldResult.TokenEstimate = 5000

	newCall := toolCallFrag("new-call", "search", "call_0")
	newCall.TokenEstimate = 50
	newCall.Budget.Overflow = contextfrag.OverflowKeep
	newResult := toolResultFrag("new-result", "search", "call_0", "found")
	newResult.TokenEstimate = 50

	frags := []contextfrag.ContextFrag{
		oldCall,
		oldResult,
		newCall,
		newResult,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 200})

	assertSelectedIDs(t, result, []string{"new-call", "new-result", "latest"})
	assertDroppedReason(t, result, "old-call", budgetDropReasonPassive)
	assertDroppedReason(t, result, "old-result", budgetDropReasonPassive)
}

// A result observed before any call for its id has no open unit to close —
// it is itself an orphan — and the call that later opens the same id is
// never closed by anything after it, so it is an orphan too. Appearance-
// order scoping drops both unconditionally instead of the old union-find
// behavior that paired them regardless of order: result-before-call for the
// same id is not a shape real backends produce (ids are reused strictly
// call-then-result), so pairing it was solving a problem this design
// doesn't have, at the cost of ever dropping a genuine orphan wrong.
func TestBudgetAttention_ResultBeforeCallBothOrphan(t *testing.T) {
	t.Parallel()

	early := toolResultFrag("early-result", "search", "call-1", "found")
	early.TokenEstimate = 40
	later := toolCallFrag("later-call", "search", "call-1")
	later.TokenEstimate = 40
	later.Scope.Attention = []contextfrag.AttentionReason{contextfrag.AttentionPassive}

	frags := []contextfrag.ContextFrag{
		attentionMessageFrag("directed-old", sdk.UserMessage("@bot keep this"), 100, contextfrag.AttentionMention),
		early,
		later,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 1000})

	assertSelectedIDs(t, result, []string{"directed-old", "latest"})
	assertDroppedReason(t, result, "early-result", budgetDropReasonOrphanExchange)
	assertDroppedReason(t, result, "later-call", budgetDropReasonOrphanExchange)
	if result.TrimNotice {
		t.Fatal("orphan-only drops must not raise the trim notice")
	}
}

// Drops within a tier are contiguous oldest-first; priority no longer
// reorders them, so a newer zero-estimate unit is not sacrificed for zero
// gain before an older unit that actually frees tokens.
func TestBudgetAttention_OldestFirstWithinTierNoZeroGainScatter(t *testing.T) {
	t.Parallel()

	heavy := attentionMessageFrag("passive-heavy-old", sdk.UserMessage("very long chatter"), 100, contextfrag.AttentionPassive)
	heavy.Priority = 70
	zero := attentionMessageFrag("passive-zero-new", sdk.UserMessage("hi"), 0, contextfrag.AttentionPassive)
	zero.Priority = 10

	frags := []contextfrag.ContextFrag{
		heavy,
		zero,
		attentionMessageFrag("latest", sdk.UserMessage("latest"), 100, contextfrag.AttentionDirect),
	}

	result := selectProviderFrags(frags, BudgetEnvelope{MaxTokens: 50})

	assertSelectedIDs(t, result, []string{"passive-zero-new", "latest"})
	assertDroppedReason(t, result, "passive-heavy-old", budgetDropReasonPassive)
}

func assertSelectedIDs(t *testing.T, result SelectionResult, want []string) {
	t.Helper()
	got := fragIDs(result.Selected)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected ids = %#v, want %#v; dropped=%#v", got, want, fragIDs(result.Dropped))
	}
	if result.Summary.TotalSelected != len(want) {
		t.Fatalf("TotalSelected = %d, want %d", result.Summary.TotalSelected, len(want))
	}
}

func assertDroppedReason(t *testing.T, result SelectionResult, fragID, reason string) {
	t.Helper()
	for _, record := range result.Summary.DropReasons {
		if record.FragID == fragID {
			if record.Reason != reason {
				t.Fatalf("drop reason for %s = %q, want %q", fragID, record.Reason, reason)
			}
			return
		}
	}
	t.Fatalf("missing drop record for %s; records=%#v", fragID, result.Summary.DropReasons)
}

func toolCallFrag(id, name, callID string) contextfrag.ContextFrag {
	return messageFrag(id, sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{
			sdk.ToolCallPart{ToolCallID: callID, ToolName: name, Input: map[string]any{}},
		},
	})
}

func toolResultFrag(id, name, callID, result string) contextfrag.ContextFrag {
	return messageFrag(id, sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: callID,
		ToolName:   name,
		Result:     result,
	}))
}

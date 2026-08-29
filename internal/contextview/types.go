package contextview

import contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"

type BuildInput struct {
	Scope           contextfrag.Scope
	Intent          contextfrag.Intent
	Sources         []SourceSpec
	Targets         []contextfrag.RenderTarget
	Budget          BudgetEnvelope
	DynamicMutators []contextfrag.DynamicMutator
	Options         BuildOptions
}

type SourceSpec struct {
	Name   string
	Config any
}

type BudgetEnvelope struct {
	MaxTokens int
	// Plan activates unified provider-envelope budgeting. Nil preserves the
	// legacy unbudgeted provider selection path. Either Plan or
	// EnforceProtectedBudget makes the budget a provider allowance, which
	// charges fragments at least the provider envelope estimate.
	Plan *contextfrag.ContextBudgetPlan
	// EnforceProtectedBudget makes MaxTokens a hard allowance that includes
	// must-keep fragments without reactivating the turn-start system pass.
	// Step reselection uses it to fail closed when newly injected protected
	// context cannot fit the remaining provider envelope.
	EnforceProtectedBudget bool
	// RecentProtectTokens bands the newest droppable history within this many
	// charged tokens to drop last. Zero disables the window.
	RecentProtectTokens int
	ToolExchange        *contextfrag.ToolExchangePolicy
}

type BuildOptions struct {
	DryRun bool
}

type ContextView struct {
	Intent      contextfrag.Intent
	SourceFrags []contextfrag.ContextFrag
	Selected    []contextfrag.ContextFrag
	Placement   PlacementPlan
	Manifest    contextfrag.Manifest
	Rendered    map[contextfrag.RenderTarget]RenderedPayload
	Trace       BuildTrace
}

type RenderedPayload struct {
	Target      contextfrag.RenderTarget
	ContentHash string
	Data        any
}

type PlacementPlan struct {
	StablePrefixHash   string
	FirstVolatileIndex int
	Items              []PlacementItem
}

type PlacementItem struct {
	FragID    string
	Slot      contextfrag.Slot
	Position  int
	CacheHint contextfrag.CacheClass
	Ref       contextfrag.ContextRef
}

type BuildTrace struct {
	CollectDurations map[string]int64
	SelectionSummary SelectionSummary
	PlacementSummary PlacementSummary
	RenderSummaries  map[contextfrag.RenderTarget]RenderSummary
	Warnings         []contextfrag.ValidationWarning
}

type SelectionSummary struct {
	TotalCollected int
	TotalSelected  int
	TotalDropped   int
	DropReasons    []DropRecord
}

type DropRecord struct {
	FragID string
	Ref    contextfrag.ContextRef
	Reason string
}

type PlacementSummary struct {
	StablePrefixFrags int
	DynamicFrags      int
}

type RenderSummary struct {
	Target      contextfrag.RenderTarget
	ContentHash string
	ItemCount   int
}

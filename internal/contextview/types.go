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
	// Mutations is the run's live mutation ledger; renderers record
	// post-selection changes such as a final render prune onto it.
	Mutations *contextfrag.MutationLedger
}

type SourceSpec struct {
	Name   string
	Config any
}

type BudgetEnvelope struct {
	MaxTokens int
	// Plan activates unified provider-envelope budgeting. Nil preserves the
	// legacy unbudgeted provider selection path. Plan makes the budget a
	// provider allowance, which charges fragments at least the provider
	// envelope estimate.
	Plan *contextfrag.ContextBudgetPlan
	// RecentProtectTokens bands the newest droppable history within this many
	// charged tokens to drop last. Zero disables the window.
	RecentProtectTokens int
	// KeepHistory keeps every history fragment whatever the budget: an armed
	// run folds what does not fit before it dispatches. A tool exchange
	// missing its other half is still cut.
	KeepHistory bool
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

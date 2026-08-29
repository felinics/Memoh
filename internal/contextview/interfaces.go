package contextview

import (
	"context"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type CollectRequest struct {
	Scope  contextfrag.Scope
	Intent contextfrag.Intent
	Config any
}

type RenderInput struct {
	Intent    contextfrag.Intent
	Selected  []contextfrag.ContextFrag
	Placement PlacementPlan
	Manifest  *contextfrag.Manifest
	Scope     contextfrag.Scope
	Target    contextfrag.RenderTarget
}

type Collector interface {
	Name() string
	Collect(context.Context, CollectRequest) ([]contextfrag.ContextFrag, error)
}

type CollectorRegistry interface {
	Get(name string) (Collector, bool)
	Names() []string
}

type Selector interface {
	ProfileFor(contextfrag.Intent) IntentProfile
	Select([]contextfrag.ContextFrag, IntentProfile, BudgetEnvelope) SelectionResult
}

type SelectionResult struct {
	Selected []contextfrag.ContextFrag
	Dropped  []contextfrag.ContextFrag
	// FatalError stops rendering while allowing Builder to return the partial,
	// content-light selection audit accumulated before the failure.
	FatalError error
	// TrimNotice reports that budget trimming dropped history and the builder
	// must splice the trim notice into Selected at TrimNoticeIndex.
	TrimNotice      bool
	TrimNoticeIndex int
	Edited          []contextfrag.ContextEditTrace
	// EditReasons maps an edited fragment ID to its stable selection reason.
	EditReasons map[string]string
	Warnings    []contextfrag.ValidationWarning
	Summary     SelectionSummary
}

type IntentProfile struct {
	Intent        contextfrag.Intent
	MustKeepSlots []contextfrag.Slot
	// MustKeepFrag evaluates retention that depends on fragment policy rather
	// than provider placement alone.
	MustKeepFrag func(contextfrag.ContextFrag) bool
	// SlotTrustFloors declares the minimum trust level per slot.
	SlotTrustFloors map[contextfrag.Slot]contextfrag.TrustLevel
}

type Placer interface {
	Place([]contextfrag.ContextFrag, contextfrag.Intent) PlacementPlan
}

type Renderer interface {
	Target() contextfrag.RenderTarget
	Render(context.Context, RenderInput) (RenderedPayload, error)
}

type RendererRegistry interface {
	Get(contextfrag.RenderTarget) (Renderer, bool)
}

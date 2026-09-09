package contextview

import (
	"context"
	"sort"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type PassthroughSelector struct{}

func (PassthroughSelector) ProfileFor(intent contextfrag.Intent) IntentProfile {
	return IntentProfile{Intent: intent}
}

func (PassthroughSelector) Select(frags []contextfrag.ContextFrag, _ IntentProfile, _ BudgetEnvelope) SelectionResult {
	selected := append([]contextfrag.ContextFrag(nil), frags...)
	return SelectionResult{
		Selected: selected,
		Summary: SelectionSummary{
			TotalCollected: len(frags),
			TotalSelected:  len(selected),
		},
	}
}

type IdentityPlacer struct{}

func (IdentityPlacer) Place(selected []contextfrag.ContextFrag, _ contextfrag.Intent) PlacementPlan {
	items := make([]PlacementItem, 0, len(selected))
	for i, frag := range selected {
		items = append(items, PlacementItem{
			FragID:    frag.ID,
			Slot:      frag.Slot,
			Position:  i,
			CacheHint: frag.CacheClass,
			Ref:       frag.Ref,
		})
	}
	return PlacementPlan{
		FirstVolatileIndex: len(items),
		Items:              items,
	}
}

type NoopRenderer struct {
	TargetName contextfrag.RenderTarget
}

func (r NoopRenderer) Target() contextfrag.RenderTarget {
	return r.TargetName
}

func (r NoopRenderer) Render(_ context.Context, input RenderInput) (RenderedPayload, error) {
	target := r.TargetName
	if target == "" {
		target = input.Target
	}
	return RenderedPayload{Target: target}, nil
}

type MapCollectorRegistry struct {
	collectors map[string]Collector
}

func NewMapCollectorRegistry(collectors ...Collector) *MapCollectorRegistry {
	registry := &MapCollectorRegistry{collectors: make(map[string]Collector, len(collectors))}
	for _, collector := range collectors {
		if collector == nil {
			continue
		}
		registry.collectors[collector.Name()] = collector
	}
	return registry
}

func (r *MapCollectorRegistry) Get(name string) (Collector, bool) {
	if r == nil {
		return nil, false
	}
	collector, ok := r.collectors[name]
	if !ok || collector == nil {
		return nil, false
	}
	return collector, true
}

func (r *MapCollectorRegistry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.collectors))
	for name := range r.collectors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type MapRendererRegistry struct {
	renderers map[contextfrag.RenderTarget]Renderer
}

func NewMapRendererRegistry(renderers ...Renderer) *MapRendererRegistry {
	registry := &MapRendererRegistry{renderers: make(map[contextfrag.RenderTarget]Renderer, len(renderers))}
	for _, renderer := range renderers {
		if renderer == nil {
			continue
		}
		registry.renderers[renderer.Target()] = renderer
	}
	return registry
}

func (r *MapRendererRegistry) Get(target contextfrag.RenderTarget) (Renderer, bool) {
	if r == nil {
		return nil, false
	}
	renderer, ok := r.renderers[target]
	if !ok || renderer == nil {
		return nil, false
	}
	return renderer, true
}

type StaticCollector struct {
	CollectorName string
	Frags         []contextfrag.ContextFrag
}

func (c StaticCollector) Name() string {
	return c.CollectorName
}

func (c StaticCollector) Collect(_ context.Context, _ CollectRequest) ([]contextfrag.ContextFrag, error) {
	return append([]contextfrag.ContextFrag(nil), c.Frags...), nil
}

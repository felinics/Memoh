package contextview

import (
	"context"
	"errors"
	"fmt"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

type Builder struct {
	collectors CollectorRegistry
	selector   Selector
	placer     Placer
	renderers  RendererRegistry
}

func NewBuilder(collectors CollectorRegistry, selector Selector, placer Placer, renderers RendererRegistry) *Builder {
	return &Builder{
		collectors: collectors,
		selector:   selector,
		placer:     placer,
		renderers:  renderers,
	}
}

func (b *Builder) Build(ctx context.Context, input BuildInput) (*ContextView, error) {
	if b.collectors == nil {
		return nil, errors.New("collector registry is required")
	}
	if b.selector == nil {
		return nil, errors.New("selector is required")
	}
	if b.placer == nil {
		return nil, errors.New("placer is required")
	}

	trace := BuildTrace{
		CollectDurations: make(map[string]int64, len(input.Sources)),
		RenderSummaries:  make(map[contextfrag.RenderTarget]RenderSummary, len(input.Targets)),
	}
	sourceFrags := make([]contextfrag.ContextFrag, 0)
	for _, spec := range input.Sources {
		collector, ok := b.collectors.Get(spec.Name)
		if !ok {
			return nil, fmt.Errorf("unknown collector %q", spec.Name)
		}

		start := time.Now()
		frags, err := collector.Collect(ctx, CollectRequest{
			Scope:  input.Scope,
			Intent: input.Intent,
			Config: spec.Config,
		})
		trace.CollectDurations[spec.Name] = time.Since(start).Microseconds()
		if err != nil {
			return nil, fmt.Errorf("collector %q: %w", spec.Name, err)
		}
		sourceFrags = append(sourceFrags, frags...)
	}
	// Provider system fragments render into one leading system field even when
	// a late collector (for example tool usage) supplied them after message
	// fragments. Canonicalize that order before selection and placement so the
	// cache plan describes the same prefix the renderer emits.
	sourceFrags = sortSystemFragsByPriority(sourceFrags)
	sourceFrags = contextfrag.NormalizeContextRefs(sourceFrags)

	profile := b.selector.ProfileFor(input.Intent)
	result := b.selector.Select(sourceFrags, profile, input.Budget)
	trace.SelectionSummary = result.Summary

	placement := b.placer.Place(result.Selected, input.Intent)
	trace.PlacementSummary = summarizePlacement(placement)

	manifest := contextfrag.BuildManifest(result.Selected)
	manifest.View = input.Intent.ManifestView()
	manifest.DynamicMutators = normalizeDynamicMutators(input.DynamicMutators)
	manifest.Selection = selectionTrace(result.Summary)
	manifest.EditTrace = append(manifest.EditTrace, selectionEditTrace(result.Dropped)...)
	manifest.EditTrace = append(manifest.EditTrace, result.Edited...)
	manifest.ValidationWarnings = append(manifest.ValidationWarnings, result.Warnings...)
	trace.Warnings = append(trace.Warnings, manifest.ValidationWarnings...)

	view := &ContextView{
		Intent:      input.Intent,
		SourceFrags: sourceFrags,
		Selected:    result.Selected,
		Placement:   placement,
		Manifest:    manifest,
		Rendered:    make(map[contextfrag.RenderTarget]RenderedPayload, len(input.Targets)),
		Trace:       trace,
	}

	if input.Options.DryRun {
		return view, nil
	}

	for _, target := range input.Targets {
		if b.renderers == nil {
			return nil, fmt.Errorf("unknown renderer %q", target)
		}
		renderer, ok := b.renderers.Get(target)
		if !ok {
			return nil, fmt.Errorf("unknown renderer %q", target)
		}
		payload, err := renderer.Render(ctx, RenderInput{
			Intent:    input.Intent,
			Selected:  result.Selected,
			Placement: placement,
			Manifest:  &manifest,
			Scope:     input.Scope,
			Target:    target,
		})
		if err != nil {
			return nil, fmt.Errorf("renderer %q: %w", target, err)
		}
		if payload.Target == "" {
			payload.Target = target
		}
		view.Rendered[target] = payload
		view.Trace.RenderSummaries[target] = RenderSummary{
			Target:      payload.Target,
			ContentHash: payload.ContentHash,
			ItemCount:   len(placement.Items),
		}
	}

	return view, nil
}

func summarizePlacement(placement PlacementPlan) PlacementSummary {
	stable := placement.FirstVolatileIndex
	if stable < 0 || stable > len(placement.Items) {
		stable = len(placement.Items)
	}
	return PlacementSummary{
		StablePrefixFrags: stable,
		DynamicFrags:      len(placement.Items) - stable,
	}
}

func normalizeDynamicMutators(mutators []contextfrag.DynamicMutator) []contextfrag.DynamicMutator {
	if len(mutators) == 0 {
		return nil
	}
	out := make([]contextfrag.DynamicMutator, 0, len(mutators))
	seen := make(map[contextfrag.DynamicMutator]bool, len(mutators))
	for _, mutator := range mutators {
		if mutator == "" || seen[mutator] {
			continue
		}
		seen[mutator] = true
		out = append(out, mutator)
	}
	return out
}

func selectionTrace(summary SelectionSummary) *contextfrag.SelectionTrace {
	if summary.TotalCollected == 0 && summary.TotalSelected == 0 && summary.TotalDropped == 0 && len(summary.DropReasons) == 0 {
		return nil
	}
	trace := &contextfrag.SelectionTrace{
		Selected: summary.TotalSelected,
		Dropped:  summary.TotalDropped,
	}
	if len(summary.DropReasons) > 0 {
		trace.DropReasons = make(map[string]int, len(summary.DropReasons))
		for _, record := range summary.DropReasons {
			reason := record.Reason
			if reason == "" {
				reason = "unknown"
			}
			trace.DropReasons[reason]++
		}
	}
	return trace
}

func selectionEditTrace(dropped []contextfrag.ContextFrag) []contextfrag.ContextEditTrace {
	if len(dropped) == 0 {
		return nil
	}
	out := make([]contextfrag.ContextEditTrace, 0, len(dropped))
	for _, frag := range dropped {
		ref := frag.Ref
		if err := contextfrag.ValidateContextRef(ref); err != nil {
			ref = contextfrag.WithContextRef(frag, ref).Ref
		}
		out = append(out, contextfrag.ContextEditTrace{
			EditID: "selection.drop." + frag.ID,
			Op:     contextfrag.EditRemove,
			Slot:   frag.Slot,
			Refs:   []contextfrag.ContextRef{ref},
		})
	}
	return out
}

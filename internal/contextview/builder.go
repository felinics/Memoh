package contextview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
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
	if result.TrimNotice && result.TrimNoticeIndex >= 0 && result.TrimNoticeIndex <= len(result.Selected) {
		notice := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{TrimNoticeFrag(input.Scope)})[0]
		selected := make([]contextfrag.ContextFrag, 0, len(result.Selected)+1)
		selected = append(selected, result.Selected[:result.TrimNoticeIndex]...)
		selected = append(selected, notice)
		selected = append(selected, result.Selected[result.TrimNoticeIndex:]...)
		result.Selected = selected
	}

	placement := b.placer.Place(result.Selected, input.Intent)
	trace.PlacementSummary = summarizePlacement(placement)

	manifest := contextfrag.BuildManifest(result.Selected)
	manifest.View = input.Intent.ManifestView()
	manifest.DynamicMutators = normalizeDynamicMutators(input.DynamicMutators)
	manifest.Selection = selectionTrace(result.Summary)
	manifest.SelectionDecisions = selectionDecisions(sourceFrags, result)
	if input.Budget.Plan != nil {
		plan := *input.Budget.Plan
		manifest.BudgetPlan = &plan
	}
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

	if result.FatalError != nil {
		return view, result.FatalError
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

func selectionDecisions(sourceFrags []contextfrag.ContextFrag, result SelectionResult) []contextfrag.SelectionDecision {
	dropReasonsByRef := make(map[selectionRefKey][]string, len(result.Summary.DropReasons))
	legacyDropReasonsByID := make(map[string][]string)
	for _, record := range result.Summary.DropReasons {
		if key, ok := newSelectionRefKey(record.Ref); ok {
			dropReasonsByRef[key] = append(dropReasonsByRef[key], record.Reason)
			continue
		}
		legacyDropReasonsByID[record.FragID] = append(legacyDropReasonsByID[record.FragID], record.Reason)
	}
	selectedByRef := make(map[selectionRefKey][]int, len(result.Selected))
	for i, frag := range result.Selected {
		if key, ok := newSelectionRefKey(frag.Ref); ok {
			selectedByRef[key] = append(selectedByRef[key], i)
		}
	}

	decisions := make([]contextfrag.SelectionDecision, len(sourceFrags))
	decided := make([]bool, len(sourceFrags))
	selectedUsed := make([]bool, len(result.Selected))

	// DropRecord.Ref identifies the exact source candidate that was rejected.
	// Resolve those records before any selected-fragment matching so two
	// candidates sharing a debug ID cannot exchange their audit outcomes.
	for i, source := range sourceFrags {
		key, ok := newSelectionRefKey(source.Ref)
		if !ok {
			continue
		}
		reasons := dropReasonsByRef[key]
		if len(reasons) == 0 {
			continue
		}
		decisions[i] = selectionDecisionForFrag(source, contextfrag.DecisionDropped, reasons[0])
		decided[i] = true
		dropReasonsByRef[key] = reasons[1:]
	}

	// Match unchanged selections by the complete ContextRef, including the
	// content hash, before considering trim identity or legacy IDs.
	for i, source := range sourceFrags {
		if decided[i] {
			continue
		}
		key, ok := newSelectionRefKey(source.Ref)
		if !ok {
			continue
		}
		indexes := selectedByRef[key]
		for len(indexes) > 0 && selectedUsed[indexes[0]] {
			indexes = indexes[1:]
		}
		selectedByRef[key] = indexes
		if len(indexes) == 0 {
			continue
		}
		selectedIndex := indexes[0]
		decisions[i] = selectionDecisionForSelection(source, result.Selected[selectedIndex], result.EditReasons[source.ID])
		decided[i] = true
		selectedUsed[selectedIndex] = true
		selectedByRef[key] = indexes[1:]
	}

	// A trim keeps the ContextRef identity but refreshes its content hash.
	// Exact matches above must run first because multiple revisions of one
	// durable identity can legitimately appear in the same source set.
	for i, source := range sourceFrags {
		if decided[i] {
			continue
		}
		for selectedIndex, selected := range result.Selected {
			if selectedUsed[selectedIndex] || !source.Ref.EqualIdentity(selected.Ref) {
				continue
			}
			decisions[i] = selectionDecisionForSelection(source, selected, result.EditReasons[source.ID])
			decided[i] = true
			selectedUsed[selectedIndex] = true
			break
		}
	}

	// A budget edit can refresh a fragment's content hash before a later stage
	// drops it, so its drop record no longer carries the source's exact ref.
	// Match those records by ContextRef identity once selected matching is
	// done, consuming deterministically among the remaining hashes.
	for i, source := range sourceFrags {
		if decided[i] {
			continue
		}
		sourceKey, ok := newSelectionRefKey(source.Ref)
		if !ok {
			continue
		}
		candidateKeys := make([]selectionRefKey, 0, 1)
		for key, reasons := range dropReasonsByRef {
			if len(reasons) > 0 && key.identity == sourceKey.identity {
				candidateKeys = append(candidateKeys, key)
			}
		}
		if len(candidateKeys) == 0 {
			continue
		}
		sort.Slice(candidateKeys, func(a, b int) bool {
			return candidateKeys[a].contentHash < candidateKeys[b].contentHash
		})
		key := candidateKeys[0]
		reasons := dropReasonsByRef[key]
		decisions[i] = selectionDecisionForFrag(source, contextfrag.DecisionDropped, reasons[0])
		decided[i] = true
		dropReasonsByRef[key] = reasons[1:]
	}

	// Keep ID-only drop records for legacy selectors that did not provide a Ref.
	for i, source := range sourceFrags {
		if decided[i] {
			continue
		}
		if reasons := legacyDropReasonsByID[source.ID]; len(reasons) > 0 {
			decisions[i] = selectionDecisionForFrag(source, contextfrag.DecisionDropped, reasons[0])
			decided[i] = true
			legacyDropReasonsByID[source.ID] = reasons[1:]
		}
	}

	// Preserve the former ID fallback for trim/replacement selectors only when
	// one source and one selected fragment remain for that ID. Multiple
	// candidates are ambiguous and must never be paired by their debug ID.
	unresolvedSourcesByID := make(map[string]int)
	unusedSelectedByID := make(map[string]int)
	for i, source := range sourceFrags {
		if !decided[i] {
			unresolvedSourcesByID[source.ID]++
		}
	}
	for i, selected := range result.Selected {
		if !selectedUsed[i] {
			unusedSelectedByID[selected.ID]++
		}
	}
	for i, source := range sourceFrags {
		if decided[i] || unresolvedSourcesByID[source.ID] != 1 || unusedSelectedByID[source.ID] != 1 {
			continue
		}
		for selectedIndex, selected := range result.Selected {
			if selectedUsed[selectedIndex] || selected.ID != source.ID {
				continue
			}
			decisions[i] = selectionDecisionForSelection(source, selected, result.EditReasons[source.ID])
			decided[i] = true
			selectedUsed[selectedIndex] = true
			break
		}
	}

	for i, source := range sourceFrags {
		if !decided[i] {
			decisions[i] = selectionDecisionForFrag(source, contextfrag.DecisionDropped, "unknown")
		}
	}
	for i, selected := range result.Selected {
		if !selectedUsed[i] {
			reason := ""
			if selected.ID == systemBudgetMarkerID {
				reason = "system_budget_marker"
			}
			decisions = append(decisions, selectionDecisionForFrag(selected, contextfrag.DecisionSelected, reason))
		}
	}
	return decisions
}

type selectionRefKey struct {
	identity    string
	hashAlgo    string
	hashScope   string
	contentHash string
}

func newSelectionRefKey(ref contextfrag.ContextRef) (selectionRefKey, bool) {
	if strings.TrimSpace(ref.Namespace) == "" || strings.TrimSpace(ref.ID) == "" {
		return selectionRefKey{}, false
	}
	return selectionRefKey{
		identity:    ref.StableKey(),
		hashAlgo:    strings.TrimSpace(ref.HashAlgo),
		hashScope:   strings.TrimSpace(ref.HashScope),
		contentHash: strings.TrimSpace(ref.ContentHash),
	}, true
}

func selectionDecisionForSelection(source, selected contextfrag.ContextFrag, reason string) contextfrag.SelectionDecision {
	decision := contextfrag.DecisionSelected
	if source.Ref.ContentHash != selected.Ref.ContentHash ||
		contextfrag.ResolveFragTokens(source) != contextfrag.ResolveFragTokens(selected) {
		decision = contextfrag.DecisionTrimmed
	}
	return selectionDecisionForFrag(selected, decision, reason)
}

func selectionDecisionForFrag(
	frag contextfrag.ContextFrag,
	decision contextfrag.SelectionDecisionKind,
	reason string,
) contextfrag.SelectionDecision {
	itemManifest := contextfrag.BuildManifest([]contextfrag.ContextFrag{frag})
	item := contextfrag.ManifestItem{}
	if len(itemManifest.Items) > 0 {
		item = itemManifest.Items[0]
	}
	return contextfrag.SelectionDecision{
		ID:            frag.ID,
		Ref:           item.Ref,
		Slot:          frag.Slot,
		Source:        frag.Provenance.Source,
		SourceID:      frag.Provenance.SourceID,
		Decision:      decision,
		Reason:        reason,
		TokenEstimate: item.TokenEstimate,
		TextBytes:     item.TextBytes,
		ImageCount:    item.ImageCount,
		CacheClass:    frag.CacheClass,
		RetentionTier: frag.RetentionTier,
	}
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

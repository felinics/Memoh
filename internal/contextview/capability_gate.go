package contextview

import (
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

const capabilityGateDropReason = "capability_gated"

type capabilityGateSelector struct {
	delegate  Selector
	available map[string]struct{}
}

func newCapabilityGateSelector(
	delegate Selector,
	defs []contextfrag.ToolDefAccounting,
) *capabilityGateSelector {
	available := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		if name := strings.TrimSpace(def.Name); name != "" {
			available[name] = struct{}{}
		}
	}
	return &capabilityGateSelector{delegate: delegate, available: available}
}

func (s *capabilityGateSelector) ProfileFor(intent contextfrag.Intent) IntentProfile {
	return s.delegate.ProfileFor(intent)
}

func (s *capabilityGateSelector) Select(
	frags []contextfrag.ContextFrag,
	profile IntentProfile,
	budget BudgetEnvelope,
) SelectionResult {
	kept, gated := filterUnavailableCapabilities(frags, s.available)
	result := s.delegate.Select(kept, profile, budget)
	return appendCapabilityGateDrops(result, gated)
}

func filterUnavailableCapabilities(
	frags []contextfrag.ContextFrag,
	available map[string]struct{},
) ([]contextfrag.ContextFrag, []contextfrag.ContextFrag) {
	if len(frags) == 0 {
		return nil, nil
	}

	dropped := make([]bool, len(frags))
	groupItems := make(map[string]int)
	keptGroupItems := make(map[string]int)
	for i, frag := range frags {
		groupID := frag.Render.GroupID
		if groupID != "" && !isRenderGroupHeader(frag) {
			groupItems[groupID]++
		}
		if isRenderGroupHeader(frag) {
			continue
		}
		if requiresUnavailableCapability(frag, available) {
			dropped[i] = true
			continue
		}
		if groupID != "" {
			keptGroupItems[groupID]++
		}
	}
	for i, frag := range frags {
		if !isRenderGroupHeader(frag) {
			continue
		}
		groupID := frag.Render.GroupID
		if groupItems[groupID] > 0 {
			dropped[i] = keptGroupItems[groupID] == 0
			continue
		}
		dropped[i] = requiresUnavailableCapability(frag, available)
	}

	kept := make([]contextfrag.ContextFrag, 0, len(frags))
	var gated []contextfrag.ContextFrag
	for i, frag := range frags {
		if dropped[i] {
			gated = append(gated, frag)
			continue
		}
		kept = append(kept, frag)
	}
	return kept, gated
}

func isRenderGroupHeader(frag contextfrag.ContextFrag) bool {
	return frag.Render.GroupID != "" && frag.ID == frag.Render.GroupID+".header"
}

func requiresUnavailableCapability(
	frag contextfrag.ContextFrag,
	available map[string]struct{},
) bool {
	capability := strings.TrimSpace(frag.RequiredCapability)
	if frag.Slot != contextfrag.SlotSystem || capability == "" {
		return false
	}
	_, ok := available[capability]
	return !ok
}

func appendCapabilityGateDrops(
	result SelectionResult,
	gated []contextfrag.ContextFrag,
) SelectionResult {
	for _, frag := range gated {
		result.Dropped = append(result.Dropped, frag)
		result.Summary.DropReasons = append(result.Summary.DropReasons, DropRecord{
			FragID: frag.ID,
			Ref:    frag.Ref,
			Reason: capabilityGateDropReason,
		})
	}
	result.Summary.TotalCollected += len(gated)
	result.Summary.TotalDropped += len(gated)
	return result
}

func capabilitySafeFallbackConfig(
	cfg agentpkg.RunConfig,
	sourceFrags []contextfrag.ContextFrag,
	available map[string]struct{},
) agentpkg.RunConfig {
	normalized := contextfrag.NormalizeContextRefs(sourceFrags)
	kept, gated := filterUnavailableCapabilities(normalized, available)
	cfg.System = renderSystemOnly(withoutWorkspaceHookSystemSections(kept))
	cfg.ContextToolUsage = ""
	cfg.ContextToolUsageFrags = nil
	cfg.ContextFrags = nonSystemFrags(cfg.ContextFrags)
	result := appendCapabilityGateDrops(SelectionResult{}, gated)
	cfg.ContextManifest.Selection = selectionTrace(result.Summary)
	cfg.ContextManifest.SelectionDecisions = make(
		[]contextfrag.SelectionDecision,
		0,
		len(gated),
	)
	for _, frag := range gated {
		cfg.ContextManifest.SelectionDecisions = append(
			cfg.ContextManifest.SelectionDecisions,
			selectionDecisionForFrag(frag, contextfrag.DecisionDropped, capabilityGateDropReason),
		)
	}
	return cfg
}

func withoutWorkspaceHookSystemSections(frags []contextfrag.ContextFrag) []contextfrag.ContextFrag {
	out := make([]contextfrag.ContextFrag, 0, len(frags))
	for _, frag := range frags {
		if frag.Slot == contextfrag.SlotSystem &&
			frag.Kind == contextfrag.KindHookContext &&
			frag.Trust == contextfrag.TrustWorkspace &&
			frag.Provenance.Source == "hook_system_section" {
			continue
		}
		out = append(out, frag)
	}
	return out
}

func renderSystemOnly(frags []contextfrag.ContextFrag) string {
	payload := &SDKRenderedPayload{}
	var previous contextfrag.RenderPolicy
	hasPrevious := false
	for _, frag := range sortSystemFragsByPriority(frags) {
		if frag.Slot != contextfrag.SlotSystem {
			continue
		}
		renderSystemFrag(payload, frag, previous, hasPrevious)
		previous = frag.Render
		hasPrevious = true
	}
	return payload.System
}

func nonSystemFrags(frags []contextfrag.ContextFrag) []contextfrag.ContextFrag {
	out := make([]contextfrag.ContextFrag, 0, len(frags))
	for _, frag := range frags {
		if frag.Slot != contextfrag.SlotSystem {
			out = append(out, frag)
		}
	}
	return out
}
